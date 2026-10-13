package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == WorkerFlag {
		code, err := Worker(os.Stdin)
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func configuration(t *testing.T) (string, Config, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Profile: "corp:owner", StateDirectory: filepath.Join(root, "state"), Services: []string{"contact", "calendar", "todo"}}
	raw, _ := json.Marshal(c)
	path := filepath.Join(root, "workspace.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, c, fp
}

func TestOfficialEmbeddedDiscoveryAndIsolation(t *testing.T) {
	path, c, fp := configuration(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CLIENT_ID", "must-not-use-ambient-client")
	run := Process(executable)
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}
	value, err := s.Call(context.Background(), "list_commands", json.RawMessage(`{"service":"todo","query":"create","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	list := value.(map[string]any)
	if list["total"].(int) == 0 {
		t.Fatalf("official command catalog was empty: %#v", list)
	}
	value, err = s.Call(context.Background(), "describe_command", json.RawMessage(`{"command":"todo task create"}`))
	if err != nil {
		t.Fatal(err)
	}
	schema := value.(map[string]any)
	if schema["effect"] != "write" || schema["confirmation"] != "not_required" {
		t.Fatalf("official contract = %#v", schema)
	}
	if schema["parameters"].(map[string]any)["executors"].(map[string]any)["required"] != true {
		t.Fatal("lost official required parameter")
	}
	// Selected-source collection uses this official read shortcut directly; it
	// remains outside the business MCP's configured service surface.
	source, err := run(context.Background(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", "chat +chat-messages", "--compact", "--format", "json"}})
	if err != nil {
		t.Fatal("embedded selected-history contract unavailable", err)
	}
	var sourceSchema map[string]any
	if json.Unmarshal(source, &sourceSchema) != nil || sourceSchema["effect"] != "read" || sourceSchema["availability"] != "available" {
		t.Fatal("selected-history contract changed")
	}
	if os.Getenv("DWS_CLIENT_ID") != "must-not-use-ambient-client" {
		t.Fatal("worker changed parent environment")
	}
	if editionConfig := filepath.Join(c.StateDirectory, "keychain"); func() bool {
		info, e := os.Stat(editionConfig)
		return e == nil && info.IsDir() && info.Mode().Perm() == 0700
	}() == false {
		t.Fatal("missing isolated official credential directory")
	}
	// Profile discovery uses only the isolated empty registry, not any owner
	// login from the user's ambient ~/.dws installation.
	if err := verifyIdentity(context.Background(), run, Request{Config: path, Fingerprint: fp}, c); err == nil {
		t.Fatal("ambient identity unexpectedly granted access")
	}
}

func TestConfigScopeAndChange(t *testing.T) {
	path, c, fp := configuration(t)
	if _, _, _, err := Binding(path, "other:owner"); err == nil {
		t.Fatal("cross-bot identity allowed")
	}
	if _, err := commandPath(c, "chat message send"); err == nil {
		t.Fatal("unconfigured personal chat allowed")
	}
	if _, err := commandPath(c, "todo task create --profile other"); err == nil {
		t.Fatal("flag in path allowed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("public config allowed")
	}
	os.Chmod(path, 0600)
	c.Services = []string{"contact"}
	raw, _ := json.Marshal(c)
	os.WriteFile(path, raw, 0600)
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile}
	if _, err := s.Call(context.Background(), "list_services", json.RawMessage(`{}`)); err == nil {
		t.Fatal("changed binding accepted")
	}
	missing := filepath.Join(filepath.Dir(path), "missing.json")
	_, pin, configured, err := Binding(missing, c.Profile)
	if err != nil || configured {
		t.Fatal("missing config binding", err)
	}
	s = Service{Path: missing, Fingerprint: pin, ExpectedProfile: c.Profile}
	if _, err := s.Call(context.Background(), "list_services", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(context.Background(), "list_commands", json.RawMessage(`{"service":"todo"}`)); err == nil {
		t.Fatal("missing config allowed execution")
	}
}

func TestWriteOnceAcrossConcurrentServers(t *testing.T) {
	path, c, fp := configuration(t)
	var calls atomic.Int32
	run := func(ctx context.Context, r Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true,"outcome":"success","data":{"id":"created"}}`), nil
	}
	r := Request{Config: path, Fingerprint: fp, Args: []string{"todo", "task", "create", "--title=Task"}}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := executeOnce(context.Background(), c, "same-operation", r, run); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("write replayed %d times", calls.Load())
	}
	value, err := operationStatus(c, "same-operation", 0)
	if err != nil || value.(map[string]any)["status"] != "completed" {
		t.Fatal(value, err)
	}
	r.Args = append(r.Args, "--title=Different")
	if _, err := executeOnce(context.Background(), c, "same-operation", r, run); err == nil {
		t.Fatal("operation ID reused with different contents")
	}
	r.Args = []string{"todo", "task", "create"}
	broken := func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return nil, errors.New("worker timed out")
	}
	value, err = executeOnce(context.Background(), c, "uncertain", r, broken)
	if err != nil || value.(map[string]any)["status"] != "unknown" {
		t.Fatal(value, err)
	}
	value, err = executeOnce(context.Background(), c, "uncertain", r, broken)
	if err != nil || calls.Load() != 2 {
		t.Fatal("uncertain write replayed", err, calls.Load())
	}
}

