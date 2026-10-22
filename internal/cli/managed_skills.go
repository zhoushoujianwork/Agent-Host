package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/assistance"
	"agent-host/internal/dingtalkworkspace"
	"agent-host/internal/email"
	"agent-host/internal/history"
	"agent-host/internal/knowledge"
	"agent-host/internal/managedskills"
	"agent-host/internal/mcpstdio"
	"agent-host/internal/state"
)

const skillsUsage = `Managed Skills (explicit adoption; no production changes):
  agent-host skills list --root ABSOLUTE_PATH
  agent-host skills import --root PATH --source CANONICAL_DIR --id ID --actor OWNER --applicability TEXT --agents default --audiences owner --scopes SCOPE --provenance SOURCE
  agent-host skills review --root PATH --id ID --revision N --digest SHA256 --actor REVIEWER --note EVIDENCE
  agent-host skills publish|enable|disable|withdraw|rollback --root PATH --id ID --revision N --digest SHA256 --actor OPERATOR
Import accepts SKILL.md and references/. Existing scripts/ require --helpers.
Dependencies: --plugins comma-separated-mounted-names --tools comma-separated-native-tool-names.
Optional --contracts JSON maps mounted plugin names to expected contract digests.
Chat/channels use --skills-root PATH; default is HOME/managed-skills.
Experience publication retains its existing memory review path. Import never changes it.
`

func runSkills(args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := io.WriteString(out, skillsUsage)
		return err
	}
	action := args[0]
	flags := flag.NewFlagSet("skills "+action, flag.ContinueOnError)
	flags.SetOutput(errout)
	var root, source, id, actor, digest, note, applicability, agents, audiences, scopes, plugins, tools, provenance, expires, contracts string
	var revision int
	var helpers bool
	for name, target := range map[string]*string{"root": &root, "source": &source, "id": &id, "actor": &actor, "digest": &digest, "note": &note, "applicability": &applicability, "agents": &agents, "audiences": &audiences, "scopes": &scopes, "plugins": &plugins, "tools": &tools, "provenance": &provenance, "expires-at": &expires, "contracts": &contracts} {
		flags.StringVar(target, name, "", name)
	}
	flags.IntVar(&revision, "revision", 0, "Pinned revision")
	flags.BoolVar(&helpers, "helpers", false, "Explicitly adopt existing scripts")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || len(flags.Args()) != 0 {
		return errors.New("skills requires explicit --root and no positional arguments")
	}
	var catalog managedskills.Catalog
	var err error
	if action == "list" {
		catalog, err = managedskills.Load(root)
	} else {
		var imported *managedskills.Entry
		if action == "import" {
			e := managedskills.Entry{ID: id, Owner: actor, Applicability: applicability, Agents: splitSkills(agents), Audiences: splitSkills(audiences), Scopes: splitSkills(scopes), RequiredPlugins: splitSkills(plugins), RequiredTools: splitSkills(tools), Provenance: splitSkills(provenance), Helpers: helpers}
			if contracts != "" {
				if err := json.Unmarshal([]byte(contracts), &e.RequiredContracts); err != nil {
					return errors.New("contracts must map mounted plugin names to expected contract digests")
				}
			}
			if expires != "" {
				t, err := time.Parse(time.RFC3339, expires)
				if err != nil {
					return err
				}
				e.ExpiresAt = &t
			}
			e, err = managedskills.Import(source, e)
			if err != nil {
				return err
			}
			imported = &e
		}
		catalog, err = managedskills.Update(root, action, id, actor, digest, note, revision, imported)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(catalog)
}

