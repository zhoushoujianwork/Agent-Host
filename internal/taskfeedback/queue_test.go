package taskfeedback

import (
	"path/filepath"
	"testing"
)

func TestQueueClaimsOnceAndPreservesUnknownOnRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "feedback")
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	a := Account{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_alice"}
	item := Item{ID: "delivery", RunID: "run", Home: "/trusted/home", Plan: Plan{Mode: "notify", Target: &a}}
	if err = q.Bind(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if err = q.Bind(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	items, _ := q.List(t.Context())
	if len(items) != 1 {
		t.Fatal(items)
	}
	first, second := items[0], items[0]
	ok, err := q.Claim(t.Context(), &first, "Exact result")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err = q.Claim(t.Context(), &second, "Different text"); err != nil || ok {
		t.Fatal("duplicate claimed", ok, err)
	}
	if err = q.Recover(t.Context(), Account{Channel: "dingtalk", AccountID: "other", TenantID: "tenant"}); err != nil {
		t.Fatal(err)
	}
	items, _ = q.List(t.Context())
	if items[0].Status != "sending" {
		t.Fatal("another adapter changed live work")
	}
	if err = q.Recover(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	items, _ = q.List(t.Context())
	if items[0].Status != "unknown" || items[0].Text != "Exact result" || items[0].Attempts != 1 {
		t.Fatal(items)
	}
	if claimed, err := q.Claim(t.Context(), &items[0], "Replay"); err == nil || claimed {
		t.Fatal("unknown send was claimable")
	}
}

func TestRecipientResolutionPreservesAuditAndInspectionIsReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "feedback")
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err = q.Bind(t.Context(), Item{ID: "pending", RunID: "run", Home: "/trusted/home", Plan: Plan{Mode: "needs_recipient", Requester: "owner"}}); err != nil {
		t.Fatal(err)
	}
	items, _ := q.List(t.Context())
	old := items[0]
	a := Account{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_alice"}
	plan := Plan{Mode: "notify", Requester: "owner", RequestFor: "alice", NotifyTo: "alice", Target: &a}
	if err = q.ResolveRecipient(t.Context(), old, plan); err != nil {
		t.Fatal(err)
	}
	if err = q.ResolveRecipient(t.Context(), old, plan); err == nil {
		t.Fatal("stale resolution replaced current plan")
	}
	items, _ = q.List(t.Context())
	audit, _ := q.Attempts(t.Context(), old.ID)
	if items[0].Status != "awaiting_result" || items[0].Plan.NotifyTo != "alice" || len(audit) != 1 {
		t.Fatal(items, audit)
	}
	reader, err := OpenForInspection(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err = reader.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = reader.Bind(t.Context(), Item{ID: "must-not-write"}); err == nil {
		t.Fatal("inspection database accepted a mutation")
	}
}
