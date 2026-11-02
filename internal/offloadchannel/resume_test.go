package offloadchannel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
)

type canceledExecutor struct{ cleanup, session string }

func (c canceledExecutor) Run(_ context.Context, req offloadexec.Request) (offloadexec.Result, error) {
	raw, _ := json.Marshal(map[string]any{"caller": "agh", "caller_run_id": req.CallerRunID, "session": c.session, "error_kind": "canceled", "cleanup": c.cleanup})
	return offloadexec.Result{Status: "canceled", ErrorKind: offloadexec.KindCanceled, Session: c.session, Profile: "canon", Raw: raw}, &harness.ExecutionError{Kind: harness.FailureCanceled, Cause: context.Canceled}
}

func TestExplicitCanceledResumePreservesBindingAndNormalGate(t *testing.T) {
	f := newFixture(t)
	f.host.Client = canceledExecutor{session: "real-native"}
	s, err := f.host.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.host.Run(t.Context(), s, "inspect")
	if err == nil || first.ExecutionStatus != "canceled" || first.NativeSessionID != "real-native" {
		t.Fatalf("%+v %v", first, err)
	}
	if _, err := f.host.Session(t.Context(), s.ID); err == nil {
		t.Fatal("automatic canceled resume accepted")
	}
	f.host.Config.ResumeCanceledRun = first.ID
	if _, err := f.host.Session(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	f.host.Config.ResumeCanceledRun = "wrong-run"
	if _, err := f.host.Session(t.Context(), s.ID); err == nil {
		t.Fatal("wrong explicit source accepted")
	}
	old, err := f.host.store().Run(t.Context(), first.ID)
	if err != nil || old.ExecutionStatus != "canceled" || old.AcceptanceStatus != "unverified" {
		t.Fatalf("original outcome changed %+v %v", old, err)
	}
}

func TestCanceledResumeRequiresSessionAndConfirmedCleanup(t *testing.T) {
	for _, c := range []canceledExecutor{{session: "", cleanup: ""}, {session: "native", cleanup: "unresolved"}} {
		t.Run(c.cleanup+c.session, func(t *testing.T) {
			f := newFixture(t)
			f.host.Client = c
			s, _ := f.host.Session(t.Context(), "")
			first, err := f.host.Run(t.Context(), s, "inspect")
			if err == nil {
				t.Fatal("cancel unexpectedly succeeded")
			}
			f.host.Config.ResumeCanceledRun = first.ID
			if _, err := f.host.Session(t.Context(), s.ID); err == nil {
				t.Fatal("unsafe canceled resume accepted")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
