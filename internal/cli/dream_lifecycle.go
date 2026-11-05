package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"agent-host/internal/dream"
	"agent-host/internal/harness/nativepolicy"
	"agent-host/internal/host"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
	"agent-host/internal/state"
)

func runDreamLifecycle(args []string, out, errout io.Writer) error {
	action := args[0]
	fs := flag.NewFlagSet("dream "+action, flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, source, person, agent, audience, scope, status, id, actor, note, config, skillsRoot, format string
	var timeoutText string
	var limit int
	var revision int
	for name, target := range map[string]*string{"home": &home, "source": &source, "person": &person, "agent": &agent, "audience": &audience, "memory-scope": &scope, "status": &status, "id": &id, "actor": &actor, "note": &note, "memory-offload-config": &config, "skills-root": &skillsRoot, "format": &format} {
		fs.StringVar(target, name, "", name)
	}
	fs.IntVar(&limit, "limit", 50, "bounded result limit")
	fs.StringVar(&timeoutText, "timeout", "5m", "extraction timeout")
	fs.IntVar(&revision, "revision", 0, "expected authoritative revision")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || format != "json" {
		return errors.New("dream lifecycle commands require named flags and explicit --format json")
	}
	if home == "" {
		return errors.New("dream lifecycle command requires --home")
	}
	p := state.DreamPartition{PersonID: person, AgentID: agent, AudienceID: audience, Scope: scope}
	ctx := context.Background()
	write := action == "run" || action == "review" || action == "archive" || action == "rotate" || action == "restore" || action == "promote"
	var release func() error
	var err error
	if write {
		release, err = state.Acquire(home)
		if err != nil {
			return err
		}
		defer release()
	}
	store, err := state.Open(home, !write)
	if err != nil {
		return err
	}
	defer store.Close()
	partitioned := store.WithPartition(agent, audience)
	encode := func(value any) error { e := json.NewEncoder(out); e.SetIndent("", "  "); return e.Encode(value) }
	switch action {
	case "run":
		if config == "" || source == "" {
			return errors.New("dream run requires --source and --memory-offload-config")
		}
		profile, _, err := offloadchannel.Load(config)
		if err != nil {
			return err
		}
		client, err := profile.NewClient()
		if err != nil {
			return err
		}
		library, ok := client.(*offloadexec.Library)
		if !ok || library.Backend() != "claude" || library.Model() == "" {
			return errors.New("dream run requires configured strict tool-free Claude backend; unsupported backend was not dispatched")
		}
		timeout, parseErr := time.ParseDuration(timeoutText)
		if parseErr != nil || timeout <= 0 {
			return errors.New("dream run requires positive --timeout")
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		extractor := host.DreamExtractor{Runner: nativepolicy.NewWithLibrary("", library), Model: library.Model(), Backend: library.Backend()}
		receipts, err := (dream.Lifecycle{Store: partitioned, Extractor: extractor, Batch: limit}).Process(runCtx, source, dream.Destination{PersonID: person, Agent: agent, Audience: audience, MemoryScope: scope})
		if err != nil {
			return err
		}
		return encode(receipts)
	case "list":
		items, err := partitioned.DreamCandidates(ctx, p, status, limit)
		if err != nil {
			return err
		}
		return encode(items)
	case "show":
		if id == "" {
			return errors.New("dream show requires --id")
		}
		item, err := partitioned.DreamCandidate(ctx, id)
		if err != nil {
			return err
		}
		if item.Partition != p {
			return errors.New("dream candidate does not match exact requested partition")
		}
		return encode(item)
	case "review":
		decision := status
		if decision != "accept" && decision != "reject" {
			return errors.New("dream review requires --status accept|reject")
		}
		item, err := partitioned.DreamCandidate(ctx, id)
		if err != nil {
			return err
		}
		if item.Partition != p {
			return errors.New("dream candidate does not match exact requested partition")
		}
		item, err = dream.ReviewCandidate(ctx, partitioned, id, actor, note, decision)
		if err != nil {
			return err
		}
		return encode(item)
	case "archive":
		count, err := partitioned.ArchiveExpiredDream(ctx, p, time.Now().UTC(), limit)
		if err != nil {
			return err
		}
		return encode(map[string]any{"archived": count, "partition": p})
	case "rotate", "restore":
		if id == "" {
			return errors.New("dream rotate/restore requires --id")
		}
		if action == "rotate" {
			err = partitioned.RetireDreamMemory(ctx, p, id, actor, note, revision)
		} else {
			err = partitioned.RestoreDreamMemory(ctx, p, id, actor, note, revision)
		}
		if err != nil {
			return err
		}
		return encode(map[string]any{"action": action, "id": id, "revision": revision + 1, "partition": p})
	case "export":
		bundle, err := dream.ExportHOT(ctx, partitioned, p, time.Now().UTC(), limit)
		if err != nil {
			return err
		}
		return encode(bundle)
	case "promote":
		if skillsRoot == "" || id == "" {
			return errors.New("dream promote requires --id and --skills-root")
		}
		item, err := partitioned.DreamCandidate(ctx, id)
		if err != nil {
			return err
		}
		if item.Partition != p {
			return errors.New("dream candidate does not match exact requested partition")
		}
		entry, err := dream.PromoteSkill(ctx, partitioned, id, skillsRoot, actor)
		if err != nil {
			return err
		}
		return encode(entry)
	default:
		return errors.New("unknown dream lifecycle command")
	}
}
