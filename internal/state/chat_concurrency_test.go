//go:build darwin || linux

package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestChatLocksShareHomeButProtectSessionsAndMaintenance(t *testing.T) {
	home := t.TempDir()
	first, err := AcquireChat(home)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := AcquireChat(home)
	if err != nil {
		t.Fatal("independent CLI blocked", err)
	}
	defer second()
	if _, err := Acquire(home); err == nil {
		t.Fatal("exclusive maintenance overlapped active chats")
	}
	if active, err := HostActive(home); err != nil || !active {
		t.Fatal("shared owners were invisible to inspection", active, err)
	}
	lease, err := AcquireChatSession(home, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	defer lease()
	if _, err := AcquireChatSession(home, "same-session"); !errors.Is(err, ErrSessionBusy) {
		t.Fatal("duplicate native session accepted", err)
	}
	other, err := AcquireChatSession(home, "other-session")
	if err != nil {
		t.Fatal("independent session blocked", err)
	}
	other()
	first()
	second()
	exclusive, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive()
	if _, err := AcquireChat(home); err == nil {
		t.Fatal("chat bypassed an older or exclusive host")
	}
}

func TestChatInitializationWaitCanCancel(t *testing.T) {
	home := t.TempDir()
	release, err := AcquireChatInitialization(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := AcquireChatInitialization(ctx, home); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestChatRecoveryPreservesLiveOwnerAndNeverReplaysAbandonedRun(t *testing.T) {
	const helper = "AGENT_HOST_DEAD_CHAT_HOME"
	if value := os.Getenv(helper); value != "" {
		if _, err := AcquireChatSession(value, "session-dead"); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	s, home := testStore(t)
	live := addRun(t, s, "live", "scope", "active request", false)
	dead := addRun(t, s, "dead", "scope", "abandoned request", false)
	lease, err := AcquireChatSession(home, live.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChatRecoveryPreservesLiveOwnerAndNeverReplaysAbandonedRun$")
	cmd.Env = append(os.Environ(), helper+"="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crashed owner fixture: %s %v", out, err)
	}
	if err := s.RecoverIdleChatRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := s.Run(t.Context(), live.ID)
	if err != nil || current.ExecutionStatus != "running" {
		t.Fatal("peer startup interrupted active execution", current, err)
	}
	abandoned, err := s.Run(t.Context(), dead.ID)
	if err != nil || abandoned.ExecutionStatus != "unknown" {
		t.Fatal("abandoned execution was not preserved as unknown", abandoned, err)
	}
	if err := s.SessionCanResume(t.Context(), dead.SessionID); err == nil {
		t.Fatal("abandoned native work became replayable")
	}
}
