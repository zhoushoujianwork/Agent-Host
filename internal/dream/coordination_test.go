package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/state"
)

func coordinationFixture(t *testing.T, suffix string) CoordinationTarget {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	hot := filepath.Join(root, "hot")
	home := filepath.Join(root, "home-"+suffix)
	for _, path := range []string{hot, home} {
		if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	return CoordinationTarget{StateHome: home, ConfigID: "config-" + suffix, ConfigPath: filepath.Join(root, "dream-"+suffix+".json"), HotRoot: hot, PolicyPath: filepath.Join(root, "sharing.json"), Partition: state.DreamPartition{PersonID: "person-" + suffix, AgentID: "agent", AudienceID: "user:person", Scope: "scope"}}
}

// TestWriteViewerCoordinationFixture is opt-in cross-language proof. The Python
// viewer test supplies private temporary roots and reads the real Go ledger.
func TestWriteViewerCoordinationFixture(t *testing.T) {
	home, hot := os.Getenv("DREAM_VIEWER_HOME"), os.Getenv("DREAM_VIEWER_HOT_ROOT")
	if home == "" || hot == "" {
		t.Skip("fixture paths not requested")
	}
	base := filepath.Dir(hot)
	policy := filepath.Join(base, "共享<&>\u2028policy.json")
	first := CoordinationTarget{StateHome: home, ConfigID: "config-one", ConfigPath: filepath.Join(base, "梦<&>\u2028dream.json"), HotRoot: hot, PolicyPath: policy, Partition: state.DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}}
	secondHome := filepath.Join(base, "second-home")
	if err := os.Mkdir(secondHome, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	second := first
	second.StateHome = secondHome
	second.ConfigID = "config-two"
	second.ConfigPath = filepath.Join(base, "second.json")
	second.Partition.PersonID = "person-two"
	for _, target := range []CoordinationTarget{first, second} {
		if _, err := coordinateForTest(context.Background(), target, observed(2, "a", "b"), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("DREAM_VIEWER_FIXTURE_MODE") == "behind" {
		if _, err := coordinateForTest(context.Background(), second, observed(3, "a", "b"), nil, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	reason := "全局撤销 <&>\n行\u2028尾"
	if _, err := RevokeAuthorization(context.Background(), CoordinationTarget{StateHome: second.StateHome, Partition: second.Partition}, "viewer-fixture", reason); err != nil {
		t.Fatal(err)
	}
}

func observed(generation int, policy, binding string) AuthorizationObservation {
	return AuthorizationObservation{Generation: generation, PolicyDigest: strings.Repeat(policy, 64), BindingDigest: strings.Repeat(binding, 64), MountID: "mount", CanonicalPersonID: "person"}
}

func coordinateForTest(ctx context.Context, target CoordinationTarget, observation AuthorizationObservation, prepareErr error, published *[]string) (AuthorizationStatus, error) {
	_, status, err := CoordinateSharedPublish(ctx, target, func() (AuthorizationObservation, SharedHOTBuilder, error) {
		if prepareErr != nil {
			return AuthorizationObservation{}, nil, prepareErr
		}
		return observation, func(authority HOTAuthority) (HOTBundle, error) {
			return HOTBundle{Digest: strings.Repeat("d", 64), Authority: &authority}, nil
		}, nil
	}, func(bundle HOTBundle) error {
		if published != nil {
			*published = append(*published, bundle.Digest)
		}
		return nil
	})
	return status, err
}

func TestCoordinationGlobalHighwaterAndStickyInvalidation(t *testing.T) {
	first := coordinationFixture(t, "one")
	second := first
	second.StateHome = filepath.Join(filepath.Dir(first.StateHome), "home-two")
	if err := os.Mkdir(second.StateHome, 0700); err != nil {
		t.Fatal(err)
	}
	second.ConfigID = "config-two"
	second.ConfigPath = filepath.Join(filepath.Dir(first.ConfigPath), "dream-two.json")
	second.Partition.PersonID = "person-two"
	if _, err := coordinateForTest(context.Background(), first, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinateForTest(context.Background(), second, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinateForTest(context.Background(), first, observed(2, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinateForTest(context.Background(), second, observed(1, "a", "b"), nil, nil); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatal("global generation rollback accepted", err)
	}
	newer, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: first.StateHome, Partition: first.Partition})
	if err != nil || newer.State != "active" || newer.HighestGeneration != 2 {
		t.Fatal("lower generation invalidated newer authorization", newer, err)
	}
	stale, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: second.StateHome, Partition: second.Partition})
	if err != nil || stale.State != "invalid" || !strings.Contains(stale.Reason, "current policy generation") {
		t.Fatal("target behind global generation appeared active", stale, err)
	}
	status, err := coordinateForTest(context.Background(), first, observed(2, "c", "b"), nil, nil)
	if err == nil || status.State != "invalid" {
		t.Fatal("same generation policy change was not invalid", status, err)
	}
	status, err = coordinateForTest(context.Background(), first, observed(2, "a", "b"), nil, nil)
	if err == nil || status.State != "invalid" {
		t.Fatal("old same-generation policy restored invalid authorization", status, err)
	}
	status, err = coordinateForTest(context.Background(), first, observed(3, "a", "b"), nil, nil)
	if err != nil || status.State != "active" || status.HighestGeneration != 3 {
		t.Fatal("higher generation did not reactivate", status, err)
	}
	status, err = coordinateForTest(context.Background(), first, observed(3, "a", "c"), nil, nil)
	if err == nil || status.State != "invalid" {
		t.Fatal("same generation binding change was not persistently invalid", status, err)
	}
	otherStatus, otherErr := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: second.StateHome, Partition: second.Partition})
	if otherErr != nil || otherStatus.State != "invalid" || !strings.Contains(otherStatus.Reason, "target authorization") {
		t.Fatal("target authorization change did not invalidate the global policy", otherStatus, otherErr)
	}
	status, err = coordinateForTest(context.Background(), first, observed(4, "a", "b"), nil, nil)
	if err != nil || status.State != "active" || status.HighestGeneration != 4 {
		t.Fatal("higher generation did not restore target authorization", status, err)
	}
}

func TestCoordinationTransientFailureAndExplicitRevocation(t *testing.T) {
	target := coordinationFixture(t, "transient")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithAuthorizationLock(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition}, func(status AuthorizationStatus) error { called = status.State == "active"; return nil }); err != nil || !called {
		t.Fatal("authorization maintenance lock seam failed", err)
	}
	if _, err := coordinateForTest(context.Background(), target, AuthorizationObservation{}, errors.New("temporary source I/O"), nil); err == nil {
		t.Fatal("temporary source failure was hidden")
	}
	status, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition})
	if err != nil || status.State != "active" {
		t.Fatal("temporary source failure revoked authorization", status, err)
	}
	status, err = RevokeAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition}, "operator", "binding removed")
	if err != nil || status.State != "invalid" {
		t.Fatal(status, err)
	}
	if _, err = coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err == nil {
		t.Fatal("same generation restored revoked authorization")
	}
	status, err = coordinateForTest(context.Background(), target, observed(2, "a", "b"), nil, nil)
	if err != nil || status.State != "active" {
		t.Fatal("higher generation did not restore revoked authorization", status, err)
	}
}