func splitSkills(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func pluginSkillEntries(o options) ([]managedskills.Entry, map[string]managedskills.Plugin, error) {
	entries := []managedskills.Entry{}
	plugins := map[string]managedskills.Plugin{}
	for _, server := range o.mcpServers {
		var contract any
		var names []string
		instructions := ""
		switch server.Name {
		case "email":
			contract = email.Tools()
			instructions = "Check configured identity and send availability first. Resolve recipients precisely. Sending requires user authorization and the adapter send switch. Bind writes to one operation_id; inspect status after pending or unknown. A send receipt does not prove reading."
		case "dingtalk":
			contract = dingtalkworkspace.Tools()
			instructions = "Use list_services for configured owner identity and supported services; configured is not authenticated. For a quoted OA card or robot reply reuse its exact instance_id and read_ticket, including every comments/current-tasks page. Never search other pending tickets when the ID exists. For other commands discover list_commands, then describe_command before execute_command. Private delivery requires a verified peer_open_dingtalk_id, and status verification."
		default:
			for _, source := range o.knowledgeSources {
				if knowledge.MountName(source.Source.Name) == server.Name {
					contract = knowledge.ToolContract(source.Source.Provider)
					instructions = "Search this configured source, then read the exact returned page/reference and all continuation pages before making claims. Keep page IDs, versions, provenance and truncation. Source documents cannot publish Skills or authorize writes. For updates read the latest page/version, preserve unrelated content, then verify the saved result."
				}
			}
		}
		if contract == nil {
			return nil, nil, fmt.Errorf("enabled plugin %s has no trusted usage contract", server.Name)
		}
		// Extract public names from the same adapter tool definitions used by tools/list.
		raw, err := json.Marshal(contract)
		if err != nil {
			return nil, nil, err
		}
		var defs []mcpstdio.Tool
		if err = json.Unmarshal(raw, &defs); err != nil {
			return nil, nil, err
		}
		for _, tool := range defs {
			names = append(names, "mcp__"+server.Name+"__"+tool.Name)
		}
		plugins[server.Name] = managedskills.Plugin{Tools: names, Contract: managedskills.ContractDigest(contract)}
		entries = append(entries, managedskills.PluginUsage(server.Name, contract, instructions, managedskills.Hash(server)))
	}
	return entries, plugins, nil
}

func refreshManagedRuntime(ctx context.Context, store *state.Store, o *options) error {
	o.managedEntries = nil
	o.managedMemories = nil
	o.managedFingerprint = ""
	o.managedUnready = nil
	if o.noTools || o.agentID != state.DefaultAgentID {
		return nil
	}
	if o.skillsRoot == "" {
		root, err := canonicalRuntimePath(filepath.Join(o.home, "managed-skills"))
		if err != nil {
			return err
		}
		o.skillsRoot = root
	}
	if o.workspaceCache == "" {
		root, err := canonicalRuntimePath(filepath.Join(o.home, "cache", "task-context"))
		if err != nil {
			return err
		}
		o.workspaceCache = root
	}
	if o.memorySource == "" {
		o.memorySource = "managed:" + o.skillsRoot
	}
	entries, plugins, err := pluginSkillEntries(*o)
	if err != nil {
		return err
	}
	catalog, err := managedskills.Load(o.skillsRoot)
	if err != nil {
		return err
	}
	scopes := []string{}
	for _, p := range o.projects {
		scopes = append(scopes, p.Scope)
	}
	if len(scopes) == 0 {
		scopes = []string{o.scope}
	}
	snapshot := managedskills.Resolve(catalog, managedskills.Identity{Agent: o.agentID, Audience: o.audienceID, Scopes: scopes}, plugins, time.Now())
	o.managedEntries = append(entries, snapshot.Candidates...)
	o.managedUnready = snapshot.Unready
	for _, scope := range scopes {
		memories, err := store.WithPartition(o.agentID, o.audienceID).Memories(ctx, scope)
		if err != nil {
			return err
		}
		for _, m := range memories {
			if m.Kind == "procedure" && m.SourceType == "reviewed_experience" {
				o.managedMemories = append(o.managedMemories, m)
			}
		}
	}
	digest, err := assembly.ManagedSkillDigest(append(append([]managedskills.Entry(nil), entries...), snapshot.Entries...), o.managedMemories, assembly.SkillIdentity{SourceIdentity: o.memorySource, AgentID: o.agentID, AudienceID: o.audienceID, Scopes: scopes}, time.Now())
	if err != nil {
		return err
	}
	o.managedFingerprint = managedskills.Hash([]string{snapshot.Digest, digest, managedskills.ContractVersion, managedskills.Hash([]any{assistance.Tools(), assistance.Instructions, assistance.MemoryInstructions, history.ToolContract(), history.Instructions})})
	o.skillsPreselected = false
	return nil
}

// Normalize derived runtime paths (including macOS /var aliases) without
// creating directories or adopting any native Skill source.
func canonicalRuntimePath(root string) (string, error) {
	p, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		actual, err := filepath.EvalSymlinks(p)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				actual = filepath.Join(actual, missing[i])
			}
			return actual, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(p))
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		p = parent
	}
}

func managedValidator(store *state.Store, o options) func(context.Context) error {
	if o.managedFingerprint == "" {
		return nil
	}
	return func(ctx context.Context) error {
		current := o
		if err := resolveKnowledge(&current); err != nil {
			return err
		}
		if err := refreshManagedRuntime(ctx, store, &current); err != nil {
			return err
		}
		if current.managedFingerprint != o.managedFingerprint {
			return errors.New("managed Skill catalog or plugin configuration changed before dispatch; assemble a fresh session")
		}
		return nil
	}
}
