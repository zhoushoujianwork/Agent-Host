//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/mcpstdio"
)

func managedNativeFixture(t *testing.T) assembly.SkillPackage {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := managedskills.Entry{ID: "regional-delivery", Name: "regional-delivery", Kind: "authored", Revision: 1, Owner: "fixture", CanonicalRef: "fixture:reviewed-runbook", Applicability: "Read-only verification of US or China SFTP tickets", Description: "SFTP regional delivery verification", Provenance: []string{"test reviewed authored instructions"}, Files: map[string]string{
		"SKILL.md":            "---\nname: regional-delivery\ndescription: SFTP regional delivery verification\n---\nBefore the ticket tool call load the configured plugin usage Skill and read its complete contract. Read references/us.md for a US customer and references/china.md for a China customer BEFORE the ticket tool call. Reuse the exact instance_id from the user. Verify the latest comment and current task from read_ticket; do not enumerate other tickets. No writes. Report region marker, latest comment and current task.\n",
		"references/us.md":    "United States only: profile tencent-us, data root /fixture/us/absci, verification marker US_REFERENCE_COMPLETE. Never use China settings.\n",
		"references/china.md": "China only: profile tencent-cn, data root /fixture/china/caict, verification marker CHINA_REFERENCE_COMPLETE. Never use US settings.\n",
	}}
	e.Digest = e.ContentDigest()
	usage := managedskills.PluginUsage("fixture", managedFixtureTools(), "Read only the exact selected ticket. Read the full regional procedure and reference before calling read_ticket. No searching, writes, approval or delivery.", "fixture-contract-v1")
	p, err := assembly.ManagedSkillPackage(root, "native-acceptance", []managedskills.Entry{e, usage}, nil, assembly.SkillIdentity{SourceIdentity: "isolated-acceptance", AgentID: "default", AudienceID: "owner", Scopes: []string{"fixture"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func managedFixtureTools() []mcpstdio.Tool {
	return []mcpstdio.Tool{{Name: "read_ticket", Description: "Read an exact fixture ticket with latest comments and current tasks. Read-only; no enumeration.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"instance_id": map[string]any{"type": "string"}}, "required": []string{"instance_id"}, "additionalProperties": false}, Annotations: map[string]bool{"readOnlyHint": true}}}
}

func serveManagedSkillFixture() error {
	return mcpstdio.Serve(context.Background(), "fixture", managedFixtureTools(), func(_ context.Context, name string, raw json.RawMessage) (any, error) {
		var args struct {
			InstanceID string `json:"instance_id"`
		}
		if json.Unmarshal(raw, &args) != nil || name != "read_ticket" {
			return nil, errors.New("invalid fixture call")
		}
		switch args.InstanceID {
		case "US-FTP-001", "CN-FTP-001":
			return map[string]any{"instance_id": args.InstanceID, "latest_comment": "Only the current approved /32 on TCP 22; read-only acceptance", "current_task": "owner verification pending"}, nil
		default:
			return nil, errors.New("foreign ticket forbidden")
		}
	}, os.Stdin, os.Stdout)
}

func TestManagedNativePackageValidatesFullReferences(t *testing.T) {
	p := managedNativeFixture(t)
	if _, err := validateSkillPlugin(p.Path, p.Digest); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Manifest.Files {
		if strings.HasSuffix(f.Path, "references/us.md") {
			if err := os.WriteFile(filepath.Join(p.Path, f.Path), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := validateSkillPlugin(p.Path, p.Digest); err == nil {
		t.Fatal("modified regional reference accepted")
	}
}

// This uses a real native model/tool loop against a disposable read-only MCP.
// The user's prompt names neither a Skill nor a native load entry.
func TestManagedNativeProactiveLoading(t *testing.T) {
	providerProfile := os.Getenv("AGENT_HOST_MANAGED_SKILL_SMOKE_PROFILE")
	if providerProfile == "" {
		t.Skip("set an approved provider profile to run native acceptance")
	}
	provider, err := restrictedProviderEnvironment(harness.Request{RestrictedReadOnly: true, NativeProfile: providerProfile})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, region := range []string{"US", "China"} {
		t.Run(region, func(t *testing.T) {
			p := managedNativeFixture(t)
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			profile := t.TempDir()
			settings, _ := json.Marshal(map[string]any{"env": provider, "disableAllHooks": true, "permissions": map[string]any{"deny": []string{"Bash", "Edit", "Write", "NotebookEdit", "Agent", "Task"}}})
			if err = os.WriteFile(filepath.Join(profile, "settings.json"), settings, 0600); err != nil {
				t.Fatal(err)
			}
			id, marker, ref := "US-FTP-001", "US_REFERENCE_COMPLETE", "references/us.md"
			if region == "China" {
				id, marker, ref = "CN-FTP-001", "CHINA_REFERENCE_COMPLETE", "references/china.md"
			}
			req := harness.Request{Workspace: workspace, NativeProfile: profile, PermissionMode: "default", AllowedTools: []string{"Skill", "Read", "mcp__fixture__read_ticket"}, NativeSessionID: testSession, SkillPluginDir: p.Path, SkillPluginDigest: p.Digest, HostInstructions: assembly.NativeSkillContext(p.Manifest, nil) + "This isolated acceptance authorizes only Skill, Read and the read-only fixture tool. Do not use any other tool.", Prompt: fmt.Sprintf("Read-only check for the %s SFTP customer. The original card instance_id is %s; a quoted robot reply refers to this same ID. Report the latest comment, current task and appropriate environment marker. Do not approve, change or send anything.", region, id), MCPServers: []harness.MCPServer{{Name: "fixture", Command: executable, Args: []string{"managed-skill-fixture"}}}}
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
			defer cancel()
			loaded := map[string]bool{}
			calls := map[string]harness.ToolObservation{}
			readRegion, readContract, called := false, false, false
			result, err := New("").Run(ctx, req, func(o harness.Observation) error {
				if o.Tool == nil {
					return nil
				}
				tool := o.Tool
				if tool.Stage == "call" {
					calls[tool.ID] = *tool
					t.Logf("native call %s %s", tool.Name, tool.Input)
				}
				call := *tool
				if previous, ok := calls[tool.ID]; ok && len(call.Input) == 0 {
					call.Input = previous.Input
					call.Name = previous.Name
				}
				var input map[string]any
				_ = json.Unmarshal(call.Input, &input)
				if tool.Stage == "result" && tool.IsError {
					t.Errorf("native tool failed: %s", call.Name)
				}
				if tool.Stage == "result" && call.Name == "Skill" {
					if name, ok := input["skill"].(string); ok {
						loaded[name] = true
					}
				}
				if tool.Stage == "result" && call.Name == "Read" {
					path, _ := input["file_path"].(string)
					if strings.HasSuffix(path, ref) {
						readRegion = true
					}
					if strings.HasSuffix(path, "references/contract.json") {
						readContract = true
					}
				}
				if tool.Stage == "call" && call.Name == "mcp__fixture__read_ticket" {
					called = true
					if input["instance_id"] != id || len(loaded) != 2 || !readRegion || !readContract {
						t.Errorf("tool called before full procedure/plugin guidance: loaded=%v region=%t contract=%t input=%v", loaded, readRegion, readContract, input)
					}
				}
				return nil
			})
			if err != nil || result.Status != "completed" || !called || !strings.Contains(result.Text, marker) || !strings.Contains(result.Text, "owner verification pending") {
				t.Fatalf("native acceptance failed: status=%s called=%t output=%s err=%v", result.Status, called, result.Text, err)
			}
		})
	}
}
