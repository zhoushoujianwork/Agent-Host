//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/taskfeedback"
)

type testFeedbackSender struct {
	calls      int
	status     string
	text, user string
}

func (s *testFeedbackSender) NotifyUser(_ context.Context, user, id, text string) (string, string, error) {
	s.calls++
	s.user, s.text = user, text
	if s.status == "accepted" {
		return "accepted", "platform-receipt", nil
	}
	return s.status, "", errors.New("fixture delivery " + s.status)
}

func TestCLIExplicitFeedbackDeliversOnceWithoutRerunningBusiness(t *testing.T) {
	_, args, capture := fileResumeFixture(t)
	root := t.TempDir()
	directory := filepath.Join(root, "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(directory, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", DisplayName: "Alice", Accounts: []userAccount{{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant", SenderID: "ou_alice"}}}}})
	args = append(args, "--feedback-config-dir", directory, "--request-for", "alice", "-m", "Check the named service")
	if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	home := ""
	for i, a := range args {
		if a == "--home" {
			home = args[i+1]
		}
	}
	runs := fileResumeRuns(t, home)
	if len(runs) != 1 || runs[0].Feedback == nil || runs[0].Feedback.NotifyTo != "alice" || runs[0].Feedback.Mode != "notify" {
		t.Fatal("CLI did not bind recipient", runs)
	}
	q, err := taskfeedback.Open(filepath.Join(root, "feedback"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	before, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	sender := &testFeedbackSender{status: "accepted"}
	account := taskfeedback.Account{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant"}
	for range 2 {
		if err = dispatchTaskFeedback(t.Context(), q, options{feedbackConfigDir: directory}, account, nil, sender); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(capture)
	items, _ := q.List(t.Context())
	if sender.calls != 1 || sender.user != "ou_alice" || sender.text == "" || len(items) != 1 || items[0].Status != "accepted" || items[0].Receipt != "platform-receipt" || string(before) != string(after) {
		t.Fatal("notification duplicated or replayed business", sender.calls, items)
	}
	var out bytes.Buffer
	if err = runNotifications(t.Context(), []string{"show", items[0].ID, "--config-dir", directory}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err = json.Unmarshal(out.Bytes(), &evidence); err != nil || len(evidence["attempts"].([]any)) != 1 {
		t.Fatal("missing delivery evidence", out.String(), err)
	}
	t.Log("CLI task executed once; verified recipient received one fixture notification; durable receipt and attempt history retained")
}
