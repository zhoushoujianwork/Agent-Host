package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/state"
)

func chatOffloadFixture(t *testing.T) (options, []string) {
	t.Helper()
	dir := canonicalTemp(t)
	o := offloadOptions(t, "dingtalk", dir, canonicalTemp(t))
	return o, []string{"chat", "--offload-config", o.offloadConfig, "--workspace", o.workspace, "--home", o.home, "--format", "json"}
}

func chatOffloadEvents(t *testing.T, args []string) ([]event.Event, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), args, nil, &out, io.Discard)
	dec := json.NewDecoder(&out)
	var events []event.Event
	for {
		var e event.Event
		if decodeErr := dec.Decode(&e); decodeErr == io.EOF {
			break
		} else if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		events = append(events, e)
	}
	return events, err
}

func TestOffloadChatOneShotAndResume(t *testing.T) {
	o, args := chatOffloadFixture(t)
	var previous state.Run
	for index, text := range []string{"first question", "follow up"} {
		turnArgs := append(append([]string{}, args...), "-m", text)
		if index > 0 {
			turnArgs = append(turnArgs, "--resume", previous.SessionID)
		}
		events, err := chatOffloadEvents(t, turnArgs)
		if err != nil {
			t.Fatal(err)
		}
		var runID string
		ready := 0
		for _, e := range events {
			if e.Type == "run.started" {
				runID = e.RunID
			}
			if e.Type == "execution.ready" {
				ready++
				if e.Data["output"] == "" {
					t.Fatal("answer not visible")
				}
			}
			if e.Type == "offload.result" {
				t.Fatal("private record leaked into Chat output")
			}
		}
		if ready != 1 || runID == "" {
			t.Fatal("missing answer or run")
		}
		store, err := state.Open(o.home, false)
		if err != nil {
			t.Fatal(err)
		}
		run, err := store.Run(t.Context(), runID)
		store.Close()
		if err != nil {
			t.Fatal(err)
		}
		if run.Source != nil || run.Room != nil || run.ExecutionStatus != "completed" || !run.MemoryPluginDisabled {
			t.Fatalf("fabricated provenance or outcome: %+v", run)
		}
		if index > 0 && (run.SessionID != previous.SessionID || run.NativeSessionID != previous.NativeSessionID) {
			t.Fatal("native continuity lost")
		}
		previous = run
	}
	if _, err := chatOffloadEvents(t, append(append([]string{}, args...), "-m", "changed", "--resume", previous.SessionID, "--workspace", canonicalTemp(t))); err == nil {
		t.Fatal("changed workspace silently adopted native history")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.offloadConfig), "argv.3")); !os.IsNotExist(err) {
		t.Fatal("changed workspace launched native execution")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.offloadConfig), "argv.2"))
	if err != nil || !strings.Contains(string(raw), "-r\nnative-1\n") || !strings.Contains(string(raw), "--caller-run-id\nagh:"+previous.ID+"\n") {
		t.Fatalf("bad resume: %s %v", raw, err)
	}
}

func TestOffloadChatRejectsUnsupportedModesAndPoliciesBeforeExecution(t *testing.T) {
	for name, extra := range map[string][]string{
		"interactive": {}, "room": {"-m", "x", "--room", "r"},
		"renew": {"-m", "x", "--renew-runtime"}, "takeover": {"-m", "x", "--takeover-session", "s", "--request-id", "q"},
		"notification": {"-m", "x", "--notify-to", "someone"},
		"tools":        {"-m", "x", "--no-tools"}, "audience": {"-m", "x", "--audience", "other"},
	} {
		t.Run(name, func(t *testing.T) {
			o, args := chatOffloadFixture(t)
			if _, err := chatOffloadEvents(t, append(args, extra...)); err == nil {
				t.Fatal("unsupported request accepted")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.offloadConfig), "count")); !os.IsNotExist(err) {
				t.Fatal("unsupported request launched execution")
			}
		})
	}
}

func TestOffloadChatHonorsStateAndSessionLocks(t *testing.T) {
	o, args := chatOffloadFixture(t)
	release, err := state.Acquire(o.home)
	if err != nil {
		t.Fatal(err)
	}
	_, err = chatOffloadEvents(t, append(args, "-m", "blocked"))
	release()
	if err == nil {
		t.Fatal("exclusive channel writer lock ignored")
	}
	events, err := chatOffloadEvents(t, append(args, "-m", "first"))
	if err != nil {
		t.Fatal(err)
	}
	var session string
	for _, e := range events {
		if e.Type == "run.started" {
			session = e.SessionID
		}
	}
	release, err = state.AcquireChatSession(o.home, session)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = chatOffloadEvents(t, append(args, "-m", "busy", "--resume", session)); err == nil {
		t.Fatal("session writer lock ignored")
	}
}

func TestOffloadChatTextOutputShowsAnswer(t *testing.T) {
	_, args := chatOffloadFixture(t)
	args = append(args, "--format", "text", "-m", "hello")
	var out bytes.Buffer
	if err := Run(t.Context(), args, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "answer-1") != 1 {
		t.Fatalf("missing or duplicate answer: %s", out.String())
	}
}

func TestOffloadChatRetrievalOnlyNeedsNoClassifier(t *testing.T) {
	o, args := chatOffloadFixture(t)
	raw, err := os.ReadFile(o.offloadConfig)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data["memory"] = map[string]any{"enabled": true, "processing_enabled": false}
	writeBotJSON(t, o.offloadConfig, data)
	if _, err = chatOffloadEvents(t, append(args, "-m", "read only memory")); err != nil {
		t.Fatal(err)
	}
	data["memory"] = map[string]any{"enabled": true}
	writeBotJSON(t, o.offloadConfig, data)
	if _, err = chatOffloadEvents(t, append(args, "-m", "missing key")); err == nil {
		t.Fatal("missing classifier credentials accepted")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(o.offloadConfig), "argv.2")); !os.IsNotExist(err) {
		t.Fatal("missing key launched execution")
	}
}

func TestOffloadWebRoomContinuationAndRecordedRetry(t *testing.T) {
	o, base := chatOffloadFixture(t)
	var runs []state.Run
	for _, request := range []string{"one", "two", "two"} {
		events, err := chatOffloadEvents(t, append(append([]string{}, base...), "--room", "web-room", "--request-id", request, "-m", request))
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for _, e := range events {
			if e.Type == "run.started" {
				id = e.RunID
			}
			if e.Type == "offload.result" {
				t.Fatal("private evidence replayed")
			}
			if e.Type == "run.finished" && e.Data["memory_status"] != "skipped" {
				t.Fatal("outcome not projected")
			}
		}
		st, err := state.Open(o.home, false)
		if err != nil {
			t.Fatal(err)
		}
		r, err := st.Run(t.Context(), id)
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	if runs[0].SessionID == runs[1].SessionID || runs[0].NativeSessionID != runs[1].NativeSessionID || runs[1].ID != runs[2].ID || runs[1].Room.NativeContinuation != state.OffloadRoomResume {
		t.Fatal("room continuity or deduplication lost")
	}
	dir := filepath.Dir(o.offloadConfig)
	if _, err := os.Stat(filepath.Join(dir, "argv.3")); !os.IsNotExist(err) {
		t.Fatal("duplicate replayed native work")
	}
	if _, err := chatOffloadEvents(t, append(append([]string{}, base...), "--room", "web-room", "--request-id", "two", "-m", "different")); err == nil {
		t.Fatal("request ID reused with changed content")
	}
}
