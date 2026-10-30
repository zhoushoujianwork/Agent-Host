package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCandidateReviewReadOnlyPrivacyAndStates(t *testing.T) {
	home, db := fixture(t)
	_, err := db.Exec(`CREATE TABLE candidates(id TEXT,run_id TEXT,scope TEXT,agent_id TEXT,audience_id TEXT,status TEXT,payload BLOB)`)
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"pending", "rejected", "accepted", "pending"} {
		created := fixtureNow.Add(-10 * 24 * time.Hour)
		if i == 3 {
			created = fixtureNow.Add(-time.Hour)
		}
		note := ""
		if status == "rejected" {
			note = "superseded by PRIVATE_NEW_CANDIDATE"
		}
		payload, _ := json.Marshal(map[string]any{"created_at": created, "review_note": note, "draft": map[string]any{"summary": "PRIVATE_SUMMARY", "evidence_ids": []string{"PRIVATE_EVIDENCE"}}, "assessment": "PRIVATE_ASSESSMENT"})
		_, err = db.Exec(`INSERT INTO candidates VALUES(?,?,'PRIVATE_SCOPE','PRIVATE_AGENT','PRIVATE_AUDIENCE',?,?)`, string(rune('a'+i)), "source-run", status, payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO runs(id,payload) VALUES('source-run','{"experience_status":"reviewed","input":"PRIVATE_INPUT","output":"PRIVATE_OUTPUT","source":{"sender_id":"PRIVATE_SENDER"}}')`)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"state.db", "state.db-wal"} {
		before[name], err = os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := candidateReport(context.Background(), home, "all", 7*24*time.Hour, 20, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 3 || r.Truncated {
		t.Fatalf("report: %+v", r)
	}
	for i, c := range r.Candidates {
		if c.AgeSeconds == nil || *c.AgeSeconds != 864000 || (c.RunID != "source-run" || c.EvidenceCount != 1 || c.Publication != "unverified" || c.ExperienceState != "reviewed") {
			t.Fatalf("metadata: %+v", c)
		}
		if c.Status != []string{"pending", "rejected", "accepted"}[i] || c.HasReviewNote != (i == 1) {
			t.Fatal(c)
		}
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "PRIVATE_") || !strings.Contains(string(b), "supersession") {
		t.Fatal(string(b))
	}
	r, err = candidateReport(context.Background(), home, "pending", 0, 1, fixtureNow)
	if err != nil || len(r.Candidates) != 1 || r.Candidates[0].ID != "a" || !r.Truncated {
		t.Fatalf("bounded report: %+v %v", r, err)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("modified %s: %v", name, err)
		}
	}
	if _, err = os.Stat(filepath.Join(home, "host.lock")); !os.IsNotExist(err) {
		t.Fatal("host lock created")
	}
	// A rejected candidate with a supersession note stays rejected; free text does
	// not establish a machine-verifiable replacement or publication result.
}

func TestCandidateReviewValidationAndUnknownAge(t *testing.T) {
	for _, args := range [][]string{nil, {"--home", "x", "--limit", "101"}, {"--home", "x", "--min-age", "-1h"}, {"--home", "x", "--status", "superseded"}} {
		if RunCandidateReview(context.Background(), args, io.Discard, io.Discard) == nil {
			t.Fatal(args)
		}
	}
	home, db := fixture(t)
	_, err := db.Exec(`CREATE TABLE candidates(id TEXT,run_id TEXT,scope TEXT,agent_id TEXT,audience_id TEXT,status TEXT,payload BLOB); INSERT INTO candidates VALUES('invalid','missing','','','','pending','{"created_at":"invalid"}');`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := candidateReport(context.Background(), home, "pending", 168*time.Hour, 20, fixtureNow)
	if err != nil || len(r.Candidates) != 1 || r.Candidates[0].AgeSeconds != nil {
		t.Fatalf("%+v %v", r, err)
	}
	var out bytes.Buffer
	if err := RunCandidateReview(context.Background(), []string{"--home", home}, &out, io.Discard); err != nil || !json.Valid(out.Bytes()) {
		t.Fatalf("%s %v", out.String(), err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if RunCandidateReview(context.Background(), []string{"--home", missing}, io.Discard, io.Discard) == nil {
		t.Fatal("missing store accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing home created")
	}
}
