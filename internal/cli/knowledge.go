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

	"agent-host/internal/assistance"
	"agent-host/internal/harness"
	"agent-host/internal/knowledge"
)

const knowledgeUsage = `Runtime sources: local Markdown/Dokki retrieval and Confluence page/blog retrieval and writes.

  agent-host knowledge check --config ABSOLUTE_PATH
  agent-host chat --knowledge-config ABSOLUTE_PATH [options]

Configuration and provider token files must be owner-private (0600 or 0400).
check validates local files only; no remote search or authentication is performed.
Enabled datasource.plugins.* configurations are global across channels, Agents,
projects and audiences. Legacy direct configurations retain their selectors.
Legacy default means unregistered cwd. Local sources require explicit relative paths.
Native permissions apply. The internal serve command runs only pinned MCP instances.
`

func runKnowledge(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, knowledgeUsage)
		return err
	}
	command := args[0]
	if command != "check" && command != "serve" {
		return errors.New("knowledge requires check")
	}
	fs := flag.NewFlagSet("knowledge "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var path, source, fingerprint string
	fs.StringVar(&path, "config", "", "Absolute knowledge configuration path")
	if command == "serve" {
		fs.StringVar(&source, "source", "", "Selected source name")
		fs.StringVar(&fingerprint, "fingerprint", "", "Pinned source binding")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 || path == "" {
		return errors.New("provide --config only with documented options")
	}
	if command == "serve" {
		return knowledge.Serve(ctx, path, source, fingerprint, in, out)
	}
	c, err := knowledge.Load(path)
	if err != nil {
		return err
	}
	type checkedSource struct {
		knowledge.Hint
		Fingerprint string   `json:"fingerprint"`
		Projects    []string `json:"projects"`
		Audiences   []string `json:"audiences,omitempty"`
	}
	hints := []checkedSource{}
	for _, s := range c.Sources {
		// Operator inspection checks each source even when it is restricted to
		// an audience other than the local owner. No document content is read.
		r, err := knowledge.ResolveConfiguredSource(s)
		if err != nil {
			return err
		}
		hints = append(hints, checkedSource{Hint: r.Hint(), Fingerprint: r.Fingerprint, Projects: r.Source.Projects, Audiences: r.Source.Audiences})
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "sources": hints, "remote_verified": false})
}

func resolveKnowledge(o *options) error {
	o.contextPolicy, o.contextExecutable = "", ""
	o.knowledgeSources = nil
	o.mcpServers = nil
	o.knowledgeHints = nil
	o.emailFingerprint = ""
	o.dingtalkWorkspaceFingerprint = ""
	if o.noTools {
		return nil
	}
	if o.audienceID == "" || o.audienceID == "owner" {
		policy, err := assistance.ConfigurationPolicy(o.discoveryConfig)
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		o.contextExecutable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return err
		}
		o.contextPolicy = policy
	}
	if err := resolveEmail(o); err != nil {
		return err
	}
	if err := resolveDingTalkWorkspace(o); err != nil {
		return err
	}
	if o.knowledgePluginDisabled || (!globalKnowledgePlugins(*o) && !knowledgePluginsEnabled(o.pluginNames)) {
		return nil
	}
	selected, err := selectKnowledgeForOptions(*o)
	if err != nil {
		return err
	}
	o.knowledgeSources = selected
	if len(selected) == 0 {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("cannot resolve knowledge MCP executable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return errors.New("cannot resolve knowledge MCP executable")
	}
	mounts := map[string]bool{}
	for _, r := range selected {
		name := knowledge.MountName(r.Source.Name)
		if mounts[name] {
			return errors.New("knowledge instances have colliding MCP names; rename a legacy source")
		}
		mounts[name] = true
		o.mcpServers = append(o.mcpServers, harness.MCPServer{Name: knowledge.MountName(r.Source.Name), Command: executable, Args: []string{"knowledge", "serve", "--config", r.ConfigPath, "--source", r.Source.Name, "--fingerprint", r.Fingerprint}})
		hint := r.Hint()
		for _, p := range o.projects {
			for _, id := range r.Source.Projects {
				if p.ID == id {
					hint.Projects = append(hint.Projects, id)
					break
				}
			}
		}
		// Catalog uses the actual native tool names to avoid ambiguous instances.
		for i, tool := range hint.Tools {
			hint.Tools[i] = fmt.Sprintf("mcp__%s__%s", knowledge.MountName(r.Source.Name), tool)
		}
		o.knowledgeHints = append(o.knowledgeHints, hint)
	}
	return nil
}

func knowledgeValidator(o options) func() error {
	if o.knowledgePluginDisabled || (!globalKnowledgePlugins(o) && !knowledgePluginsEnabled(o.pluginNames)) || len(knowledgePaths(o)) == 0 || o.noTools {
		return nil
	}
	expected := knowledge.Fingerprint(o.knowledgeSources)
	return func() error {
		current, err := selectKnowledgeForOptions(o)
		if err != nil {
			return err
		}
		if knowledge.Fingerprint(current) != expected {
			return errors.New("knowledge access changed after assembly; start a new session")
		}
		return nil
	}
}
