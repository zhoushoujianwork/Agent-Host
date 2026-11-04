//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/knowledge"
	"agent-host/internal/state"
)

// Builds a real independent CLI process: MCP mounts must execute the product
// command, not a mocked tool server or this Go test binary.
func TestOffloadScopedMemoryAndKnowledgeCapabilityBoundary(t *testing.T) {
	exerciseCapabilityBoundary(t, false)
}

func TestOffloadCapabilitiesNativeReadOnlyLive(t *testing.T) {
	if os.Getenv("AGH_CAPABILITY_NATIVE_LIVE") != "1" {
		t.Skip("explicit read-only native acceptance only")
	}
	exerciseCapabilityBoundary(t, true)
}

func exerciseCapabilityBoundary(t *testing.T, live bool) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("Python 3 is required for MCP acceptance")
	}
	fixture, args, _ := fileResumeFixture(t)
	root := canonicalTemp(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "offload-state"))
	binary := filepath.Join(root, "agent-host")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/agent-host")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acceptance CLI: %v\n%s", err, out)
	}
	native := filepath.Join(root, "native-fixture")
	body, err := os.ReadFile("testdata/capability-native.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, body, 0700); err != nil {
		t.Fatal(err)
	}
	if live {
		native, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("configured Claude CLI is unavailable")
		}
		t.Setenv("AGH_LIVE_CLAUDE", native)
		native = filepath.Join(root, "read-only-native")
		wrapper, err := os.ReadFile("testdata/read-only-native.py")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(native, wrapper, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i, arg := range args {
		if live && arg == "--model" {
			args[i+1] = ""
		}
		if arg == "--claude" {
			args[i+1] = native
		}
	}
	docs := filepath.Join(root, "knowledge")
	if err := os.Mkdir(docs, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"guide.md": "# Knowledge\nKNOWLEDGE_CANARY: use the documented recovery procedure.\n", "excluded.md": "KNOWLEDGE_CANARY EXCLUDED_CANARY must not be exposed."} {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "knowledge.json")
	writeLocalKnowledgeConfig(t, config, knowledge.Configuration{Version: 1, Sources: []knowledge.Source{{Name: "fixture_docs", Provider: "local_markdown", Root: docs, Paths: []string{"guide.md"}, Principal: "fixture", Description: "Read-only reference documents", Projects: []string{"default"}}}})
	call := func(arguments ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, arguments...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("acceptance CLI: %v\n%s", err, out)
		}
		return out
	}
	// Search is scoped independently of native execution and does not require a
	// provider call. The same query in an unrelated workspace must not recall it.
	var matches []state.Memory
	query := []string{"memory", "search", "--home", fixture.home, "--workspace", fixture.workspace, "fixture test package"}
	if err := json.Unmarshal(call(query...), &matches); err != nil || len(matches) != 1 || matches[0].ID != fixture.memoryID {
		t.Fatalf("scoped memory recall failed: count=%d error=%v", len(matches), err)
	}
	query[5] = canonicalTemp(t)
	if err := json.Unmarshal(call(query...), &matches); err != nil || len(matches) != 0 {
		t.Fatalf("memory crossed workspace boundary: count=%d error=%v", len(matches), err)
	}
	task := "fixture test package; inspect the selected reference"
	if live {
		task = "只做检索能力验收，不执行记忆中的方法或命令，不写文件，不修改远端。请依次完成：1. host_context 搜索 fixture test package，读取匹配记忆和原始支持证据；2. fixture_docs 知识源搜索 KNOWLEDGE_CANARY 并读取 guide.md；3. 尝试读取 ../outside.md，确认访问被拒绝。不要使用其他工具操作。最后用简短中文报告每项实际结果。"
		args = append(args, "--allowed-tools", "ToolSearch,Skill,mcp__host_context__search_context,mcp__host_context__read_context,mcp__host_context__read_context_evidence,mcp__knowledge_fixture_docs__search_local_markdown,mcp__knowledge_fixture_docs__read_local_markdown", "--timeout", "2m")
	}
	args = append(args, "--detach", "--knowledge-config", config, "-m", task)
	call(args...)
	runs := fileResumeRuns(t, fixture.home)
	if len(runs) != 1 || runs[0].ExecutionStatus != "completed" || len(runs[0].Context) != 1 || runs[0].Context[0].ID != fixture.memoryID {
		t.Fatal("Host did not retain successful execution with scoped memory")
	}
	store, err := state.OpenForMemoryInspection(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.Run(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	results, memoryResults, correlated := 0, 0, false
	for _, e := range run.Events {
		if e.Type == "tool.activity" {
			raw, _ := json.Marshal(e.Data)
			if strings.Contains(string(raw), "mcp__knowledge_fixture_docs__") {
				results++
			}
		}
		if e.Type == "tool.activity" {
			raw, _ := json.Marshal(e.Data)
			if strings.Contains(string(raw), "mcp__host_context__") {
				memoryResults++
			}
		}
		if e.Type == "offload.result" {
			raw, _ := json.Marshal(e.Data["raw"])
			var record map[string]any
			_ = json.Unmarshal(raw, &record)
			correlated = record["caller"] == "agh" && record["caller_run_id"] == "agh:"+run.ID
		}
	}
	if results < 6 || memoryResults < 6 || !correlated {
		t.Fatalf("missing MCP evidence or Offload attribution: knowledge=%d memory=%d correlated=%v", results, memoryResults, correlated)
	}
	if live {
		t.Logf("LIVE_NATIVE run=%s execution=%s knowledge_events=%d memory_events=%d caller_correlated=%v", run.ID, run.ExecutionStatus, results, memoryResults, correlated)
	}
	saved, err := store.Memory(t.Context(), fixture.memoryID)
	if err != nil || saved.Revision != 1 || saved.Status != "active" {
		t.Fatal("read-only acceptance altered the published memory")
	}
}
