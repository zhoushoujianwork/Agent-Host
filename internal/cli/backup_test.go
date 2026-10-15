package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/backup"
)

func TestBackupCommandsCreateVerifyRestoreAndStatus(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "host")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	call := func(args ...string) {
		t.Helper()
		out.Reset()
		errout.Reset()
		if err := Run(context.Background(), append([]string{"backup"}, args...), nil, &out, &errout); err != nil {
			t.Fatal(err, errout.String())
		}
	}
	key := filepath.Join(base, "key")
	call("init", "--root", root, "--destination", filepath.Join(base, "archives"), "--identity", key, "--timezone", "Asia/Singapore")
	call("create", "--root", root)
	var point backup.Point
	if err := json.Unmarshal(out.Bytes(), &point); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "archives", point.Archive)
	call("verify", "--archive", archive, "--identity", key)
	call("restore", "--archive", archive, "--identity", key, "--target", filepath.Join(base, "restore"))
	call("status", "--root", root)
	var status backup.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.Latest == nil || status.LastError != "" {
		t.Fatalf("bad status %+v %v", status, err)
	}
	call("list", "--root", root)
	if err := Run(context.Background(), []string{"backup", "restore", "--archive", archive, "--identity", key}, nil, &out, &errout); err == nil {
		t.Fatal("missing target accepted")
	}
}

func TestBackupPolicyIsAcceptedByServicePreflight(t *testing.T) {
	root, directory := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(directory, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"bot": map[string]any{"enabled": false}, "dws": map[string]any{"enabled": true, "profile": "fixture-corp:fixture-owner", "services": []string{"contact"}}}})
	outside := t.TempDir()
	if err := backup.Init(root, filepath.Join(outside, "archives"), filepath.Join(outside, "key"), "Asia/Singapore"); err != nil {
		t.Fatal(err)
	}
	channels, err := discoverUnifiedChannels(directory)
	if err != nil || len(channels) != 1 {
		t.Fatalf("backup policy blocked startup: %+v %v", channels, err)
	}
	var output bytes.Buffer
	if err = runService(t.Context(), []string{"check", "--config-dir", directory}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "backup.json"), []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = discoverUnifiedChannels(directory); err == nil {
		t.Fatal("invalid backup policy passed startup")
	}
}

func TestBackupInitDefaultsToExistingBackupsDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "host")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if err := Run(t.Context(), []string{"backup", "init", "--root", root, "--identity", filepath.Join(base, "key")}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	var initialized struct {
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal(out.Bytes(), &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Destination != filepath.Join(root, "backups") {
		t.Fatalf("unexpected default %s", initialized.Destination)
	}
}
