package nativepolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/harness"
)

func TestHostSessionEnvironmentIsolationReuseAndMissingResume(t *testing.T) {
	profile := t.TempDir()
	profile, _ = filepath.EvalSymlinks(profile)
	if err := os.Chmod(profile, 0700); err != nil {
		t.Fatal(err)
	}
	first, cleanup, err := executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "session-a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	second, _, err := executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "session-b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Root == second.Root || first.Workspace == second.Workspace {
		t.Fatal("different Host conversations shared a native environment")
	}
	resumed, _, err := executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "session-a", Resume: true})
	if err != nil || resumed.Root != first.Root {
		t.Fatalf("same Host conversation did not reuse its environment: %+v %v", resumed, err)
	}
	if err = os.RemoveAll(first.Root); err != nil {
		t.Fatal(err)
	}
	if _, _, err = executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "session-a", Resume: true}); err == nil {
		t.Fatal("resume silently replaced a missing environment")
	}
	if _, _, err = executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "../escape"}); err == nil {
		t.Fatal("unsafe Host session identity was accepted")
	}
}

func TestExtractionEnvironmentIsFreshAndDisposable(t *testing.T) {
	env, cleanup, err := executionEnvironment(harness.Request{Extraction: true, HostSessionID: "ignored", NativeProfile: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(env.Workspace) != "workspace" {
		t.Fatalf("unexpected extraction workspace: %+v", env)
	}
	cleanup()
	if _, err = os.Stat(env.Root); !os.IsNotExist(err) {
		t.Fatal("disposable extraction environment was retained")
	}
}

func TestResumeRequiresHostIdentityAndExtractionStaysFresh(t *testing.T) {
	if _, _, err := executionEnvironment(harness.Request{Resume: true}); err == nil {
		t.Fatal("resume without the original Host identity created a replacement environment")
	}
	if _, _, err := executionEnvironment(harness.Request{Resume: true, Extraction: true}); err == nil {
		t.Fatal("extraction resumed native state")
	}
}

func TestCodexRPCPreservesRawMCPAndObservedModel(t *testing.T) {
	var observations []harness.Observation
	d := codexRPCDecoder{emit: func(o harness.Observation) error { observations = append(observations, o); return nil }}
	frames := []string{
		`{"id":4,"result":{"model":"gpt-start","thread":{"id":"thread-1"}}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","item":{"id":"mcp-1","type":"mcpToolCall","server":"ops","tool":"lookup","arguments":{"id":7}}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","item":{"id":"mcp-1","type":"mcpToolCall","server":"ops","tool":"lookup","status":"completed","result":{"ok":true}}}}`,
		`{"method":"model/rerouted","params":{"threadId":"thread-1","toModel":"gpt-observed"}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`,
	}
	for _, frame := range frames {
		if err := d.consume([]byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	if !d.terminal || len(observations) != len(frames) || observations[0].NativeSessionID != "thread-1" || observations[3].Model != "gpt-observed" {
		t.Fatalf("native RPC evidence incomplete: %+v", observations)
	}
	if observations[1].Tool == nil || observations[1].Tool.Name != "mcp__ops__lookup" || observations[2].Tool.Stage != "result" || string(observations[2].Raw) != frames[2] {
		t.Fatalf("MCP evidence was changed or lost: %+v", observations)
	}
}

func TestCodexRPCRejectsExplicitIncompatibleVersion(t *testing.T) {
	if err := (&codexRPCDecoder{}).consume([]byte(`{"jsonrpc":"1.0","method":"turn/started","params":{}}`)); err == nil {
		t.Fatal("explicit incompatible JSON-RPC version was accepted")
	}
}