func TestCoordinationPersistsHighwaterBeforeBuilderAndInvalidatesAuthorizationErrors(t *testing.T) {
	target := coordinationFixture(t, "builder-order")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, status, err := CoordinateSharedPublish(context.Background(), target, func() (AuthorizationObservation, SharedHOTBuilder, error) {
		return observed(2, "c", "b"), func(HOTAuthority) (HOTBundle, error) { return HOTBundle{}, errors.New("temporary source I/O") }, nil
	}, func(HOTBundle) error { return nil })
	if err == nil || status.State != "active" || status.HighestGeneration != 2 {
		t.Fatal("builder source failure lost the accepted highwater", status, err)
	}
	if _, err = coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatal("rollback after builder failure was accepted", err)
	}
	if _, err = coordinateForTest(context.Background(), target, observed(2, "c", "b"), nil, nil); err != nil {
		t.Fatal("same generation could not recover after source repair", err)
	}
	_, status, err = CoordinateSharedPublish(context.Background(), target, func() (AuthorizationObservation, SharedHOTBuilder, error) {
		return observed(3, "d", "b"), func(HOTAuthority) (HOTBundle, error) {
			return HOTBundle{}, RevokeAuthorizationError(errors.New("authorization changed during source read"))
		}, nil
	}, func(HOTBundle) error { return nil })
	if err == nil || status.State != "invalid" || status.HighestGeneration != 3 {
		t.Fatal("builder authorization failure was not persisted invalid", status, err)
	}
}

