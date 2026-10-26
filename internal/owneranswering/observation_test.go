package owneranswering

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

// Synthetic provider metadata is deliberately outside the attributed message
// contract. Reading/viewing changes neither message content nor Host processing.
func withReadMetadata(t *testing.T, e state.DiscoveryEvidence, read bool) state.DiscoveryEvidence {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["read"] = read
	fields["unread"] = !read
	fields["owner_viewed"] = read
	fields["read_receipt"] = map[string]any{"owner": read}
	raw, _ = json.Marshal(fields)
	var result state.DiscoveryEvidence
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return state.PrepareDiscoveryEvidence(result)
}

func TestReadIncomingIsHandledAndReadChangesNeverDuplicate(t *testing.T) {
	for _, initialRead := range []bool{true, false} {
		t.Run(map[bool]string{true: "already-read", false: "unread"}[initialRead], func(t *testing.T) {
			f := setup(t)
			f.e[0] = withReadMetadata(t, f.e[0], initialRead)
			f.consume(t)
			_, q := f.question(t)
			if f.handled != 1 || f.sent != 1 || q.OwnerTakeover || len(q.OwnerActivity) != 0 {
				t.Fatal("owner viewing suppressed or took over incoming request", q)
			}
			f.e[0] = withReadMetadata(t, f.e[0], !initialRead)
			if err := f.c.Activate(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.consume(t)
			if f.handled != 1 || f.sent != 1 || f.notified != 0 {
				t.Fatal("read metadata change replayed work")
			}
			records, err := f.c.Store.OwnerAnsweringRecords(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			observations := 0
			for _, r := range records {
				if strings.HasPrefix(r.Key, "message:") {
					observations++
				}
			}
			if observations != 1 {
				t.Fatal("message observation not durable", observations)
			}
		})
	}
}

func TestOwnerOutgoingIsManualActivityNotHostExecution(t *testing.T) {
	f := setup(t)
	owner := f.message("owner-outgoing", "I will look at it", "owner-observed", "person")
	owner.CreatedAt = f.now.Add(-500 * time.Millisecond)
	owner = state.PrepareDiscoveryEvidence(owner)
	f.e = append(f.e, owner)
	f.consume(t)
	_, q := f.question(t)
	if q.Status != "taken_over" || !q.OwnerTakeover || len(q.OwnerActivity) != 1 || q.OwnerActivity[0].MessageID != owner.MessageID || q.OwnerActivity[0].Type != "outgoing_message" || !q.OwnerActivity[0].At.Equal(owner.CreatedAt) {
		t.Fatal("actual manual activity not retained", q)
	}
	if q.Attempted || q.RunID != "" || q.HandlerOutcome.Outcome != "" || q.Peer.Status != "" || f.handled != 0 || f.sent != 0 {
		t.Fatal("manual reply manufactured Host completion", q)
	}
}

func TestFreshOwnerTakeoverPreservesActualHostOutcome(t *testing.T) {
	f := setup(t)
	f.c.Handle = func(context.Context, Question) (Outcome, error) {
		f.handled++
		owner := f.message("manual-during-tools", "I'll take over", "owner-observed", "person")
		owner.CreatedAt = f.now.Add(-500 * time.Millisecond)
		f.e = append(f.e, state.PrepareDiscoveryEvidence(owner))
		return Outcome{Outcome: "answer", Text: "Actual tool result", RunID: "actual-run"}, nil
	}
	f.consume(t)
	_, q := f.question(t)
	if q.Status != "taken_over" || !q.Attempted || q.RunID != "actual-run" || q.HandlerOutcome.Text != "Actual tool result" || q.Peer.Status != "" || f.handled != 1 || f.sent != 0 || len(q.OwnerActivity) != 1 || q.OwnerActivity[0].MessageID != "manual-during-tools" {
		t.Fatal("takeover lost independent execution/manual evidence", q)
	}
}

func TestLatePostActivationMessageOlderThanThroughIntakesOnce(t *testing.T) {
	f := setup(t)
	first := f.e[0]
	f.e = nil
	f.consume(t) // complete empty window advances Through
	sourceKey := "source:" + f.s.Attribution(f.c.Config).ID
	sr, err := f.c.Store.OwnerAnsweringRecord(t.Context(), sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	var before sourceState
	json.Unmarshal(sr.Payload, &before)
	if !first.CreatedAt.Before(before.Through) || first.CreatedAt.Before(before.ActivationBaseline) {
		t.Fatal("bad late-message fixture")
	}
	f.e = []state.DiscoveryEvidence{first}
	f.now = f.now.Add(2 * time.Second)
	f.consume(t)
	if f.handled != 1 || f.sent != 1 {
		t.Fatal("unseen late message dropped by collection watermark")
	}
	if err = f.c.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.consume(t)
	if f.handled != 1 || f.sent != 1 {
		t.Fatal("late message replayed after restart")
	}
	sr, err = f.c.Store.OwnerAnsweringRecord(t.Context(), sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	var after sourceState
	json.Unmarshal(sr.Payload, &after)
	if !after.ActivationBaseline.Equal(before.ActivationBaseline) || !after.Through.After(before.Through) {
		t.Fatal("baseline moved with collection watermark", before, after)
	}
}

func TestPreviouslyObservedOwnerOutgoingStillCancelsLateQuestion(t *testing.T) {
	f := setup(t)
	peer := f.e[0]
	owner := f.message("manual-earlier-poll", "I am handling it", "owner-observed", "person")
	owner.CreatedAt = f.now.Add(-500 * time.Millisecond)
	owner = state.PrepareDiscoveryEvidence(owner)
	f.e = []state.DiscoveryEvidence{owner}
	f.consume(t)
	f.now = f.now.Add(2 * time.Second)
	f.e = []state.DiscoveryEvidence{peer, owner}
	f.consume(t)
	_, q := f.question(t)
	if q.Status != "taken_over" || len(q.OwnerActivity) != 1 || q.Attempted || f.handled != 0 || f.sent != 0 {
		t.Fatal("observed owner message incorrectly suppressed for takeover", q)
	}
}

func TestLegacySourceBaselineMigratesConservatively(t *testing.T) {
	f := setup(t)
	key := "source:" + f.s.Attribution(f.c.Config).ID
	r, err := f.c.Store.OwnerAnsweringRecord(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	oldThrough := f.now
	r.Payload, _ = json.Marshal(sourceState{Policy: f.c.policy(), Through: oldThrough})
	if err = f.c.Store.CompareAndSaveOwnerAnswering(t.Context(), &r); err != nil {
		t.Fatal(err)
	}
	if err = f.c.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.consume(t)
	if f.handled != 0 || f.sent != 0 {
		t.Fatal("legacy baseline reopened historical messages")
	}
	r, err = f.c.Store.OwnerAnsweringRecord(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	var st sourceState
	json.Unmarshal(r.Payload, &st)
	if !st.ActivationBaseline.Equal(oldThrough) {
		t.Fatal("legacy baseline not pinned to prior Through", st)
	}
}

func TestObservationAndActiveIntakeCommittedBeforeTools(t *testing.T) {
	f := setup(t)
	original := f.c.Handle
	f.c.Handle = func(ctx context.Context, q Question) (Outcome, error) {
		key := "message:" + hash([]string{f.s.Attribution(f.c.Config).ID, q.Evidence[0].MessageID})
		r, err := f.c.Store.OwnerAnsweringRecord(ctx, key)
		var seen messageObservation
		json.Unmarshal(r.Payload, &seen)
		if err != nil || seen.Digest != q.Evidence[0].Digest {
			t.Fatal("tools started without committed message observation", err, seen)
		}
		return original(ctx, q)
	}
	f.consume(t)
	if f.handled != 1 {
		t.Fatal("handling did not execute")
	}
}
