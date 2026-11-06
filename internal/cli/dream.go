package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"io"

	"agent-host/internal/dream"
)

const dreamUsage = `Read-only dream evidence preview:
  agent-host dream scan --source ABS --person ID --agent ID --audience ID --memory-scope SCOPE --format json

Manual dream lifecycle (all require exact person/agent/audience/scope and --format json):
  agent-host dream run --home PATH --source ABS --memory-offload-config FILE [partition flags]
  agent-host dream list|show --home PATH [--id ID] [--status pending|accepted|rejected|promoted|expired|all] [partition flags]
  agent-host dream work-list|work-show --home PATH [--id ID] [--status pending|processed|missing_evidence|retryable|retired|all] [partition flags]
  agent-host dream cleanup-list --home PATH [--status planned|completed|all] [partition flags]
  agent-host dream review --home PATH --id ID --status accept|reject --actor ID --note TEXT [partition flags]
  agent-host dream promote --home PATH --id ID --skills-root ABS --actor ID [--target-skill-id ID --target-skill-revision N --target-skill-digest DIGEST] [partition flags]
  agent-host dream export --home PATH --limit N [partition flags]
  agent-host dream archive --home PATH --limit N [partition flags]
  agent-host dream rotate|restore --home PATH --id MEMORY --revision N --actor ID --note TEXT [partition flags]
  agent-host dream status --home PATH --dream-config FILE --limit N --format json
  agent-host dream authorization --home PATH [partition flags] --format json
  agent-host dream revoke --home PATH --actor ID --note REASON [partition flags] --format json

The scan reads private handoff-* directories and emits one deterministic JSON
preview. It writes no state, invokes no model, and does not create receipts.
`

func runDream(args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, dreamUsage)
		return err
	}
	if args[0] != "scan" {
		return runDreamLifecycle(args, out, errout)
	}
	fs := flag.NewFlagSet("dream scan", flag.ContinueOnError)
	fs.SetOutput(errout)
	var source, person, agent, audience, scope, format string
	fs.StringVar(&source, "source", "", "Absolute private handoff root")
	fs.StringVar(&person, "person", "", "Trusted destination person")
	fs.StringVar(&agent, "agent", "", "Trusted destination Agent")
	fs.StringVar(&audience, "audience", "", "Trusted destination audience")
	fs.StringVar(&scope, "memory-scope", "", "Trusted destination memory scope")
	fs.StringVar(&format, "format", "", "Output format (json)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("dream scan accepts named options only")
	}
	if format != "json" {
		return errors.New("dream scan requires explicit --format json")
	}
	report, err := dream.Scan(source, dream.Destination{PersonID: person, Agent: agent, Audience: audience, MemoryScope: scope})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
