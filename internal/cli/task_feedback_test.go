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
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
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

func TestCLIRecipientResolutionDeliversOnceWithoutRerunningBusiness(t *testing.T) {
	_, args, capture := fileResumeFixture(t)
	root := t.TempDir()
	directory := filepath.Join(root, "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(directory, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", DisplayName: "Alice", Accounts: []userAccount{{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant", SenderID: "ou_alice"}}}}})
	args = append(args, "--feedback-config-dir", directory, "--request-for", "missing", "-m", "Check the named service")
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
	if len(runs) != 1 || runs[0].Feedback == nil || runs[0].Feedback.NotifyTo != "missing" || runs[0].Feedback.Mode != "needs_recipient" {
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
	pending, err := q.List(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	resolveArgs := []string{"resolve", pending[0].ID, "--config-dir", directory, "--notify-to", "alice", "--notify-channel", "feishu"}
	if err = runNotifications(t.Context(), resolveArgs, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err = runNotifications(t.Context(), resolveArgs, io.Discard, io.Discard); err == nil {
		t.Fatal("repeated resolution succeeded")
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
	if err = json.Unmarshal(out.Bytes(), &evidence); err != nil || len(evidence["attempts"].([]any)) != 2 {
		t.Fatal("missing delivery evidence", out.String(), err)
	}
	if len(fileResumeRuns(t, home)) != 1 {
		t.Fatal("resolution created another run")
	}
	if err = runNotifications(t.Context(), resolveArgs, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted notification resolved again")
	}
	t.Log("CLI task executed once; verified recipient received one fixture notification; durable receipt and attempt history retained")
}

// openFeedbackSource opens the first queued item's source state the way a
// peer would: through the Host lease, with no run.finished receipt written.
func openFeedbackSource(t *testing.T, q *taskfeedback.Queue) *state.Store {
	t.Helper()
	items, err := q.List(t.Context())
	if err != nil || len(items) == 0 {
		t.Fatal(items, err)
	}
	release, err := state.Acquire(items[0].Home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release() })
	s, err := state.Open(items[0].Home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// completeNative reproduces the window this change opens: native execution is
// durably complete and closeout is pending, with no final run receipt.
func completeNative(t *testing.T, s *state.Store) {
	t.Helper()
	if err := s.CompleteRun(t.Context(), "run", "completed", "Service healthy", "observed-model", ""); err != nil {
		t.Fatal(err)
	}
}

func announceReady(t *testing.T, s *state.Store) {
	t.Helper()
	e := event.Event{RunID: "run", Type: "execution.ready", Phase: "execute"}
	if err := s.AppendEvent(t.Context(), &e); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackNotifySendsOnceFromExecutionReadyWhileCloseoutPending(t *testing.T) {
	q, o, a := feedbackWorkerFixture(t, false)
	source := openFeedbackSource(t, q)
	completeNative(t, source)
	sender := &testFeedbackSender{status: "accepted"}
	// Completed native execution without execution.ready or run.finished still waits.
	if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now()); err != nil || sender.calls != 0 {
		t.Fatalf("delivered before the answer was announced: calls=%d err=%v", sender.calls, err)
	}
	announceReady(t, source)
	for i := 0; i < 2; i++ {
		if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := q.List(t.Context())
	if sender.calls != 1 || items[0].Status != "accepted" || items[0].Attempts != 1 || items[0].ExecutionStatus != "completed" || items[0].SourceObservation != "" || sender.user != "ou_alice" || !bytes.Contains([]byte(sender.text), []byte("Service healthy")) {
		t.Fatalf("notify answer must send exactly once from execution.ready: calls=%d item=%+v text=%q", sender.calls, items[0], sender.text)
	}
}

func TestFeedbackNotifyUnknownEarlySendIsNeverRetried(t *testing.T) {
	q, o, a := feedbackWorkerFixture(t, false)
	source := openFeedbackSource(t, q)
	completeNative(t, source)
	announceReady(t, source)
	sender := &testFeedbackSender{status: "unknown"}
	for _, offset := range []time.Duration{0, time.Minute, time.Hour} {
		if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := q.List(t.Context())
	if sender.calls != 1 || items[0].Status != "unknown" {
		t.Fatalf("unknown outcome was retried: calls=%d %+v", sender.calls, items[0])
	}
}

func TestFeedbackOriginModeIgnoresExecutionReadyUntilFinalReceipt(t *testing.T) {
	q, o, a := feedbackWorkerFixture(t, false)
	items, _ := q.List(t.Context())
	origin := items[0]
	origin.ID = "origin-notification"
	origin.Plan = taskfeedback.Plan{Requester: "owner", RequestFor: "alice", NotifyTo: "alice", Mode: "origin", Target: &a, Origin: &taskfeedback.Origin{Account: a, MessageID: "message-1"}}
	if err := q.Bind(t.Context(), origin); err != nil {
		t.Fatal(err)
	}
	source := openFeedbackSource(t, q)
	completeNative(t, source)
	announceReady(t, source)
	// The notify item was bound too; it is not under test here.
	sender := &testFeedbackSender{status: "accepted"}
	if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now()); err != nil {
		t.Fatal(err)
	}
	all, _ := q.List(t.Context())
	for _, item := range all {
		if item.ID != "origin-notification" {
			continue
		}
		// The channel owns the origin reply; recovery must neither claim nor
		// observe a host exit while the live turn is still in closeout.
		if item.Status != "awaiting_result" || item.Attempts != 0 || item.SourceObservation != "" {
			t.Fatalf("origin recovery raced the early channel reply: %+v", item)
		}
	}
	if sender.calls != 1 || sender.user != "ou_alice" {
		t.Fatalf("only the verified notify item may use the direct sender: calls=%d", sender.calls)
	}
}
