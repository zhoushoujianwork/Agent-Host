package state

import (
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func approvalFixture() OperationApproval {
	now := time.Now().UTC()
	return OperationApproval{
		ID: "approval-opaque", OwnerID: "owner", Operation: "delete local project", Target: "/example/project", Impact: "Removes local files", Workspace: "/example",
		Status: "sending", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Source: ChannelMessage{ID: "member-receipt", Channel: "feishu", AccountID: "bot", TenantID: "tenant", ConversationID: "group", SenderID: "member", Text: "delete project"},
	}
}

func approvalReply(a OperationApproval) ChannelMessage {
	m := feishuChannelInput("owner-receipt", "2")
	m.Channel, m.AccountID, m.TenantID, m.ConversationID, m.SenderID = a.Source.Channel, a.Source.AccountID, a.Source.TenantID, a.Source.ConversationID, a.OwnerID
	m.ReplyTarget.ID = m.ConversationID
	m.ReplyToID, m.Text = "delivered-message", "OK"
	return m
}

func persistApprovalReply(t *testing.T, s *Store, m ChannelMessage) {
	t.Helper()
	if ok, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !ok {
		t.Fatalf("persist owner receipt: %v %v", ok, err)
	}
}

func pendingApproval(t *testing.T, s *Store) OperationApproval {
	t.Helper()
	a := approvalFixture()
	created, err := s.CreateOperationApproval(testContext, a)
	if err != nil || !created {
		t.Fatalf("create approval: %v %v", created, err)
	}
	if err := s.RecordApprovalDelivery(testContext, a.ID, "delivered-message", "pending", ""); err != nil {
		t.Fatal(err)
	}
	a, err = s.OperationApproval(testContext, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	persistApprovalReply(t, s, approvalReply(a))
	return a
}

func TestOperationApprovalDurableLifecycle(t *testing.T) {
	home := t.TempDir()
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	a := pendingApproval(t, s)
	if created, err := s.CreateOperationApproval(testContext, approvalFixture()); err != nil || created {
		t.Fatalf("duplicate: %v %v", created, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.OperationApproval(testContext, a.ID)
	if err != nil || stored.Status != "pending" || stored.Target != a.Target || stored.OwnerID != a.OwnerID {
		t.Fatalf("durable proposal: %#v %v", stored, err)
	}
	reply := approvalReply(a)
	grant, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, true, a.CreatedAt.Add(time.Minute))
	if err != nil || grant.Status != "consumed" || grant.ApprovalReceiptID != reply.ID || grant.RunID != "" {
		t.Fatalf("consume: %#v %v", grant, err)
	}
	if err := s.FinishOperationApproval(testContext, a.ID, "authorized-run", "execution failed"); err != nil {
		t.Fatal(err)
	}
	finished, err := s.OperationApproval(testContext, a.ID)
	if err != nil || finished.RunID != "authorized-run" || finished.Status != "consumed" || finished.Error != "execution failed" {
		t.Fatalf("execution evidence: %#v %v", finished, err)
	}
	if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, true, a.CreatedAt.Add(2*time.Minute)); err == nil {
		t.Fatal("replayed consumed grant")
	}
}

func TestOperationApprovalScopeIsolation(t *testing.T) {
	s, _ := testStore(t)
	a := pendingApproval(t, s)
	for _, tc := range []struct {
		name   string
		mutate func(*ChannelMessage)
	}{
		{"channel", func(m *ChannelMessage) { m.Channel = "dingtalk" }},
		{"account", func(m *ChannelMessage) { m.AccountID = "other-bot" }},
		{"tenant", func(m *ChannelMessage) { m.TenantID = "other-tenant" }},
		{"conversation", func(m *ChannelMessage) { m.ConversationID = "other-group" }},
		{"nonowner", func(m *ChannelMessage) { m.SenderID = "member" }},
		{"missing reply", func(m *ChannelMessage) { m.ReplyToID = "" }},
		{"wrong reply", func(m *ChannelMessage) { m.ReplyToID = "different-message" }},
		{"missing receipt", func(m *ChannelMessage) { m.ID = "" }},
		{"unpersisted receipt", func(m *ChannelMessage) { m.ID = "forged-receipt" }},
		{"missing provider message", func(m *ChannelMessage) { m.MessageID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := approvalReply(a)
			tc.mutate(&reply)
			if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, true, a.CreatedAt.Add(time.Minute)); err == nil {
				t.Fatal("forged owner reply consumed grant")
			}
			stored, err := s.OperationApproval(testContext, a.ID)
			if err != nil || stored.Status != "pending" {
				t.Fatalf("failed authorization changed approval: %#v %v", stored, err)
			}
		})
	}
	if _, err := s.ConsumeOperationApproval(testContext, a.ID, "new-owner", approvalReply(a), true, a.CreatedAt.Add(time.Minute)); err == nil {
		t.Fatal("old owner grant survived owner rebound")
	}
}

func TestFindOperationApprovalCorrelation(t *testing.T) {
	s, _ := testStore(t)
	a := pendingApproval(t, s)
	reply := approvalReply(a)
	if got, err := s.FindOperationApproval(testContext, reply, ""); err != nil || got.ID != a.ID {
		t.Fatalf("direct reply: %#v %v", got, err)
	}
	if _, err := s.FindOperationApproval(testContext, reply, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Feishu token bypass accepted: %v", err)
	}
	for _, mutate := range []func(*ChannelMessage){
		func(m *ChannelMessage) { m.Channel = "other" }, func(m *ChannelMessage) { m.AccountID = "other" },
		func(m *ChannelMessage) { m.TenantID = "other" }, func(m *ChannelMessage) { m.ConversationID = "other" },
		func(m *ChannelMessage) { m.ReplyToID = "" }, func(m *ChannelMessage) { m.ReplyToID = "wrong" },
	} {
		candidate := reply
		mutate(&candidate)
		if _, err := s.FindOperationApproval(testContext, candidate, ""); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("mismatched lookup: %v", err)
		}
	}
	other := approvalFixture()
	other.ID = "dingtalk-code"
	other.Source.Channel = "dingtalk"
	if ok, err := s.CreateOperationApproval(testContext, other); !ok || err != nil {
		t.Fatalf("create DingTalk approval: %v %v", ok, err)
	}
	if err := s.RecordApprovalDelivery(testContext, other.ID, "dingtalk-delivery", "pending", ""); err != nil {
		t.Fatal(err)
	}
	dingReply := approvalReply(other)
	dingReply.ReplyToID = ""
	dingReply.Text = "OK " + other.ID
	if got, err := s.FindOperationApproval(testContext, dingReply, other.ID); err != nil || got.ID != other.ID {
		t.Fatalf("DingTalk code: %#v %v", got, err)
	}
}

