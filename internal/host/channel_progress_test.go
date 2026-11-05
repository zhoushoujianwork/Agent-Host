package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/channelprogress"
	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

func progressServer(t *testing.T, req harness.Request) *harness.MCPServer {
	t.Helper()
	for i := range req.MCPServers {
		if req.MCPServers[i].Name == "channel_progress" {
			return &req.MCPServers[i]
		}
	}
	return nil
}
func TestChannelProgressMountRebindsRunAndPreservesContinuation(t *testing.T) {
	h, runner := historyHost(t)
	h.Config.ProgressHome = h.Config.HistoryHome
	h.Config.ProgressExecutable = "/fixture/agent-host"
	h.Config.HistoryHome = ""
	h.Config.ManagedCatalog = true
	h.Config.MemorySource = "progress-fixture"
	h.Config.PersonalHOTPersonID = "owner"
	h.Config.PersonalHOTDocuments = func(context.Context) ([]harness.PresetDocument, string, error) {
		return []harness.PresetDocument{{Name: "MEMORY.md", Content: []byte("reviewed personal HOT")}}, "progress-hot-v1", nil
	}
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.Config.WorkspaceCache = cache
	session := newSession(t, h)
	for _, id := range []string{"first", "second"} {
		m := historyChannelMessage(t, h, session, id, "work "+id, "tenant")
		r, err := h.RunChannel(t.Context(), session, m.Text, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = h.Store.FinishChannelMessage(t.Context(), m.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		if _, err = h.Store.ChannelProgressBinding(t.Context(), m.ID, r.ID); err == nil {
			t.Fatal("finished run retains progress")
		}
	}
	for _, req := range runner.requests {
		server := progressServer(t, req)
		if server == nil {
			t.Fatal("missing progress mount")
		}
		if len(req.PresetDocuments) != 1 || req.PresetDocuments[0].Name != "MEMORY.md" || string(req.PresetDocuments[0].Content) != "reviewed personal HOT" {
			t.Fatalf("progress request lost personal HOT: %+v", req.PresetDocuments)
		}
		raw, err := os.ReadFile(filepath.Join(req.SkillPluginDir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest harness.SkillManifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		expected := managedskills.PluginUsage("channel_progress", channelprogress.ToolContract(), channelprogress.Instructions, server.SessionKey)
		if len(manifest.Sources) != 1 || manifest.Sources[0].SkillID != expected.ID || manifest.Sources[0].ContentDigest != expected.Digest || manifest.Sources[0].ToolContractDigest != expected.ToolContractDigest {
			t.Fatalf("wrong progress manifest: %+v", manifest)
		}
		skillRoot := filepath.Join(req.SkillPluginDir, "skills", manifest.Sources[0].Name)
		guide, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
		if err != nil || !strings.Contains(string(guide), channelprogress.Instructions) {
			t.Fatal("missing progress guidance", err)
		}
		contract, err := os.ReadFile(filepath.Join(skillRoot, "references", "contract.json"))
		if err != nil {
			t.Fatal(err)
		}
		var actual any
		if json.Unmarshal(contract, &actual) != nil || managedskills.ContractDigest(actual) != expected.ToolContractDigest {
			t.Fatal("wrong progress tool contract")
		}
	}
	if runner.requests[0].SkillPluginDigest == "" || runner.requests[0].SkillPluginDigest != runner.requests[1].SkillPluginDigest {
		t.Fatal("per-turn progress arguments changed managed Skill identity")
	}
	a, b := progressServer(t, runner.requests[0]), progressServer(t, runner.requests[1])
	if a == nil || b == nil || a.SessionKey == "" || a.SessionKey != b.SessionKey || strings.Join(a.Args, " ") == strings.Join(b.Args, " ") {
		t.Fatal("progress mount did not rebind", a, b)
	}
	if runner.requests[0].Resume || !runner.requests[1].Resume || runner.requests[0].NativeSessionID != runner.requests[1].NativeSessionID {
		t.Fatal("progress reset native conversation")
	}
}
func TestChannelProgressGating(t *testing.T) {
	for _, mode := range []string{"cli", "history-only", "tool-free", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			h, runner := historyHost(t)
			h.Config.ProgressHome = h.Config.HistoryHome
			h.Config.ProgressExecutable = "/fixture/agent-host"
			h.Config.HistoryHome = ""
			session := newSession(t, h)
			var run state.Run
			var err error
			switch mode {
			case "cli":
				run, err = h.Run(t.Context(), session, "work")
			case "history-only":
				m := historyChannelMessage(t, h, session, "history", "work", "tenant")
				h.Config.HistoryMessageID = m.ID
				run, err = h.Run(t.Context(), session, "work")
			default:
				m := historyChannelMessage(t, h, session, "channel", "work", "tenant")
				h.Config.ToolsDisabled = mode == "tool-free"
				h.Config.RestrictedReadOnly = mode == "restricted"
				run, err = h.RunChannel(t.Context(), session, m.Text, m.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.requests) != 1 || progressServer(t, runner.requests[0]) != nil {
				t.Fatal("progress mounted outside allowed channel execution", run)
			}
		})
	}
}
