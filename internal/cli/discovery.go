package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"agent-host/internal/assistance"
	"agent-host/internal/discovery"
	"agent-host/internal/harness/claude"
	"agent-host/internal/state"
)

const discoveryUsage = `System DWS topic discovery, separate from task MCP tools.

  agent-host discovery check --config ABSOLUTE_PATH [--home PATH]
  agent-host discovery collect --config ABSOLUTE_PATH [--home PATH] [--claude PATH]
  agent-host discovery serve --config ABSOLUTE_PATH [--home PATH] [--claude PATH]
  agent-host discovery sources [--home PATH]
  agent-host discovery topics [--status pending|accepted|dismissed|all] [--limit N] [--home PATH]
  agent-host discovery show [--home PATH] TOPIC_ID
  agent-host discovery accept --project PROJECT_ID [--home PATH] TOPIC_ID
  agent-host discovery dismiss [--home PATH] TOPIC_ID

Only explicitly configured conversations are collected under one fixed DWS profile.
check validates private local configuration and registered project mappings only.
collect runs one bounded history reconciliation and tool-free topic extraction.
serve repeats this independently; managed DingTalk can share its store using
discovery_config in service.json. Stop that service before standalone mutations.
Topic acceptance exports bounded owner-private evidence; it executes no task,
sends no message, and publishes no memory. Inspection requires no model credentials.
`

func runDiscovery(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, discoveryUsage)
		return err
	}
	command := args[0]
	switch command {
	case "check", "collect", "serve", "sources", "topics", "show", "accept", "dismiss":
	default:
		return errors.New("discovery requires a documented subcommand")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("discovery "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, path, binary, status, project string
	var limit int
	fs.StringVar(&home, "home", filepath.Join(userHome, ".agent-host"), "Private Host state home")
	fs.StringVar(&path, "config", "", "Private system discovery configuration")
	fs.StringVar(&binary, "claude", "claude", "Native executable for tool-free topic extraction")
	fs.StringVar(&status, "status", "pending", "Topic state filter")
	fs.StringVar(&project, "project", "", "Explicit registered project for accepted evidence")
	fs.IntVar(&limit, "limit", 20, "Inspection limit, 1-100")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	needsID := command == "show" || command == "accept" || command == "dismiss"
	if needsID && len(fs.Args()) != 1 || !needsID && len(fs.Args()) != 0 || limit < 1 || limit > 100 {
		return errors.New("provide documented options before the single topic ID; limit must be 1-100")
	}
	if command == "accept" && project == "" {
		return errors.New("topic acceptance requires an explicit --project")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return err
	}
	var config discovery.Config
	if command == "check" || command == "collect" || command == "serve" {
		config, err = discovery.LoadConfig(path)
		if err != nil {
			return err
		}
		if err := config.Check(ctx, nil); err != nil {
			return err
		}
		if config.OwnerAnswering.Enabled {
			return errors.New("owner_answering is supported only by the DingTalk channel service, not standalone discovery")
		}
	}
	encoder := json.NewEncoder(out)
	if command == "check" {
		if _, err := os.Stat(filepath.Join(home, "state.db")); err == nil {
			store, err := state.OpenForProjectInspection(home)
			if err != nil {
				return err
			}
			defer func() { runErr = errors.Join(runErr, store.Close()) }()
			if err := config.Check(ctx, store); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect discovery project registration")
		}
		return encoder.Encode(map[string]any{"status": "local_configuration_valid", "sources": len(config.Sources), "remote_verified": false, "native_auth_verified": false})
	}
	writable := command == "collect" || command == "serve" || command == "accept" || command == "dismiss"
	if writable {
		release, err := state.Acquire(home)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, release()) }()
	}
	store, err := state.Open(home, !writable)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	switch command {
	case "sources":
		sources, err := store.DiscoverySources(ctx)
		if err != nil {
			return err
		}
		views := []map[string]any{}
		for _, source := range sources {
			view := map[string]any{"id": source.ID, "conversation_id": source.ConversationID, "type": source.Type, "project_id": source.ProjectID, "checkpoint": source.Checkpoint, "extraction_status": source.ExtractionStatus, "extraction_failed": source.ExtractionError != ""}
			if c := source.LastCollection; c != nil {
				view["last_collection"] = map[string]any{"from": c.From, "to": c.To, "at": c.At, "complete": c.Complete, "partial": c.Partial, "has_more": c.HasMore, "messages": c.Count, "pages_fetched": c.PagesFetched, "failed": c.Error != "", "target_verification": c.TargetVerification}
			}
			views = append(views, view)
		}
		return encoder.Encode(views)
	case "topics":
		if status == "all" {
			status = ""
		}
		topics, err := store.TopicProposals(ctx, status, limit)
		if err != nil {
			return err
		}
		return encoder.Encode(topics)
	case "show":
		topic, err := store.TopicReview(ctx, fs.Args()[0])
		if err != nil {
			return err
		}
		return encoder.Encode(topic)
	case "accept":
		evidence, err := store.AcceptTopic(ctx, fs.Args()[0], project)
		if err != nil {
			return err
		}
		return encoder.Encode(evidence)
	case "dismiss":
		if err := store.DismissTopic(ctx, fs.Args()[0]); err != nil {
			return err
		}
		topic, err := store.TopicProposal(ctx, fs.Args()[0])
		if err != nil {
			return err
		}
		return encoder.Encode(topic)
	}
	collector, err := composeDiscovery(ctx, store, config, binary)
	if err != nil {
		return err
	}
	if command == "collect" {
		report, collectErr := collector.Collect(ctx)
		return errors.Join(encoder.Encode(report), collectErr)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writeErr error
	loopErr := collector.Loop(loopCtx, func(report discovery.Report, collectErr error) {
		// Even configuration/storage failures with an empty source report remain
		// visible. Never include raw source, model output, or provider stderr.
		if err := encoder.Encode(discoveryCycleStatus(report, collectErr)); err != nil {
			writeErr = errors.New("cannot write discovery status")
			cancel()
		}
	})
	if writeErr != nil {
		return writeErr
	}
	return loopErr
}

