package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"agent-host/internal/assembly"

	"agent-host/internal/state"
)

func runMemoryStorage(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	command := args[0]
	fs := flag.NewFlagSet("memory "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, root, cache, source string
	fs.StringVar(&home, "home", "", "Operational state home")
	if command == "migrate" {
		fs.StringVar(&root, "memory-root", "", "Authoritative memory file root")
		fs.StringVar(&cache, "memory-cache", "", "Rebuildable memory index root")
		fs.StringVar(&source, "memory-source", "", "Stable channel or CLI source identity")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 || home == "" {
		return errors.New("memory storage requires --home and no positional arguments")
	}
	if command == "migrate" && (root == "" || cache == "" || source == "") {
		return errors.New("migration requires --memory-root, --memory-cache and --memory-source")
	}
	var err error
	home, err = filepath.Abs(home)
	if err != nil {
		return err
	}
	if command != "verify" {
		release, err := state.Acquire(home)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, release()) }()
	}
	s, err := state.Open(home, command == "verify")
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, s.Close()) }()
	var report state.FileMemoryReport
	switch command {
	case "migrate":
		report, err = s.MigrateFileMemory(ctx, root, cache, source)
	case "verify":
		report, err = s.VerifyFileMemory(ctx)
	case "rollback":
		report, err = s.RollbackFileMemory(ctx)
	default:
		return errors.New("unknown memory storage command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}

func refreshFileRuntime(ctx context.Context, store *state.Store, o *options) error {
	if !store.FileMemoryEnabled() {
		return nil
	}
	raw, err := os.ReadFile(state.FileMemoryMarker(o.home))
	if err != nil {
		return err
	}
	var config state.FileMemoryConfig
	if err = json.Unmarshal(raw, &config); err != nil {
		return err
	}
	if o.workspaceCache == "" {
		o.workspaceCache = filepath.Dir(config.CacheRoot)
	}
	o.memorySource = config.SourceID
	o.procedureRecallHints = nil
	partition := store.WithPartition(o.agentID, o.audienceID)
	var scopes []string
	for _, p := range o.projects {
		scopes = append(scopes, p.Scope)
	}
	if len(scopes) == 0 {
		scopes = []string{o.scope}
	}
	if o.recallBinding != "" && len(scopes) == 1 && o.retrievalQuery != "" && utf8.RuneCountInString(o.retrievalQuery) <= 80 {
		recent, err := partition.RecentChannelRoutingContext(ctx, o.recallBinding, 3)
		if err != nil {
			return err
		}
		for _, message := range recent {
			if message.ResolvedScope != scopes[0] || message.RunID == "" {
				continue
			}
			candidates, err := partition.Candidates(ctx, scopes[0], message.RunID, "accepted")
			if err != nil {
				return err
			}
			for _, candidate := range candidates {
				if candidate.MemoryID == "" || len(o.procedureRecallHints) == 3 {
					continue
				}
				memory, err := partition.Memory(ctx, candidate.MemoryID)
				if err != nil {
					return err
				}
				if memory.Scope == scopes[0] && memory.Status == "active" && memory.Kind == "procedure" && (memory.ExpiresAt == nil || memory.ExpiresAt.After(time.Now())) {
					o.procedureRecallHints = append(o.procedureRecallHints, state.ProcedureRecallHint{RunID: message.RunID, MemoryID: candidate.MemoryID})
				}
			}
		}
	}
	o.memoryFingerprint, err = store.WithPartition(o.agentID, o.audienceID).MemoryFingerprint(ctx, scopes)
	if err != nil {
		return err
	}
	o.skillFingerprint, o.skillsPreselected = "", o.retrievalQuery != ""
	if o.skillsPreselected && !o.noTools {
		var matches, persistent []state.Memory
		for _, scope := range scopes {
			r, _, err := partition.RetrieveWithProcedureHints(ctx, scope, o.retrievalQuery, o.procedureRecallHints, 16)
			if err != nil {
				return err
			}
			matches = append(matches, r.Matches...)
			persistent = append(persistent, r.Persistent...)
		}
		o.skillFingerprint, err = assembly.SkillSetDigest(assembly.Select(matches, persistent, 12*1024), assembly.SkillIdentity{SourceIdentity: config.SourceID, AgentID: o.agentID, AudienceID: o.audienceID, Scopes: scopes}, time.Now())
	}
	return err
}

func fileRuntimeCompatibility(o options) string {
	if o.memoryFingerprint == "" {
		return ""
	}
	return o.memoryFingerprint + ":skills-v1:" + o.skillFingerprint
}
