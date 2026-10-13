package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func workbenchFixture(t *testing.T) serviceLayout {
	t.Helper()
	layout, _ := newServiceFixture(t)
	for _, name := range []string{"logs", "viewer"} {
		if err := os.Mkdir(filepath.Join(layout.Root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"server.py", "memory_view.py", "index.html", "app.js", "style.css", "memory.html", "memory.js", "memory.css"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "run-viewer", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(layout.Root, "viewer", name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	return layout
}

func TestServiceWorkbenchBrowserAndLifetime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("workbench requires Python 3.10+:", err)
	}
	for _, mode := range []string{"default", "no-browser", "open-failed"} {
		t.Run(mode, func(t *testing.T) {
			layout := workbenchFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var notice bytes.Buffer
			opened := ""
			opener := func(_ context.Context, address string) error {
				opened = address
				if mode == "open-failed" {
					return errors.New("browser unavailable")
				}
				return nil
			}
			stop, err := startServiceWorkbench(ctx, layout, python, mode != "no-browser", &notice, opener)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			address := strings.Fields(notice.String())[2]
			if mode == "no-browser" && opened != "" || mode != "no-browser" && opened != address {
				t.Fatal("default browser/opt-out did not follow startup mode", opened, notice.String())
			}
			if mode == "open-failed" && !strings.Contains(notice.String(), "could not be opened") {
				t.Fatal("browser failure was hidden")
			}
			client := &http.Client{Timeout: 2 * time.Second}
			for _, path := range []string{"", "memory.html", "api/memories"} {
				response, err := client.Get(address + path)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 {
					t.Fatal("installed workbench did not serve", path, response.StatusCode, err)
				}
				if path == "api/memories" {
					var data map[string]any
					if json.Unmarshal(raw, &data) != nil || data["database_present"] != true || data["database_total"] != float64(0) || data["observed_total"] != float64(0) {
						t.Fatal("workbench imported unrelated development or archived data", string(raw))
					}
				}
			}
			cancel()
			stop()
			if response, err := client.Get(address); err == nil {
				response.Body.Close()
				t.Fatal("workbench remained running after service cancellation")
			}
		})
	}
}

func TestServiceWorkbenchMissingInstallation(t *testing.T) {
	layout, _ := newServiceFixture(t)
	_, err := startServiceWorkbench(t.Context(), layout, "python3", true, io.Discard, func(context.Context, string) error {
		t.Fatal("missing viewer opened a browser")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "rerun make run") {
		t.Fatal("missing observer should have an actionable error", err)
	}
}

func TestServiceWorkbenchRejectsNonlocalURL(t *testing.T) {
	layout := workbenchFixture(t)
	script := "import os, time\nassert 'ANTHROPIC_AUTH_TOKEN' not in os.environ\nprint('Execution viewer: https://example.com', flush=True)\ntime.sleep(30)\n"
	if err := os.WriteFile(filepath.Join(layout.Root, "viewer", "server.py"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "synthetic-inherited-provider-secret")
	_, err := startServiceWorkbench(t.Context(), layout, "python3", true, io.Discard, func(context.Context, string) error {
		t.Fatal("invalid URL opened a browser")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback URL") {
		t.Fatal("untrusted startup URL should not open a browser", err)
	}
}
