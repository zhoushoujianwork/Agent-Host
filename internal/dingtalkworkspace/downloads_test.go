package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMessageDownloadsUseOfficialContractsAndOwner(t *testing.T) {
	path, c, _ := configuration(t)
	c.Services = append(c.Services, "chat")
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	official := Process(executable)
	contracts := map[string]json.RawMessage{}
	for _, command := range []string{"chat +messages-resource-download", "chat +chat-messages"} {
		raw, e := official(t.Context(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", command, "--compact", "--format", "json"}})
		if e != nil {
			t.Fatal(e)
		}
		contracts[command] = raw
	}
	calls := 0
	fail := false
	run := func(_ context.Context, r Request) (json.RawMessage, error) {
		if r.Args[0] == "schema" {
			return contracts[r.Args[2]], nil
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		}
		calls++
		joined := strings.Join(r.Args, " ")
		if !strings.Contains(joined, "--profile corp:owner --format json") || strings.Contains(joined, "--yes") {
			t.Fatal("download identity or effect changed", joined)
		}
		if fail {
			return json.RawMessage(`{"ok":false,"error":{"code":"RESOURCE_NOT_FOUND"}}`), &CommandFailure{Code: 1}
		}
		if r.Args[1] == "+messages-resource-download" {
			for _, flag := range []string{"--resource-id=source-file", "--type=fileId", "--output=downloads/skill.skill"} {
				if !strings.Contains(joined, flag) {
					t.Fatal("resource reference lost", joined)
				}
			}
			return json.RawMessage(`{"localPath":"downloads/skill.skill","sizeBytes":123}`), nil
		}
		for _, flag := range []string{"--download-resources=true", "--output-dir=downloads/skills", "--page-limit=1", "--max-items=2", "--no-reactions=true"} {
			if !strings.Contains(joined, flag) {
				t.Fatal("download read bounds lost", joined)
			}
		}
		return json.RawMessage(`{"complete":true,"resourceDownloads":{"ok":false,"partial":true,"downloadedCount":1,"failedCount":1,"downloads":[{"localPath":"downloads/skills/one.skill"}],"failures":[{"error":"denied"}]}}`), nil
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}
	request := json.RawMessage(`{"command":"chat +messages-resource-download","parameters":{"resource-id":"source-file","type":"fileId","output":"downloads/skill.skill"}}`)
	value, err := s.Call(t.Context(), "execute_command", request)
	if err != nil || !strings.Contains(string(value.(json.RawMessage)), "localPath") {
		t.Fatal(value, err)
	}
	value, err = s.Call(t.Context(), "read_messages", json.RawMessage(`{"peer_open_dingtalk_id":"peer","after":"2026-10-21T06:00:00Z","before":"2026-10-21T07:00:00Z","limit":2,"download_resources":true,"output_dir":"downloads/skills"}`))
	if err != nil || !strings.Contains(string(value.(json.RawMessage)), `"failedCount":1`) {
		t.Fatal("partial download evidence lost", value, err)
	}
	for _, params := range []map[string]string{
		{"resource-id": "source", "overwrite": "true"},
		{"resource-id": "source", "output-dir": "downloads"},
		{"resource-id": "source", "profile": "other"},
		{"resource-id": "source", "output-file": "leak"},
	} {
		raw, _ := json.Marshal(Args{Command: "chat +messages-resource-download", Parameters: params})
		if _, err := s.Call(t.Context(), "execute_command", raw); err == nil {
			t.Fatal("unsupported override accepted", params)
		}
	}
	if calls != 2 {
		t.Fatal("rejected downloads executed", calls)
	}
	if _, err = s.Call(t.Context(), "read_messages", json.RawMessage(`{"peer_open_dingtalk_id":"peer","after":"2026-10-21T06:00:00Z","before":"2026-10-21T07:00:00Z","output_dir":"downloads/skills"}`)); err == nil || calls != 2 {
		t.Fatal("download must be explicit", err)
	}
	fail = true
	if _, err = s.Call(t.Context(), "execute_command", request); err == nil || !strings.Contains(err.Error(), "RESOURCE_NOT_FOUND") {
		t.Fatal("upstream failure hidden", err)
	}
	var catalog []map[string]any
	var contract any
	json.Unmarshal(contracts["chat +messages-resource-download"], &contract)
	collectCommands(contract, "chat", "download", &catalog)
	if len(catalog) != 1 {
		t.Fatal("download missing from catalog", catalog)
	}
	if _, err = commandPath(Config{Services: []string{"contact"}}, "chat +messages-resource-download"); err == nil {
		t.Fatal("disabled chat downloaded")
	}
}

func TestResourceOutputExceptionDoesNotEnableGlobalCapture(t *testing.T) {
	schema := map[string]any{"parameters": map[string]any{"output": map[string]any{}}}
	for _, command := range []string{"chat +chat-messages", "contact user list", "drive file list"} {
		if _, err := commandArguments(strings.Split(command, " "), map[string]string{"output": "capture.json"}, schema); err == nil {
			t.Fatal("global output override accepted", command)
		}
	}
}

func TestResourceDownloadDoesNotSwitchOwnerIdentity(t *testing.T) {
	path, c, fp := configuration(t)
	c.Services = append(c.Services, "chat")
	b, _ := json.Marshal(c)
	os.WriteFile(path, b, 0600)
	_, fp, _ = Load(path)
	called := false
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: func(_ context.Context, r Request) (json.RawMessage, error) {
		if r.Args[0] == "schema" {
			return json.RawMessage(`{"cli_path":"chat +messages-resource-download","availability":"available","effect":"read","parameters":{"resource-id":{"required":true}}}`), nil
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:other","corpId":"corp","userId":"other"}]}`), nil
		}
		called = true
		return nil, errors.New("must not download")
	}}
	if _, err := s.Call(t.Context(), "execute_command", json.RawMessage(`{"command":"chat +messages-resource-download","parameters":{"resource-id":"source"}}`)); err == nil || called {
		t.Fatal("wrong identity downloaded", err)
	}
}
