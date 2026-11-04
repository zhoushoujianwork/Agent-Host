package nativepolicy

import (
	"agent-host/internal/offloadexec"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSDKAdapter(t *testing.T, binary string) *Adapter {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	path := filepath.Join(root, "native.json")
	body, _ := json.Marshal(map[string]any{"profiles": map[string]any{"fixture": map[string]any{"backend": "claude", "model": "fixture", "base_url": server.URL, "token": "synthetic-fixture-token"}}})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	lib, err := offloadexec.NewLibrary(path, "fixture", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return NewWithLibrary(binary, lib)
}

func TestSDKCallerAttributionForBusinessAndInternalCalls(t *testing.T) {
	a, req := fixtureAdapter(t, "normal")
	for _, callerID := range []string{"agh:business-fixture", ""} {
		req.CallerRunID = callerID
		result, err := a.Run(context.Background(), req, nil)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if result.Evidence == nil || json.Unmarshal(result.Evidence.Raw, &raw) != nil || raw["caller"] != "agh" {
			t.Fatal("caller missing from package history")
		}
		actual, _ := raw["caller_run_id"].(string)
		if callerID != "" && actual != callerID || callerID == "" && !strings.HasPrefix(actual, "agh:internal:") {
			t.Fatal("caller correlation missing")
		}
	}
}
