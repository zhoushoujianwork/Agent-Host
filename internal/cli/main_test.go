package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/channel/feishumeeting"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == feishumeeting.EntryWorkerFlag {
		os.Exit(feishumeeting.EntryWorker(os.Args[2:]))
	}
	// Re-executed fixtures inherit this private configuration; normal test
	// startup never resolves the developer's real Offload credential command.
	if os.Getenv("AGENT_HOST_TEST_OFFLOAD_CONFIG") == "" {
		root, err := os.MkdirTemp("", "agh-offload-test-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(root)
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		dir := filepath.Join(root, "offload")
		if err = os.MkdirAll(dir, 0700); err != nil {
			panic(err)
		}
		raw, _ := json.Marshal(map[string]any{"default": "fixture", "profiles": map[string]any{"fixture": map[string]any{"backend": "claude", "model": "sonnet", "base_url": server.URL, "token": "synthetic-fixture-token"}}})
		if err = os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600); err != nil {
			panic(err)
		}
		os.Setenv("XDG_CONFIG_HOME", root)
		os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
		os.Setenv("OFFLOAD_MAX_WORKERS", "-1")
		os.Setenv("AGENT_HOST_TEST_OFFLOAD_CONFIG", dir)
		code := m.Run()
		server.Close()
		os.RemoveAll(root)
		os.Exit(code)
	}
	os.Exit(m.Run())
}