func TestOperationApprovalExpiryAndRejection(t *testing.T) {
	for _, mode := range []string{"expired", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			a := pendingApproval(t, s)
			now, approve := a.CreatedAt.Add(time.Minute), false
			reply := approvalReply(a)
			if mode == "expired" {
				now, approve = a.ExpiresAt, true
			} else {
				reply = feishuChannelInput("owner-rejection", "2")
				reply.AccountID, reply.TenantID, reply.ConversationID, reply.SenderID = a.Source.AccountID, a.Source.TenantID, a.Source.ConversationID, a.OwnerID
				reply.ReplyTarget.ID, reply.ReplyToID, reply.Text = reply.ConversationID, "delivered-message", "No"
				persistApprovalReply(t, s, reply)
			}
			got, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, approve, now)
			if got.Status != mode || (mode == "expired" && err == nil) || (mode == "rejected" && err != nil) {
				t.Fatalf("terminal decision: %#v %v", got, err)
			}
			if got.RunID != "" {
				t.Fatal("decision incorrectly records execution")
			}
			if err := s.FinishOperationApproval(testContext, a.ID, "forged-run", ""); err == nil {
				t.Fatal("executed unapproved request")
			}
			if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, approvalReply(a), true, now); err == nil {
				t.Fatal("terminal decision replayed")
			}
		})
	}
}

func TestOperationApprovalUndeliveredCannotApprove(t *testing.T) {
	for _, status := range []string{"sending", "unknown", "failed"} {
		t.Run(status, func(t *testing.T) {
			s, _ := testStore(t)
			a := approvalFixture()
			if ok, err := s.CreateOperationApproval(testContext, a); !ok || err != nil {
				t.Fatal(err)
			}
			if status != "sending" {
				if err := s.RecordApprovalDelivery(testContext, a.ID, "delivered-message", status, "transport failure"); err != nil {
					t.Fatal(err)
				}
			}
			persistApprovalReply(t, s, approvalReply(a))
			if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, approvalReply(a), true, a.CreatedAt.Add(time.Minute)); err == nil {
				t.Fatal("undelivered approval consumed")
			}
		})
	}
}