func TestCoordinationIdempotentBundleDoesNotAppendLedgerEvent(t *testing.T) {
	target := coordinationFixture(t, "idempotent")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	root, _ := coordinationRoot(target.HotRoot)
	ledgerRoot := filepath.Join(root, "targets", targetCoordinationKey(target.Partition))
	before, err := loadLedger(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := loadLedger(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sequence != after.Sequence {
		t.Fatal("idempotent shared bundle appended an audit event", before.Sequence, after.Sequence)
	}
}

func TestCoordinationAnchorPreventsRebindAndMissingLedgerReset(t *testing.T) {
	target := coordinationFixture(t, "anchor")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	otherRoot := filepath.Join(filepath.Dir(target.HotRoot), "other-hot")
	if err := os.Mkdir(otherRoot, 0700); err != nil {
		t.Fatal(err)
	}
	rebound := target
	rebound.HotRoot = otherRoot
	if _, err := coordinateForTest(context.Background(), rebound, observed(2, "a", "b"), nil, nil); err == nil || !strings.Contains(err.Error(), "cannot be rebound") {
		t.Fatal("anchor allowed root rebind", err)
	}
	coordRoot, _ := coordinationRoot(target.HotRoot)
	ledger := filepath.Join(coordRoot, "targets", targetCoordinationKey(target.Partition))
	missing := ledger + ".missing"
	if err := os.Rename(ledger, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinateForTest(context.Background(), target, observed(2, "a", "b"), nil, nil); err == nil || !strings.Contains(err.Error(), "missing or unsafe") {
		t.Fatal("missing ledger was reset", err)
	}
}

func TestInvalidateIfPresentDistinguishesMissingAndBrokenAuthorization(t *testing.T) {
	missing := coordinationFixture(t, "missing-anchor")
	found, err := InvalidateAuthorizationIfPresent(context.Background(), missing, "operator", "revoke")
	if err != nil || found {
		t.Fatal("absent anchor was not a clean no-op", found, err)
	}
	target := coordinationFixture(t, "broken-anchor")
	if _, err = coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, lockPath, err := anchorPaths(target.StateHome, target.Partition, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if found, err = InvalidateAuthorizationIfPresent(context.Background(), target, "operator", "revoke"); err == nil || found {
		t.Fatal("initialized anchor with missing lock was ignored", found, err)
	}
	if err = os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	coordinationDir := filepath.Join(target.StateHome, ".dream-coordination")
	if err = os.Chmod(coordinationDir, 0755); err != nil {
		t.Fatal(err)
	}
	if found, err = InvalidateAuthorizationIfPresent(context.Background(), target, "operator", "revoke"); err == nil || found {
		t.Fatal("unsafe anchor directory was treated as absent", found, err)
	}
	if err = InvalidateAuthorizationsForConfig(context.Background(), target.StateHome, target.ConfigPath, "operator", "revoke"); err == nil {
		t.Fatal("config invalidation ignored unsafe anchor directory")
	}
	ledgerMissing := coordinationFixture(t, "missing-ledger")
	if _, err = coordinateForTest(context.Background(), ledgerMissing, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	coordRoot, _ := coordinationRoot(ledgerMissing.HotRoot)
	targetLedger := filepath.Join(coordRoot, "targets", targetCoordinationKey(ledgerMissing.Partition))
	if err = os.Rename(targetLedger, targetLedger+".missing"); err != nil {
		t.Fatal(err)
	}
	if found, err = InvalidateAuthorizationIfPresent(context.Background(), ledgerMissing, "operator", "revoke"); err == nil || found {
		t.Fatal("initialized anchor with missing ledger was ignored", found, err)
	}
}

func TestCoordinationRecoversDurableEventWithoutWritingHead(t *testing.T) {
	target := coordinationFixture(t, "recovery")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	coordRoot, _ := coordinationRoot(target.HotRoot)
	root := filepath.Join(coordRoot, "targets", targetCoordinationKey(target.Partition))
	ledger, err := loadLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	stateValue, err := decodeTargetState(ledger)
	if err != nil {
		t.Fatal(err)
	}
	stateValue.State = "invalid"
	stateValue.Reason = "durable revoke"
	raw, _ := jsonMarshal(stateValue)
	hash := hashRawState(raw)
	event := ledgerEvent{Version: coordinationVersion, Sequence: ledger.Sequence + 1, PreviousHash: ledger.Hash, StateHash: hash, Actor: "operator", Reason: "durable revoke", At: time.Now().UTC(), State: raw}
	if err = writeImmutableJSON(filepath.Join(root, "events", fmtEvent(event.Sequence)), event); err != nil {
		t.Fatal(err)
	}
	headBefore, err := os.ReadFile(filepath.Join(root, "head.json"))
	if err != nil {
		t.Fatal(err)
	}
	status, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition})
	if err != nil || status.State != "invalid" || !status.NeedsRecovery {
		t.Fatal("durable event was not recovered read-only", status, err)
	}
	headAfter, _ := os.ReadFile(filepath.Join(root, "head.json"))
	if string(headBefore) != string(headAfter) {
		t.Fatal("status read rewrote ledger head")
	}
	status, err = RevokeAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition}, "operator", "durable revoke")
	if err != nil || status.NeedsRecovery {
		t.Fatal("write path did not recover lagging target head", status, err)
	}
	recovered, err := loadLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Sequence != ledger.Sequence+1 || recovered.NeedsRecovery {
		t.Fatal("recovery appended another target event or left head stale", recovered.Sequence, recovered.NeedsRecovery)
	}
	policyRoot := filepath.Join(coordRoot, "policies", policyCoordinationKey(target.PolicyPath))
	policyBefore, err := loadLedger(policyRoot)
	if err != nil {
		t.Fatal(err)
	}
	status, err = RevokeAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition}, "operator", "durable revoke")
	if err != nil || status.NeedsRecovery {
		t.Fatal(status, err)
	}
	policyAfter, _ := loadLedger(policyRoot)
	targetAfter, _ := loadLedger(root)
	if policyAfter.Sequence != policyBefore.Sequence || targetAfter.Sequence != recovered.Sequence {
		t.Fatal("idempotent revoke appended audit events")
	}
}

