//go:build unix

package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/state"
)

func fileResumeFixture(t *testing.T) (reviewFixture, []string, string) {
	t.Helper()
	t.Setenv("JEV_API_KEY", "synthetic-resume-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Questions map[string]struct {
				Type     string                     `json:"type"`
				Criteria map[string]json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		answers := map[string]any{}
		for id, question := range request.Questions {
			choice := "none"
			if id == "eligibility" {
				choice = "skip"
			}
			if _, ok := question.Criteria[choice]; !ok {
				t.Errorf("unexpected resume decision question %s", id)
				w.WriteHeader(400)
				return
			}
			probabilities := map[string]float64{}
			for key := range question.Criteria {
				probabilities[key] = 0
			}
			probabilities[choice] = 1
			answers[id] = map[string]any{"type": question.Type, "choice": choice, "probabilities": probabilities, "confidence": 1}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "resume-judge-fixture", "answers": answers, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(server.Close)
	fixture := seedReviewFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	memoryStorageCommand(t, "migrate", "--home", fixture.home, "--memory-root", filepath.Join(root, "memory"), "--memory-cache", filepath.Join(root, "cache", "memory"), "--memory-source", "resume-test")
	binary := filepath.Join(root, "claude-fixture")
	capture := filepath.Join(root, "native-calls")
	t.Setenv("AGENT_HOST_FILE_RESUME_CAPTURE", capture)
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then echo '--plugin-dir <path>'; exit 0; fi
if [ "$1" = "--version" ]; then echo '2.1.277 (Claude Code)'; exit 0; fi
session=''
resumed='false'
plugin=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session-id) session="$2"; shift ;;
    --resume) session="$2"; resumed='true'; shift ;;
    --plugin-dir) plugin="$2"; shift ;;
  esac
  shift
done
cat >/dev/null
printf '%s|%s|%s\n' "$session" "$resumed" "$plugin" >> "$AGENT_HOST_FILE_RESUME_CAPTURE"
printf '{"type":"system","subtype":"init","session_id":"%s","model":"resume-native-fixture"}\n' "$session"
printf '{"type":"assistant","session_id":"%s","message":{"model":"resume-native-fixture","content":[{"type":"text","text":"Fixture plan."}]}}\n' "$session"
printf '{"type":"result","subtype":"success","session_id":"%s","is_error":false,"result":"Fixture plan."}\n' "$session"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "native-profile")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"chat", "--home", fixture.home, "--workspace", fixture.workspace, "--claude", binary, "--native-profile", profile, "--model", "test", "--jev-provider", "custom", "--jev-endpoint", server.URL, "--format", "json"}
	return fixture, args, capture
}

func fileResumeRuns(t *testing.T, home string) []state.Run {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT payload FROM runs WHERE id!='review-run' ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var runs []state.Run
	for rows.Next() {
		var raw []byte
		var run state.Run
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestFileMemoryOneShotAndInteractiveResumeKeepUnchangedSkillSet(t *testing.T) {
	fixture, args, capture := fileResumeFixture(t)
	query := "fixture test package"
	if err := Run(t.Context(), append(append([]string(nil), args...), "-m", query), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	first := fileResumeRuns(t, fixture.home)
	if len(first) != 1 || len(first[0].Context) != 1 {
		t.Fatalf("first Skill context missing: %+v", first)
	}
	resume := append(append([]string(nil), args...), "--resume", first[0].SessionID)
	if err := Run(t.Context(), append(append([]string(nil), resume...), "-m", query), nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("one-shot resume: %v", err)
	}
	if err := Run(t.Context(), resume, strings.NewReader(query+"\n"), io.Discard, io.Discard); err != nil {
		t.Fatalf("interactive resume: %v", err)
	}
	runs := fileResumeRuns(t, fixture.home)
	if len(runs) != 3 {
		t.Fatalf("expected three business runs, got %d", len(runs))
	}
	for _, run := range runs {
		if run.SessionID != first[0].SessionID || run.ExecutionStatus != "completed" {
			t.Fatalf("unchanged Skill set rotated or failed: %+v", run)
		}
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("native calls=%s", raw)
	}
	for index, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) != 3 || parts[0] != first[0].NativeSessionID || parts[1] != fmt.Sprint(index > 0) || parts[2] == "" {
			t.Fatalf("native resume or Skill flag mismatch: %s", line)
		}
	}
}

func TestFileMemoryInteractiveResumeRefreshesChangedSkillSet(t *testing.T) {
	fixture, args, capture := fileResumeFixture(t)
	if err := Run(t.Context(), append(append([]string(nil), args...), "-m", "fixture test package"), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	first := fileResumeRuns(t, fixture.home)[0]
	resume := append(append([]string(nil), args...), "--resume", first.SessionID)
	if err := Run(t.Context(), resume, strings.NewReader("Unrelated violet orchard\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	runs := fileResumeRuns(t, fixture.home)
	if len(runs) != 2 || runs[1].SessionID == first.SessionID || runs[1].RuntimeFingerprint == first.RuntimeFingerprint || len(runs[1].Context) != 0 {
		t.Fatalf("changed Skill set reused old history: %+v", runs)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("native calls=%s", raw)
	}
	parts := strings.Split(lines[1], "|")
	if len(parts) != 3 || parts[0] == first.NativeSessionID || parts[1] != "false" || parts[2] != "" {
		t.Fatalf("withdrawn Skill remained in native request: %s", lines[1])
	}
}
