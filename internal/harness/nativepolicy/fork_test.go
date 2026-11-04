//go:build unix

package nativepolicy

import (
	"strings"
	"testing"
)

func TestIsolatedAdapterFailsClosedForFork(t *testing.T) {
	adapter, req := fixtureAdapter(t, "success")
	if adapter.SupportsFork() {
		t.Fatal("isolated adapter advertised fork support without a parent environment binding")
	}
	req.ForkFromNativeSessionID = "parent-session"
	result, err := adapter.Run(t.Context(), req, nil)
	if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "parent Host environment") {
		t.Fatalf("fork did not fail closed: result=%+v err=%v", result, err)
	}
}

func TestLivePrintForkPreservesHistoryAndAssignedChildID(t *testing.T) {
	t.Skip("isolated native fork requires a future explicit parent-environment binding")
}
