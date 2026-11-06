package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/dream"
	"agent-host/internal/state"
)

func testWorkbenchPython() string {
	if python := os.Getenv("PYTHON"); python != "" {
		return python
	}
	return "python3"
}

func workbenchFixture(t *testing.T) serviceLayout {
	t.Helper()
	layout, _ := newServiceFixture(t)
	source, err := filepath.Abs(filepath.Join("..", "..", "tools", "run-viewer"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChannelViewer(layout.Root, source); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	return layout
}

func TestServiceAppRootMatchesManagedAndCLILayouts(t *testing.T) {
	if got := serviceAppRoot(serviceLayout{Root: "/app/channels/dingtalk"}); got != "/app" {
		t.Fatal("unified managed service root mismatch", got)
	}
	if got := serviceAppRoot(serviceLayout{Root: "/app", Config: "/app/config"}); got != "/app" {
		t.Fatal("CLI workbench root mismatch", got)
	}
}

func TestServiceWorkbenchBrowserAndLifetime(t *testing.T) {
	python, err := exec.LookPath(testWorkbenchPython())
	if err != nil {
		t.Fatal("workbench requires Python 3.9+:", err)
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
			stop, err := startServiceWorkbench(ctx, layout, python, 0, mode != "no-browser", &notice, opener)
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
			for _, path := range []string{"", "memory.html", "executions.html", "stats.html", "stats.js", "stats.css", "readiness.js", "readiness.css", "api/memories", "settings.html", "settings.js", "settings.css", "api/settings", "api/agent-profiles", "agent-profiles.js", "agent-profiles.css", "platform-skills.js", "platform-skills.css"} {
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
	_, err := startServiceWorkbench(t.Context(), layout, "python3", 0, true, io.Discard, func(context.Context, string) error {
		t.Fatal("missing viewer opened a browser")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "rerun make run") {
		t.Fatal("missing observer should have an actionable error", err)
	}
}

func TestCLIWorkbenchFailsWhenObserverExits(t *testing.T) {
	layout := workbenchFixture(t)
	source := filepath.Join(layout.Root, "viewer")
	if err := os.WriteFile(filepath.Join(source, "server.py"), []byte("print('Execution viewer: http://127.0.0.1:8765', flush=True)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := channelDirectoryFixture(t)
	directory := filepath.Join(root, "config")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := runService(ctx, []string{"workbench", "--config-dir", directory, "--viewer-source", source, "--no-browser"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatal("observer failure did not fail the CLI-only command", err)
	}
}

func TestServiceWorkbenchRejectsNonlocalURL(t *testing.T) {
	layout := workbenchFixture(t)
	script := "import os, time\nassert 'ANTHROPIC_AUTH_TOKEN' not in os.environ\nprint('Execution viewer: https://example.com', flush=True)\ntime.sleep(30)\n"
	if err := os.WriteFile(filepath.Join(layout.Root, "viewer", "server.py"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "synthetic-inherited-provider-secret")
	_, err := startServiceWorkbench(t.Context(), layout, "python3", 0, true, io.Discard, func(context.Context, string) error {
		t.Fatal("invalid URL opened a browser")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback URL") {
		t.Fatal("untrusted startup URL should not open a browser", err)
	}
}

func TestServiceWorkbenchFixedPortAcrossRestarts(t *testing.T) {
	python, err := exec.LookPath(testWorkbenchPython())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	layout := workbenchFixture(t)
	peer := workbenchFixture(t)
	sources := []configuredChannel{{"dingtalk", layout.Root}, {"feishu", peer.Root}}
	var prior string
	for range 2 {
		var notice bytes.Buffer
		stop, err := startServiceWorkbenchSources(t.Context(), layout, python, port, false, &notice, nil, sources, "")
		if err != nil {
			t.Fatal(err)
		}
		address := strings.Fields(notice.String())[2]
		response, err := http.Get(address + "api/records")
		if err != nil {
			stop()
			t.Fatal(err)
		}
		var index struct {
			Channels []string `json:"channels"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&index)
		response.Body.Close()
		stop()
		if decodeErr != nil || len(index.Channels) != 2 {
			t.Fatal("shared workbench omitted a channel", index, decodeErr)
		}
		if response.StatusCode != http.StatusOK || prior != "" && address != prior {
			t.Fatal("fixed workbench URL changed or failed", prior, address, response.StatusCode)
		}
		prior = address
	}
}

func TestServiceWorkbenchIncludesCLIStateSource(t *testing.T) {
	python, err := exec.LookPath(testWorkbenchPython())
	if err != nil {
		t.Fatal("workbench requires Python 3.9+:", err)
	}
	layout := workbenchFixture(t)
	sources := []configuredChannel{{"dingtalk", layout.Root}}
	cliState := filepath.Join(t.TempDir(), "cli-state")
	if _, err := state.Open(cliState, false); err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	stop, err := startServiceWorkbenchSources(t.Context(), layout, python, 0, false, &notice, nil, sources, cliState)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	address := strings.Fields(notice.String())[2]
	response, err := http.Get(address + "api/records")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var index struct {
		Channels []string `json:"channels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&index); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(index.Channels, ","), "cli") {
		t.Fatal("workbench omitted the CLI chat source", index)
	}
}

func TestServiceWorkbenchReadsGoPublishedDreamCurrent(t *testing.T) {
	python, err := exec.LookPath(testWorkbenchPython())
	if err != nil {
		t.Fatal("workbench requires Python 3.9+:", err)
	}
	layout := workbenchFixture(t)
	hotRoot := filepath.Join(layout.Root, "dream", "hot")
	if err = os.MkdirAll(hotRoot, 0700); err != nil {
		t.Fatal(err)
	}
	partition := state.DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}
	bundle, err := dream.ExportSharedHOT(partition, "owner", "owner-share", 2, strings.Repeat("a", 64), strings.Repeat("b", 64), []dream.SharedHOTSource{{
		Metadata: dream.HOTProjectionSource{ID: "cli", StateHome: layout.State, Partition: partition, LocalOwner: true, Kinds: []string{"preference"}},
		Memories: []state.Memory{{ID: "memory", SenderID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope", Kind: "preference", Text: "user", Status: "active", Revision: 2, SourceType: "dream_reviewed"}},
	}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dream.PublishCurrent(hotRoot, dream.HOTSource{StateHome: layout.State, ConfigID: "config-id"}, bundle); err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	stop, err := startServiceWorkbench(t.Context(), layout, python, 0, false, &notice, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	address := strings.Fields(notice.String())[2]
	response, err := http.Get(address + "api/dream/partitions")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Partitions []struct {
			ID string `json:"id"`
		} `json:"partitions"`
	}
	err = json.NewDecoder(response.Body).Decode(&index)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(index.Partitions) != 1 {
		t.Fatal("Go Dream publication was not listed", index, response.StatusCode, err)
	}
	response, err = http.Get(address + "api/dream/partition?id=" + index.Partitions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Current struct {
			Digest     string            `json:"digest"`
			Documents  map[string]string `json:"documents"`
			Projection struct {
				CanonicalPersonID string `json:"canonical_person_id"`
				Generation        int    `json:"generation"`
			} `json:"projection"`
		} `json:"current"`
	}
	err = json.NewDecoder(response.Body).Decode(&detail)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || detail.Status != "current" || detail.Current.Digest != bundle.Digest || detail.Current.Documents["USER.md"] != bundle.Documents["USER.md"] || detail.Current.Projection.CanonicalPersonID != "owner" || detail.Current.Projection.Generation != 2 {
		t.Fatal("Python viewer did not verify Go Dream bytes", detail, response.StatusCode, err)
	}
}

func TestServiceWorkbenchRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "-1", "65536"} {
		err := runService(t.Context(), []string{"run", "--viewer-port", port}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "viewer-port") {
			t.Fatal("invalid port accepted", port, err)
		}
	}
}