func TestCoordinationContextCancellationAndConcurrentOrdering(t *testing.T) {
	target := coordinationFixture(t, "concurrent")
	started := make(chan struct{})
	release := make(chan struct{})
	var published []string
	var mu sync.Mutex
	oldDone := make(chan error, 1)
	go func() {
		_, _, err := CoordinateSharedPublish(context.Background(), target, func() (AuthorizationObservation, SharedHOTBuilder, error) {
			close(started)
			<-release
			return observed(1, "a", "b"), func(a HOTAuthority) (HOTBundle, error) {
				return HOTBundle{Digest: strings.Repeat("1", 64), Authority: &a}, nil
			}, nil
		}, func(b HOTBundle) error { mu.Lock(); published = append(published, b.Digest); mu.Unlock(); return nil })
		oldDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinateForTest(ctx, target, observed(2, "c", "b"), nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("lock wait ignored cancellation", err)
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if _, err := coordinateForTest(context.Background(), target, observed(2, "c", "b"), nil, &published); err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || published[0] != strings.Repeat("1", 64) || published[1] != strings.Repeat("d", 64) {
		t.Fatal("older coordinated request overwrote newer publication", published)
	}
	status, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition})
	if err != nil || status.HighestGeneration != 2 {
		t.Fatal(status, err)
	}
}