func TestOperationApprovalConcurrentConsumption(t *testing.T) {
	s, _ := testStore(t)
	a := pendingApproval(t, s)
	var succeeded atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		wg.Go(func() {
			<-start
			if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, approvalReply(a), true, a.CreatedAt.Add(time.Minute)); err == nil {
				succeeded.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if succeeded.Load() != 1 {
		t.Fatalf("expected one grant consumption, got %d", succeeded.Load())
	}
}

func TestOperationApprovalDingTalkDecisionToken(t *testing.T) {
	for _, text := range []string{"OK approval-opaque", "@bot Approve approval-opaque", "OK wrong-code", "OK", "No approval-opaque", "OK approval-opaque trailing"} {
		t.Run(text, func(t *testing.T) {
			s, _ := testStore(t)
			a := approvalFixture()
			a.Source.Channel = "dingtalk"
			if ok, err := s.CreateOperationApproval(testContext, a); !ok || err != nil {
				t.Fatal(err)
			}
			if err := s.RecordApprovalDelivery(testContext, a.ID, "delivered-message", "pending", ""); err != nil {
				t.Fatal(err)
			}
			reply := channelInput("ding-owner-reply")
			reply.AccountID, reply.TenantID, reply.ConversationID, reply.SenderID = a.Source.AccountID, a.Source.TenantID, a.Source.ConversationID, a.OwnerID
			reply.Text = text
			if ok, err := s.EnqueueChannelMessage(testContext, reply, testReplyURL); !ok || err != nil {
				t.Fatal(err)
			}
			got, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, true, a.CreatedAt.Add(time.Minute))
			valid := text == "OK approval-opaque" || text == "@bot Approve approval-opaque"
			if valid && (err != nil || got.Status != "consumed") {
				t.Fatalf("valid token rejected: %v", err)
			}
			if !valid && err == nil {
				t.Fatal("wrong token or mismatched decision granted approval")
			}
		})
	}
}

func TestOperationApprovalRefresh(t *testing.T) {
	for _, mode := range []string{"owner changed", "expired", "other channel", "other account"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			a := pendingApproval(t, s)
			channel, account, owner, now := a.Source.Channel, a.Source.AccountID, a.OwnerID, a.CreatedAt.Add(time.Minute)
			want := "pending"
			switch mode {
			case "owner changed":
				owner, want = "new-owner", "canceled"
			case "expired":
				now, want = a.ExpiresAt, "expired"
			case "other channel":
				channel, owner = "other-channel", "new-owner"
			case "other account":
				account, owner = "other-account", "new-owner"
			}
			if err := s.RefreshOperationApprovals(testContext, channel, account, owner, now); err != nil {
				t.Fatal(err)
			}
			got, err := s.OperationApproval(testContext, a.ID)
			if err != nil || got.Status != want || got.RunID != "" {
				t.Fatalf("refresh: %#v %v", got, err)
			}
			if mode == "owner changed" {
				if err := s.RefreshOperationApprovals(testContext, channel, account, a.OwnerID, now); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, approvalReply(a), true, now); err == nil {
					t.Fatal("restoring old owner revived canceled approval")
				}
			}
		})
	}
}

func TestOperationApprovalBindsAtMostOneExecutionRun(t *testing.T) {
	s, _ := testStore(t)
	a := pendingApproval(t, s)
	reply := approvalReply(a)
	if _, err := s.ConsumeOperationApproval(testContext, a.ID, a.OwnerID, reply, true, a.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.BindOperationApprovalRun(testContext, a.ID, "wrong-receipt", "wrong-run"); err == nil {
		t.Fatal("unrelated receipt bound execution")
	}
	var winners atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.BindOperationApprovalRun(testContext, a.ID, reply.ID, "authorized-run"); err == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("execution grant bound more than once", winners.Load())
	}
	got, err := s.OperationApproval(testContext, a.ID)
	if err != nil || got.RunID != "authorized-run" {
		t.Fatal("run grant not durable", got, err)
	}
}
