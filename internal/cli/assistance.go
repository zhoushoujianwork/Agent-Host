package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"agent-host/internal/assistance"
	"agent-host/internal/discovery"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

const assistanceUsage = `Owner-private context and preparation, using existing registered projects.

  agent-host assistance refresh --project ID --config PATH [options]
  agent-host assistance matters --project ID --config PATH [options]
  agent-host assistance search --project ID --config PATH [options] QUERY
  agent-host assistance show --project ID --config PATH [options] MATTER_ID
  agent-host assistance suggestions --project ID --config PATH [options]
  agent-host assistance suggestion --project ID --config PATH [options] SUGGESTION_ID
  agent-host assistance postpone --project ID --config PATH --revision N --until RFC3339 [options] SUGGESTION_ID
  agent-host assistance dismiss --project ID --config PATH --revision N [options] SUGGESTION_ID
  agent-host assistance resolve --project ID --config PATH --revision N [options] SUGGESTION_ID
  agent-host assistance prepare --project ID --config PATH --revision N [options] SUGGESTION_ID

Options include --home, --agent, --claude, --model and normal chat runtime options.
--config is the private discovery configuration; --discovery-config is equivalent.
Inspection and feedback require no model credentials. refresh performs bounded
selected-source DWS collection and native analysis. prepare starts exactly one
source-linked preparation task; repeated selection never replays it. Feedback
changes the matter/suggestion lifecycle, never durable memory. No messages are
sent automatically. Stop the managed service before standalone mutations.
Owner-only single-project scope; general personal memory and chat learning are later work.
`

func runContextMCP(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	fs := flag.NewFlagSet("context-mcp", flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, path, runID, fingerprint string
	fs.StringVar(&home, "home", "", "Private Host state home")
	fs.StringVar(&path, "config", "", "Selected-source configuration")
	fs.StringVar(&runID, "run", "", "Admitted current task")
	fs.StringVar(&fingerprint, "fingerprint", "", "Expected access binding")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || !filepath.IsAbs(home) || runID == "" || fingerprint == "" || path != "" && !filepath.IsAbs(path) {
		return errors.New("context-mcp requires absolute --home, --run and --fingerprint")
	}
	b, err := assistance.Resolve(ctx, home, path, runID)
	if err != nil {
		return err
	}
	if b.Fingerprint != fingerprint {
		return state.ErrContextUnavailable
	}
	return assistance.Serve(ctx, b, in, out)
}

func runAssistance(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, assistanceUsage)
		return err
	}
	command := args[0]
	switch command {
	case "refresh", "matters", "search", "show", "suggestions", "suggestion", "postpone", "dismiss", "resolve", "prepare":
	default:
		return errors.New("assistance requires a documented subcommand")
	}
	o, pos, err := parse("assistance "+command, args[1:], errout)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	needsID := command == "search" || command == "show" || command == "suggestion" || command == "postpone" || command == "dismiss" || command == "resolve" || command == "prepare"
	if needsID && len(pos) != 1 || !needsID && len(pos) != 0 || o.projectID == "" || o.discoveryConfig == "" || o.audienceID != state.OwnerAudienceID {
		return errors.New("assistance needs --project, --config and owner audience, with a query/ID only where documented")
	}
	mutates := command == "refresh" || command == "postpone" || command == "dismiss" || command == "resolve" || command == "prepare"
	if mutates {
		release, err := state.Acquire(o.home)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, release()) }()
	}
	store, err := state.Open(o.home, !mutates)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	store = store.WithPartition(o.agentID, o.audienceID)
	if command == "prepare" && o.agentsConfig != "" {
		catalog, err := loadAgents(o.agentsConfig)
		if err != nil {
			return err
		}
		if err := catalog.validateReferences(ctx, store, o); err != nil {
			return err
		}
		a, err := catalog.definition(o.agentID)
		if err != nil {
			return err
		}
		o, err = a.options(o, o.audienceID, false, true)
		if err != nil {
			return err
		}
	}
	if err := resolveOptions(ctx, store, &o, command == "prepare"); err != nil {
		return err
	}
	sources, err := assistance.SelectedSources(ctx, store, o.discoveryConfig, o.projectID)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return errors.New("no selected DWS sources mapped to this project")
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	switch command {
	case "refresh":
		cfg, err := discovery.LoadConfig(o.discoveryConfig)
		if err != nil {
			return err
		}
		selected := []discovery.Source{}
		for _, s := range cfg.Sources {
			if s.ProjectID == o.projectID {
				selected = append(selected, s)
			}
		}
		cfg.Sources = selected
		collector, err := composeDiscovery(ctx, store, cfg, o.binary)
		if err != nil {
			return err
		}
		report, err := collector.Collect(ctx)
		return errors.Join(encoder.Encode(report), err)
	case "matters", "search":
		query := ""
		if command == "search" {
			query = pos[0]
		}
		views, err := store.SearchMatters(ctx, o.projectID, sources, query, 20)
		if err != nil {
			return err
		}
		return encoder.Encode(views)
	case "show":
		view, err := store.MatterView(ctx, o.projectID, sources, pos[0])
		if err != nil {
			return err
		}
		evidence, err := store.MatterEvidence(ctx, o.projectID, sources, pos[0])
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"view": view, "evidence": evidence})
	case "suggestions":
		views, err := assistance.SuggestionViews(ctx, store, o.discoveryConfig, o.projectID, sources)
		if err != nil {
			return err
		}
		return encoder.Encode(views)
	case "suggestion":
		r, v, err := store.Suggestion(ctx, o.projectID, sources, pos[0])
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"suggestion": r, "matter": v})
	case "postpone", "dismiss", "resolve":
		var until time.Time
		if command == "postpone" {
			until, err = time.Parse(time.RFC3339, o.reviewAt)
			if err != nil {
				return errors.New("postpone requires --until RFC3339")
			}
		}
		if err := store.FeedbackSuggestion(ctx, o.projectID, sources, pos[0], o.suggestionRevision, command, until); err != nil {
			return err
		}
		r, v, err := store.Suggestion(ctx, o.projectID, sources, pos[0])
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"suggestion": r, "matter": v})
	}
	if o.suggestionRevision < 1 {
		return errors.New("prepare requires --revision from the current private suggestion list")
	}
	client, err := newJevClient(o.provider, o.jevEndpoint, o.jevModel)
	if err != nil {
		return err
	}
	if err := store.Recover(ctx); err != nil {
		return err
	}
	renderer := &terminal.Renderer{Out: out, JSON: o.format == "json"}
	h := composeHost(store, o, client, renderer.Render)
	session, err := h.Session(ctx, "")
	if err != nil {
		return err
	}
	turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	_, err = h.RunPreparation(turnCtx, session, pos[0], o.suggestionRevision)
	return err
}