func TestCoordinationRejectsUnsafeLocksAndCorruptLedger(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "lock")
			if err := os.WriteFile(target, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "lock")
			if err := os.WriteFile(target, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"public", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := coordinationFixture(t, test.name)
			_, lock, err := anchorPaths(target.StateHome, target.Partition, true)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, lock)
			if _, err = coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err == nil {
				t.Fatal("unsafe coordination lock accepted")
			}
		})
	}
	target := coordinationFixture(t, "corrupt")
	if _, err := coordinateForTest(context.Background(), target, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	root, _ := coordinationRoot(target.HotRoot)
	head := filepath.Join(root, "targets", targetCoordinationKey(target.Partition), "head.json")
	if err := os.WriteFile(head, []byte(`{"version":"broken"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuthorization(context.Background(), CoordinationTarget{StateHome: target.StateHome, Partition: target.Partition}); err == nil {
		t.Fatal("corrupt coordination ledger was accepted")
	}
}

func TestCoordinationRejectsForgedHeadAndHistoricalIdentity(t *testing.T) {
	forged := coordinationFixture(t, "forged-head")
	if _, err := coordinateForTest(context.Background(), forged, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	coordRoot, _ := coordinationRoot(forged.HotRoot)
	targetRoot := filepath.Join(coordRoot, "targets", targetCoordinationKey(forged.Partition))
	ledger, err := loadLedger(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	var fake targetAuthorizationState
	if err = strictJSON(ledger.History[0].State, &fake); err != nil {
		t.Fatal(err)
	}
	fake.LastCommittedHOTDigest = strings.Repeat("e", 64)
	fakeRaw, _ := json.Marshal(fake)
	if err = writeAtomicJSON(targetRoot, "head.json", ledgerHead{Version: coordinationVersion, Sequence: 1, StateHash: hashRawState(fakeRaw), State: fakeRaw}); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuthorization(context.Background(), CoordinationTarget{StateHome: forged.StateHome, Partition: forged.Partition}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("self-consistent forged old head was accepted", err)
	}

	historical := coordinationFixture(t, "historical-target")
	if _, err = coordinateForTest(context.Background(), historical, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	coordRoot, _ = coordinationRoot(historical.HotRoot)
	targetRoot = filepath.Join(coordRoot, "targets", targetCoordinationKey(historical.Partition))
	eventOne := readLedgerEventForTest(t, targetRoot, 1)
	eventTwo := readLedgerEventForTest(t, targetRoot, 2)
	var wrong targetAuthorizationState
	if err = strictJSON(eventOne.State, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong.StateHome = filepath.Join(filepath.Dir(wrong.StateHome), "wrong-home")
	eventOne.State, _ = json.Marshal(wrong)
	eventOne.StateHash = hashRawState(eventOne.State)
	eventTwo.PreviousHash = eventOne.StateHash
	writeLedgerEventForTest(t, targetRoot, eventOne)
	writeLedgerEventForTest(t, targetRoot, eventTwo)
	if _, err = ReadAuthorization(context.Background(), CoordinationTarget{StateHome: historical.StateHome, Partition: historical.Partition}); err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatal("wrong historical target identity was accepted", err)
	}

	policyHistory := coordinationFixture(t, "historical-policy")
	if _, err = coordinateForTest(context.Background(), policyHistory, observed(1, "a", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinateForTest(context.Background(), policyHistory, observed(2, "c", "b"), nil, nil); err != nil {
		t.Fatal(err)
	}
	coordRoot, _ = coordinationRoot(policyHistory.HotRoot)
	policyRoot := filepath.Join(coordRoot, "policies", policyCoordinationKey(policyHistory.PolicyPath))
	policyOne := readLedgerEventForTest(t, policyRoot, 1)
	policyTwo := readLedgerEventForTest(t, policyRoot, 2)
	var wrongPolicy policyAuthorizationState
	if err = strictJSON(policyOne.State, &wrongPolicy); err != nil {
		t.Fatal(err)
	}
	wrongPolicy.PolicyPath = filepath.Join(filepath.Dir(wrongPolicy.PolicyPath), "other.json")
	policyOne.State, _ = json.Marshal(wrongPolicy)
	policyOne.StateHash = hashRawState(policyOne.State)
	policyTwo.PreviousHash = policyOne.StateHash
	writeLedgerEventForTest(t, policyRoot, policyOne)
	writeLedgerEventForTest(t, policyRoot, policyTwo)
	if _, err = ReadAuthorization(context.Background(), CoordinationTarget{StateHome: policyHistory.StateHome, Partition: policyHistory.Partition}); err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatal("wrong historical policy identity was accepted", err)
	}
}

func readLedgerEventForTest(t *testing.T, root string, sequence uint64) ledgerEvent {
	t.Helper()
	raw, err := readPrivateRegular(filepath.Join(root, "events", fmtEvent(sequence)), coordinationFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	var event ledgerEvent
	if err = strictJSON(raw, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func writeLedgerEventForTest(t *testing.T, root string, event ledgerEvent) {
	t.Helper()
	raw, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err = os.WriteFile(filepath.Join(root, "events", fmtEvent(event.Sequence)), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
func fmtEvent(sequence uint64) string       { return fmt.Sprintf("%020d.json", sequence) }
