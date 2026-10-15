package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"agent-host/internal/approval"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type approvalHarnessFunc func(context.Context, harness.Request) (harness.Result, error)

func (f approvalHarnessFunc) Run(ctx context.Context, r harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	return f(ctx, r)
}

func TestRestrictedHostOnlyMountsProposalAndDeliversAfterNativeCompletion(t *testing.T) {
	for _, scenario := range []string{"accepted", "delivery failed", "native failed"} {
		t.Run(scenario, func(t *testing.T) {
			h, _ := testHost(t)
			h.Config.RestrictedReadOnly = true
			h.Config.PermissionMode = "dontAsk"
			h.Config.ApprovalExecutable = "/fixture/agent-host"
			h.Config.MCPServers = []harness.MCPServer{{Name: "unreviewed_write_server", Command: "must-not-run"}}
			sends := 0
			h.Config.RequestApproval = func(_ context.Context, r state.Run, p approval.Proposal) error {
				sends++
				if r.ID == "" || p.Target != "/example/project" {
					t.Fatal("proposal lost run binding")
				}
				if scenario == "delivery failed" {
					return errors.New("notification unavailable")
				}
				return nil
			}
			var directory string
			h.Runner = approvalHarnessFunc(func(ctx context.Context, req harness.Request) (harness.Result, error) {
				if !req.RestrictedReadOnly || req.PermissionMode != "dontAsk" || len(req.AdditionalDirectories) != 0 || req.SkillPluginDir != "" || len(req.MCPServers) != 1 || req.MCPServers[0].Name != "approval" {
					t.Fatal("restricted capability assembly expanded", req)
				}
				directory = req.MCPServers[0].Args[2]
				p := approval.Proposal{Operation: "delete project", Target: "/example/project", Impact: "Removes the local project files.", Shareable: true}
				body, _ := json.Marshal(p)
				input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"fixture"}}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_approval","arguments":` + string(body) + `}}` + "\n"
				var out bytes.Buffer
				if err := approval.Serve(ctx, directory, strings.NewReader(input), &out); err != nil {
					t.Fatal(err)
				}
				if sends != 0 || !strings.Contains(out.String(), `owner_notified`) {
					t.Fatal("approval sent before native completion")
				}
				if scenario == "native failed" {
					return harness.Result{Status: "failed", Error: "native failed"}, errors.New("native failed")
				}
				return harness.Result{Status: "completed", Text: "Proposal recorded.", Model: "fixture"}, nil
			})
			r, err := h.Run(t.Context(), newSession(t, h), "Please delete the project")
			if _, statErr := os.Stat(directory); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("temporary proposal directory retained", statErr)
			}
			if scenario == "native failed" {
				if sends != 0 || err == nil {
					t.Fatal("failed native turn notified owner")
				}
				return
			}
			if err != nil || sends != 1 {
				t.Fatal("proposal was not delivered once", r, err, sends)
			}
			if scenario == "accepted" && !strings.Contains(r.Output, "owner was mentioned") {
				t.Fatal("confirmed notification missing", r.Output)
			}
			if scenario == "delivery failed" && (!strings.Contains(r.Output, "notification unavailable") || strings.Contains(r.Output, "owner was mentioned")) {
				t.Fatal("failed notification hidden", r.Output)
			}
		})
	}
}
