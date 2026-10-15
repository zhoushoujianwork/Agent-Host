package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type externalCLIDecider struct{}

func (externalCLIDecider) Decide(context.Context, string, []state.Memory) (memory.Decision, error) {
	return memory.Decision{Store: true, Kind: "decision", Details: json.RawMessage(`{"reason":"fixture"}`)}, nil
}

func TestExternalMemoryCLIEndToEndAcrossConnectionsAndRestart(t *testing.T) {
	root, err := os.MkdirTemp("", "ah-ext-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "state")
	s, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "external-test"); err != nil {
		t.Fatal(err)
	}
	x := &externalMemoryControl{store: s, wake: make(chan struct{}, 1)}
	x.resolve = func(_ context.Context, agent, project string) (state.ExternalConnection, error) {
		if agent == "" {
			agent = "default"
		}
		scope := "project:source"
		if project != "" {
			scope = "project:" + project
		}
		return state.ExternalConnection{AgentID: agent, AudienceID: "owner", Scope: scope, ProjectID: project, Enabled: true}, nil
	}
	x.processor = func(c state.ExternalConnection) *host.Host {
		return &host.Host{Store: s.WithPartition(c.AgentID, c.AudienceID), Decider: externalCLIDecider{}, Objects: fixtureObjects{}}
	}
	stop, err := startMemoryControl(t.Context(), home, &memoryControl{external: x})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	profiles := filepath.Join(root, "profiles.json")
	call := func(input string, args ...string) (map[string]any, error) {
		t.Helper()
		full := append([]string{"memory"}, args...)
		full = append(full, "--connections", profiles)
		var out, errout bytes.Buffer
		err := Run(t.Context(), full, strings.NewReader(input), &out, &errout)
		var v map[string]any
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &v)
		}
		return v, err
	}
	for _, name := range []string{"codex", "same-scope", "foreign"} {
		args := []string{"connection", "add", "--home", home, "--connection", name}
		if name == "codex" {
			args = append(args, "--checkpoint-save")
		}
		if name == "foreign" {
			args = append(args, "--project", "foreign")
		}
		if _, err := call("", args...); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(profiles)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	show, err := call("", "connection", "show", "--connection", "codex")
	if err != nil || show["checkpoint_save"] != true {
		t.Fatal(show, err)
	}
	now := time.Now().UTC()
	v := state.ExternalSubmission{SchemaVersion: 1, Key: "stable-key", Source: state.ExternalSource{Entry: "codex", ObservedAt: now}, Items: []state.ExternalItem{{Kind: "statement", Text: "The release distribution uses source only.", EvidenceIDs: []string{"u"}}}, Evidence: []state.ExternalEvidence{{ID: "u", Author: "owner", Role: "user", Text: "The release distribution uses source only.", ObservedAt: now}}}
	data, _ := json.Marshal(v)
	intake, err := call(string(data), "submit", "--connection", "codex", "--input", "-")
	if err != nil || intake["ingestion_status"] != "received" || intake["processing_status"] != "pending" {
		t.Fatal(intake, err)
	}
	// The worker starts after intake, representing a restart with pending jobs.
	stopWorker := x.start(t.Context())
	defer stopWorker()
	var receipt map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err = call("", "receipt", "--connection", "codex", "--key", v.Key)
		if err != nil {
			t.Fatal(err)
		}
		if receipt["processing_status"] == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if receipt["processing_status"] != "completed" {
		t.Fatal(receipt)
	}
	id := receipt["results"].([]any)[0].(map[string]any)["memory_id"].(string)
	extracted, err := call("", "extract-objects", "--connection", "same-scope", "--memory", id, "--revision", "1")
	if err != nil || extracted["id"] != id || len(extracted["objects"].([]any)) == 0 {
		t.Fatal(extracted, err)
	}
	objects := []state.MemoryObject{{Name: "Bastion", Kind: "system", Source: state.ExternalSource{Entry: "test", ObservedAt: now}, Evidence: state.ExternalEvidence{ID: "context", Author: "owner", Role: "user", Text: "This distribution fact concerns Bastion.", ObservedAt: now}}}
	objectJSON, _ := json.Marshal(objects)
	linked, err := call(string(objectJSON), "associate", "--connection", "same-scope", "--memory", id, "--revision", "1", "--input", "-")
	if err != nil || linked["revision"] != float64(2) {
		t.Fatal(linked, err)
	}
	if _, err := call("", "extract-objects", "--connection", "same-scope", "--memory", id, "--revision", "1"); err == nil {
		t.Fatal("stale object repair accepted")
	}
	recalled, err := call("", "recall", "--connection", "same-scope", "--query", "Bastion")
	if err != nil || len(recalled["items"].([]any)) != 1 {
		t.Fatal(recalled, err)
	}
	if len(recalled["items"].([]any)[0].(map[string]any)["objects"].([]any)) != 1 {
		t.Fatal("object missing from recall", recalled)
	}
	read, err := call("", "read", "--connection", "same-scope", "--memory", id, "--revision", "2")
	if err != nil || read["supporting_evidence"].(map[string]any)["role"] != "external_agent_supplied_evidence" {
		t.Fatal(read, err)
	}
	search, err := call("", "recall", "--connection", "same-scope", "--query", "release distribution")
	if err != nil || len(search["items"].([]any)) != 1 {
		t.Fatal(search, err)
	}
	if _, err := call("", "read", "--connection", "foreign", "--memory", id); err == nil {
		t.Fatal("foreign scope read succeeded")
	}
	if _, err := call("", "receipt", "--connection", "same-scope", "--receipt", intake["id"].(string)); err == nil {
		t.Fatal("foreign connection read receipt")
	}
	again, err := call(string(data), "submit", "--connection", "codex", "--input", "-")
	if err != nil || again["id"] != intake["id"] {
		t.Fatal(again, err)
	}
	if _, err := call("", "connection", "revoke", "--connection", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := call("", "receipt", "--connection", "codex", "--key", v.Key); err == nil {
		t.Fatal("revoked connection read receipt")
	}
	if _, err := call(string(data), "submit", "--connection", "same-scope", "--input", "-", "--agent", "foreign"); err == nil {
		t.Fatal("payload scope override accepted")
	}
}

func TestExternalMemoryConnectionRechecksHostConfiguration(t *testing.T) {
	root := t.TempDir()
	s, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "test"); err != nil {
		t.Fatal(err)
	}
	x := newExternalMemoryControl(s, options{workspace: root}, nil)
	c, err := x.resolve(t.Context(), "default", "")
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "test"
	if err := s.SaveExternalConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := x.binding(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	x.resolve = func(context.Context, string, string) (state.ExternalConnection, error) {
		return state.ExternalConnection{}, errors.New("Agent removed")
	}
	if _, err := x.binding(t.Context(), c.ID); err == nil {
		t.Fatal("removed configuration retained access")
	}
}