func TestCommandScopeConfirmationAndFailures(t *testing.T) {
	path, c, fp := configuration(t)
	var mutations atomic.Int32
	run := func(ctx context.Context, r Request) (json.RawMessage, error) {
		switch r.Args[0] {
		case "schema":
			return json.RawMessage(`{"cli_path":"todo task delete","availability":"available","effect":"write","confirmation":"user_required","parameters":{"id":{"required":true,"type":"string"}}}`), nil
		case "profile":
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		default:
			mutations.Add(1)
			if !strings.Contains(strings.Join(r.Args, " "), "--profile corp:owner") {
				t.Error("identity not fixed")
			}
			return json.RawMessage(`{"ok":false,"outcome":"failure","error":{"message":"permission denied"}}`), nil
		}
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}
	request := `{"command":"todo task delete","parameters":{"id":"real-id"},"operation_id":"delete-1"}`
	if _, err := s.Call(context.Background(), "execute_command", json.RawMessage(request)); err == nil || mutations.Load() != 0 {
		t.Fatal("unconfirmed destructive action executed")
	}
	request = `{"command":"todo task delete","parameters":{"id":"real-id","profile":"other"},"operation_id":"delete-1","confirmed":true}`
	if _, err := s.Call(context.Background(), "execute_command", json.RawMessage(request)); err == nil {
		t.Fatal("profile override allowed")
	}
	request = `{"command":"todo task delete","parameters":{"id":"real-id"},"operation_id":"delete-1","confirmed":true}`
	value, err := s.Call(context.Background(), "execute_command", json.RawMessage(request))
	if err != nil || value.(map[string]any)["status"] != "failed" || mutations.Load() != 1 {
		t.Fatal("official failure was hidden", value, err)
	}
	if _, err := s.Call(context.Background(), "execute_command", json.RawMessage(request)); err != nil || mutations.Load() != 1 {
		t.Fatal("failed write was replayed", err)
	}
}

func TestLargeWriteReceiptPreservesCompleteEvidence(t *testing.T) {
	path, c, fp := configuration(t)
	raw, _ := json.Marshal(map[string]any{"ok": true, "outcome": "success", "data": strings.Repeat("quoted \"text\" 中文\n", 2000)})
	run := func(context.Context, Request) (json.RawMessage, error) { return raw, nil }
	value, err := executeOnce(context.Background(), c, "large", Request{Config: path, Fingerprint: fp, Args: []string{"todo", "task", "create"}}, run)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed := ""
	for {
		r := value.(map[string]any)
		if r["status"] != "completed" {
			t.Fatal("large result changed execution outcome", r)
		}
		reconstructed += r["result_text"].(string)
		offset, more := r["next_offset"].(int)
		if !more {
			break
		}
		value, err = operationStatus(c, "large", offset)
		if err != nil {
			t.Fatal(err)
		}
	}
	if reconstructed != string(raw) {
		t.Fatal("large write evidence was truncated or split inside UTF-8")
	}
}