func discoveryCycleStatus(report discovery.Report, cycleErr error) map[string]any {
	status := map[string]any{"sources": report.Sources, "status": "completed"}
	for _, source := range report.Sources {
		if !source.Complete || source.ExtractionPartial {
			status["status"] = "partial"
		}
	}
	if cycleErr != nil {
		status["status"] = "failed"
		status["error"] = "system discovery cycle failed; inspect source status and local configuration"
	}
	return status
}

func composeDiscovery(ctx context.Context, store *state.Store, config discovery.Config, binary string) (*discovery.Collector, error) {
	if config.OwnerAnswering.Enabled {
		return nil, errors.New("owner_answering requires the managed DingTalk channel consumer")
	}
	return composeDiscoveryCollector(ctx, store, config, binary)
}

// Only the DingTalk channel may attach the explicitly delegated answering consumer.
func composeDiscoveryCollector(ctx context.Context, store *state.Store, config discovery.Config, binary string) (*discovery.Collector, error) {
	if err := config.Check(ctx, store); err != nil {
		return nil, err
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, errors.New("configured discovery Claude executable is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	runner := claude.New(resolved)
	return discovery.NewCollector(store, config, discovery.NewDWSProvider(config), discovery.NewExtractor(runner, config.Model)).WithMatters(&assistance.Processor{Store: store, Runner: runner, Model: config.Model}), nil
}

func checkDiscoveryConfig(ctx context.Context, path, home string) (runErr error) {
	config, err := discovery.LoadConfig(path)
	if err != nil {
		return err
	}
	if err := config.Check(ctx, nil); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("cannot inspect discovery project registration")
	}
	store, err := state.OpenForProjectInspection(home)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	return config.Check(ctx, store)
}
