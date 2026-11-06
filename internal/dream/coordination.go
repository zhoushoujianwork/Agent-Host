package dream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"agent-host/internal/state"
)

const (
	coordinationVersion     = "dream-coordination-v1"
	coordinationFileLimit   = 512 << 10
	coordinationEventLimit  = 4096
	coordinationLedgerLimit = 32 << 20
)

// HOTAuthority identifies the durable authorization revision that admitted a
// shared projection. It is evidence, not a capability that can be replayed.
type HOTAuthority struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

type CoordinationTarget struct {
	StateHome  string
	ConfigID   string
	ConfigPath string
	HotRoot    string
	PolicyPath string
	Partition  state.DreamPartition
}

type AuthorizationObservation struct {
	Generation        int
	PolicyDigest      string
	BindingDigest     string
	MountID           string
	CanonicalPersonID string
	LocalTarget       bool
}

type AuthorizationStatus struct {
	TargetKey              string               `json:"target_key"`
	PolicyKey              string               `json:"policy_key"`
	State                  string               `json:"state"`
	Reason                 string               `json:"reason,omitempty"`
	HighestGeneration      int                  `json:"highest_generation"`
	PolicyDigest           string               `json:"policy_digest,omitempty"`
	BindingDigest          string               `json:"binding_digest,omitempty"`
	MountID                string               `json:"mount_id,omitempty"`
	CanonicalPersonID      string               `json:"canonical_person_id,omitempty"`
	LocalTarget            bool                 `json:"local_target,omitempty"`
	AuthorizationRevision  uint64               `json:"authorization_revision"`
	LastCommittedHOTDigest string               `json:"last_committed_hot_digest,omitempty"`
	NeedsRecovery          bool                 `json:"needs_recovery,omitempty"`
	Partition              state.DreamPartition `json:"partition"`
}

type SharedHOTBuilder func(HOTAuthority) (HOTBundle, error)

// AuthorizationFailure marks an authorization failure that must durably
// invalidate the policy and target. Source snapshot failures must remain plain
// errors so transient source I/O cannot revoke authorization.
type AuthorizationFailure struct{ Err error }

func (e AuthorizationFailure) Error() string { return e.Err.Error() }
func (e AuthorizationFailure) Unwrap() error { return e.Err }

func RevokeAuthorizationError(err error) error {
	if err == nil {
		return nil
	}
	return AuthorizationFailure{Err: err}
}

type coordinationAnchor struct {
	Version          string               `json:"version"`
	TargetKey        string               `json:"target_key"`
	CoordinationRoot string               `json:"coordination_root"`
	StateHome        string               `json:"state_home"`
	ConfigID         string               `json:"config_id"`
	ConfigPath       string               `json:"config_path"`
	HotRoot          string               `json:"hot_root"`
	PolicyPath       string               `json:"policy_path"`
	PolicyKey        string               `json:"policy_key"`
	Partition        state.DreamPartition `json:"partition"`
}

type policyAuthorizationState struct {
	Version           string `json:"version"`
	PolicyKey         string `json:"policy_key"`
	PolicyPath        string `json:"policy_path"`
	HighestGeneration int    `json:"highest_generation"`
	PolicyDigest      string `json:"policy_digest,omitempty"`
	State             string `json:"state"`
	Reason            string `json:"reason,omitempty"`
}

type targetAuthorizationState struct {
	Version                string               `json:"version"`
	TargetKey              string               `json:"target_key"`
	StateHome              string               `json:"state_home"`
	ConfigID               string               `json:"config_id"`
	ConfigPath             string               `json:"config_path"`
	HotRoot                string               `json:"hot_root"`
	PolicyPath             string               `json:"policy_path"`
	PolicyKey              string               `json:"policy_key"`
	Partition              state.DreamPartition `json:"partition"`
	MountID                string               `json:"mount_id,omitempty"`
	CanonicalPersonID      string               `json:"canonical_person_id,omitempty"`
	LocalTarget            bool                 `json:"local_target,omitempty"`
	HighestGeneration      int                  `json:"highest_generation"`
	PolicyDigest           string               `json:"policy_digest,omitempty"`
	BindingDigest          string               `json:"binding_digest,omitempty"`
	State                  string               `json:"state"`
	Reason                 string               `json:"reason,omitempty"`
	AuthorizationRevision  uint64               `json:"authorization_revision"`
	LastCommittedHOTDigest string               `json:"last_committed_hot_digest,omitempty"`
}

type ledgerHead struct {
	Version   string          `json:"version"`
	Sequence  uint64          `json:"sequence"`
	StateHash string          `json:"state_hash"`
	State     json.RawMessage `json:"state"`
}

type ledgerEvent struct {
	Version      string          `json:"version"`
	Sequence     uint64          `json:"sequence"`
	PreviousHash string          `json:"previous_state_hash"`
	StateHash    string          `json:"state_hash"`
	Actor        string          `json:"actor"`
	Reason       string          `json:"reason"`
	At           time.Time       `json:"at"`
	State        json.RawMessage `json:"state"`
}

type loadedLedger struct {
	State         json.RawMessage
	Sequence      uint64
	Hash          string
	Exists        bool
	NeedsRecovery bool
	History       []ledgerState
}

type ledgerState struct {
	Sequence uint64
	Hash     string
	State    json.RawMessage
}

func targetCoordinationKey(p state.DreamPartition) string {
	return hashStrings(p.PersonID, p.AgentID, p.AudienceID, p.Scope)[:24]
}

func policyCoordinationKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func coordinationRoot(hotRoot string) (string, error) {
	if err := validateCurrentRoot(hotRoot); err != nil {
		return "", err
	}
	parent := filepath.Dir(hotRoot)
	if parent == hotRoot || filepath.Base(hotRoot) == "coordination" {
		return "", errors.New("dream coordination root cannot be derived safely")
	}
	canonical, err := filepath.EvalSymlinks(parent)
	parentErr := validatePrivateDir(parent)
	if err != nil || canonical != parent || parentErr != nil {
		return "", fmt.Errorf("dream coordination parent must be canonical and owner-private: canonical=%q parent=%q: %w", canonical, parent, errors.Join(err, parentErr))
	}
	return filepath.Join(parent, "coordination"), nil
}

func ensureCoordinationRoot(hotRoot string) (string, error) {
	root, err := coordinationRoot(hotRoot)
	if err != nil {
		return "", err
	}
	return ensurePrivateChild(filepath.Dir(root), filepath.Base(root))
}

func anchorPaths(home string, p state.DreamPartition, create bool) (string, string, error) {
	if !validSharedPartition(p) || validatePrivateDir(home) != nil {
		return "", "", errors.New("invalid dream coordination target")
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil || canonical != home || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", "", errors.New("dream coordination state home must be canonical")
	}
	base := filepath.Join(home, ".dream-coordination")
	targets := filepath.Join(base, "targets")
	if create {
		base, err = ensurePrivateChild(home, ".dream-coordination")
		if err == nil {
			targets, err = ensurePrivateChild(base, "targets")
		}
	} else {
		baseExists, baseErr := existingPrivateDirectory(base)
		if baseErr != nil {
			return "", "", baseErr
		}
		if !baseExists {
			return "", "", os.ErrNotExist
		}
		targetsExists, targetsErr := existingPrivateDirectory(targets)
		if targetsErr != nil {
			return "", "", targetsErr
		}
		if !targetsExists {
			return "", "", errors.New("dream coordination targets directory is missing")
		}
	}
	if err != nil {
		return "", "", err
	}
	id := targetCoordinationKey(p)
	return filepath.Join(targets, id+".json"), filepath.Join(targets, id+".lock"), nil
}

func existingPrivateDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	canonical, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil || canonical != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || validatePrivateDir(path) != nil {
		return false, errors.New("dream coordination directory is unsafe")
	}
	return true, nil
}

func acquireCoordinationLock(ctx context.Context, path string, create bool) (func(), error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	valid := func() error {
		opened, e1 := f.Stat()
		pathInfo, e2 := os.Lstat(path)
		a, ok1 := infoSys(opened)
		b, ok2 := infoSys(pathInfo)
		if e1 != nil || e2 != nil || !ok1 || !ok2 || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || int(a.Uid) != os.Geteuid() || a.Nlink != 1 || pathInfo.Mode()&os.ModeSymlink != 0 || a.Dev != b.Dev || a.Ino != b.Ino {
			return errors.New("dream coordination lock is unsafe")
		}
		return nil
	}
	if err = valid(); err != nil {
		f.Close()
		return nil, err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			f.Close()
			return nil, errors.New("dream coordination lock wait exceeded 5s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = valid(); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func ledgerPath(root, kind, key string) (string, error) {
	base, err := ensurePrivateChild(root, kind)
	if err != nil {
		return "", err
	}
	return ensurePrivateChild(base, key)
}

func loadLedger(root string) (loadedLedger, error) {
	var result loadedLedger
	var totalBytes int64
	eventsPath := filepath.Join(root, "events")
	if _, err := os.Lstat(eventsPath); errors.Is(err, os.ErrNotExist) {
		if _, headErr := os.Lstat(filepath.Join(root, "head.json")); errors.Is(headErr, os.ErrNotExist) {
			return result, nil
		}
		return result, errors.New("dream coordination ledger head exists without events")
	}
	if validatePrivateDir(eventsPath) != nil {
		return result, errors.New("dream coordination events directory is unsafe")
	}
	dir, err := os.Open(eventsPath)
	if err != nil {
		return result, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(coordinationEventLimit + 1)
	if err != nil {
		return result, err
	}
	if len(entries) > coordinationEventLimit {
		return result, errors.New("dream coordination event limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	previous := strings.Repeat("0", 64)
	for i, entry := range entries {
		want := fmt.Sprintf("%020d.json", i+1)
		if entry.Name() != want {
			return result, errors.New("dream coordination event sequence is not contiguous")
		}
		raw, readErr := readPrivateRegular(filepath.Join(eventsPath, entry.Name()), coordinationFileLimit)
		if readErr != nil {
			return result, readErr
		}
		totalBytes += int64(len(raw))
		if totalBytes > coordinationLedgerLimit {
			return result, errors.New("dream coordination ledger byte limit exceeded")
		}
		var event ledgerEvent
		if strictJSON(raw, &event) != nil || event.Version != coordinationVersion || event.Sequence != uint64(i+1) || event.PreviousHash != previous || hashRawState(event.State) != event.StateHash || event.Actor == "" || event.Reason == "" {
			return result, errors.New("dream coordination event is invalid")
		}
		result.State, result.Sequence, result.Hash, result.Exists = append([]byte(nil), event.State...), event.Sequence, event.StateHash, true
		result.History = append(result.History, ledgerState{Sequence: event.Sequence, Hash: event.StateHash, State: append([]byte(nil), event.State...)})
		previous = event.StateHash
	}
	headRaw, headErr := readPrivateRegular(filepath.Join(root, "head.json"), coordinationFileLimit)
	if errors.Is(headErr, os.ErrNotExist) {
		result.NeedsRecovery = result.Exists
		return result, nil
	}
	if headErr != nil {
		return result, headErr
	}
	totalBytes += int64(len(headRaw))
	if totalBytes > coordinationLedgerLimit {
		return result, errors.New("dream coordination ledger byte limit exceeded")
	}
	var head ledgerHead
	if strictJSON(headRaw, &head) != nil || head.Version != coordinationVersion || head.Sequence < 1 || head.Sequence > result.Sequence || hashRawState(head.State) != head.StateHash {
		return result, errors.New("dream coordination ledger head is invalid")
	}
	expected := result.History[head.Sequence-1]
	if expected.Sequence != head.Sequence || expected.Hash != head.StateHash || hashRawState(expected.State) != hashRawState(head.State) {
		return result, errors.New("dream coordination ledger head does not match its event")
	}
	if head.Sequence == result.Sequence {
		if head.StateHash != result.Hash {
			return result, errors.New("dream coordination ledger head conflicts with events")
		}
	} else {
		result.NeedsRecovery = true
	}
	return result, nil
}

func appendLedger(root string, prior loadedLedger, state any, actor, reason string) (loadedLedger, error) {
	if actor == "" || reason == "" || len(actor) > 256 || len(reason) > 2048 {
		return prior, errors.New("dream coordination event requires bounded actor and reason")
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > coordinationFileLimit {
		return prior, errors.New("dream coordination state exceeds its limit")
	}
	hash := hashRawState(raw)
	if prior.Exists && hash == prior.Hash {
		if prior.NeedsRecovery {
			err = writeLedgerHead(root, ledgerHead{Version: coordinationVersion, Sequence: prior.Sequence, StateHash: prior.Hash, State: prior.State})
			if err == nil {
				prior.NeedsRecovery = false
			}
		}
		return prior, err
	}
	if prior.Sequence >= coordinationEventLimit {
		return prior, errors.New("dream coordination event limit exhausted")
	}
	events, err := ensurePrivateChild(root, "events")
	if err != nil {
		return prior, err
	}
	sequence := prior.Sequence + 1
	previous := prior.Hash
	if previous == "" {
		previous = strings.Repeat("0", 64)
	}
	event := ledgerEvent{Version: coordinationVersion, Sequence: sequence, PreviousHash: previous, StateHash: hash, Actor: actor, Reason: reason, At: time.Now().UTC(), State: raw}
	if err = writeImmutableJSON(filepath.Join(events, fmt.Sprintf("%020d.json", sequence)), event); err != nil {
		return prior, err
	}
	next := loadedLedger{State: raw, Sequence: sequence, Hash: hash, Exists: true, History: append(append([]ledgerState(nil), prior.History...), ledgerState{Sequence: sequence, Hash: hash, State: append([]byte(nil), raw...)})}
	if err = writeLedgerHead(root, ledgerHead{Version: coordinationVersion, Sequence: sequence, StateHash: hash, State: raw}); err != nil {
		next.NeedsRecovery = true
		return next, fmt.Errorf("dream coordination event is durable but head update is uncertain: %w", err)
	}
	return next, nil
}

func strictJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return errors.New("noncanonical or duplicate JSON fields")
	}
	return nil
}

func hashRawState(raw []byte) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		raw = compact.Bytes()
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func writeImmutableJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > coordinationFileLimit {
		return errors.New("dream coordination event exceeds its limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err = writeSyncClose(f, raw); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeLedgerHead(root string, head ledgerHead) error {
	return writeAtomicJSON(root, "head.json", head)
}

func writeAtomicJSON(root, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > coordinationFileLimit {
		return errors.New("dream coordination file exceeds its limit")
	}
	f, err := os.CreateTemp(root, ".coordination-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = errors.Join(f.Chmod(0600), writeSyncClose(f, raw)); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(root, name)); err != nil {
		return err
	}
	return syncDirectory(root)
}

func readAnchor(path string) (coordinationAnchor, error) {
	var anchor coordinationAnchor
	raw, err := readPrivateRegular(path, coordinationFileLimit)
	if err != nil {
		return anchor, err
	}
	if strictJSON(raw, &anchor) != nil || anchor.Version != coordinationVersion || anchor.TargetKey != targetCoordinationKey(anchor.Partition) || anchor.PolicyKey != policyCoordinationKey(anchor.PolicyPath) {
		return anchor, errors.New("dream coordination anchor is invalid")
	}
	return anchor, nil
}

func anchorForTarget(ctx context.Context, target CoordinationTarget, create bool) (coordinationAnchor, string, func(), error) {
	var zero coordinationAnchor
	anchorPath, lockPath, err := anchorPaths(target.StateHome, target.Partition, create)
	if err != nil {
		return zero, "", nil, err
	}
	release, err := acquireCoordinationLock(ctx, lockPath, create)
	if err != nil {
		return zero, "", nil, err
	}
	anchor, err := readAnchor(anchorPath)
	if err == nil {
		return anchor, anchorPath, release, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		release()
		return zero, "", nil, err
	}
	if target.ConfigID == "" || target.ConfigPath == "" || target.HotRoot == "" || target.PolicyPath == "" {
		release()
		return zero, "", nil, errors.New("dream coordination registration requires complete trusted target metadata")
	}
	if len(target.ConfigID) > 512 || !filepath.IsAbs(target.ConfigPath) || filepath.Clean(target.ConfigPath) != target.ConfigPath || !filepath.IsAbs(target.PolicyPath) || filepath.Clean(target.PolicyPath) != target.PolicyPath {
		release()
		return zero, "", nil, errors.New("dream coordination registration metadata is invalid")
	}
	root, rootErr := ensureCoordinationRoot(target.HotRoot)
	if rootErr != nil {
		release()
		return zero, "", nil, rootErr
	}
	anchor = coordinationAnchor{Version: coordinationVersion, TargetKey: targetCoordinationKey(target.Partition), CoordinationRoot: root, StateHome: target.StateHome, ConfigID: target.ConfigID, ConfigPath: target.ConfigPath, HotRoot: target.HotRoot, PolicyPath: target.PolicyPath, PolicyKey: policyCoordinationKey(target.PolicyPath), Partition: target.Partition}
	return anchor, anchorPath, release, nil
}

func validateAnchorTarget(anchor coordinationAnchor, target CoordinationTarget) error {
	if anchor.StateHome != target.StateHome || anchor.Partition != target.Partition {
		return errors.New("dream coordination anchor target changed")
	}
	if anchor.ConfigID == "" || len(anchor.ConfigID) > 512 || !filepath.IsAbs(anchor.ConfigPath) || filepath.Clean(anchor.ConfigPath) != anchor.ConfigPath || !filepath.IsAbs(anchor.PolicyPath) || filepath.Clean(anchor.PolicyPath) != anchor.PolicyPath {
		return errors.New("dream coordination anchor metadata is invalid")
	}
	if target.ConfigID != "" && (anchor.ConfigID != target.ConfigID || anchor.ConfigPath != target.ConfigPath || anchor.HotRoot != target.HotRoot || anchor.PolicyPath != target.PolicyPath) {
		return errors.New("dream coordination root, config, or policy cannot be rebound implicitly")
	}
	root, err := coordinationRoot(anchor.HotRoot)
	if err != nil || root != anchor.CoordinationRoot {
		return errors.New("dream coordination anchor root is unsafe")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || validatePrivateDir(root) != nil {
		return errors.New("dream coordination root is missing or unsafe")
	}
	return nil
}

func decodePolicyState(ledger loadedLedger) (policyAuthorizationState, error) {
	var s policyAuthorizationState
	if !ledger.Exists {
		return s, os.ErrNotExist
	}
	if strictJSON(ledger.State, &s) != nil || s.Version != coordinationVersion {
		return s, errors.New("dream policy authorization state is invalid")
	}
	return s, nil
}
func decodeTargetState(ledger loadedLedger) (targetAuthorizationState, error) {
	var s targetAuthorizationState
	if !ledger.Exists {
		return s, os.ErrNotExist
	}
	if strictJSON(ledger.State, &s) != nil || s.Version != coordinationVersion {
		return s, errors.New("dream target authorization state is invalid")
	}
	return s, nil
}

func validatePolicyLedger(anchor coordinationAnchor, ledger loadedLedger) error {
	if !ledger.Exists {
		return nil
	}
	previousGeneration := 0
	for _, item := range ledger.History {
		var value policyAuthorizationState
		if strictJSON(item.State, &value) != nil || value.Version != coordinationVersion || value.PolicyKey != anchor.PolicyKey || value.PolicyPath != anchor.PolicyPath || value.HighestGeneration < 1 || value.HighestGeneration < previousGeneration || !validProjectionDigest(value.PolicyDigest) || !validAuthorizationState(value.State, value.Reason) {
			return errors.New("dream policy authorization history is invalid")
		}
		previousGeneration = value.HighestGeneration
	}
	value, err := decodePolicyState(ledger)
	if err != nil || value.PolicyKey != anchor.PolicyKey || value.PolicyPath != anchor.PolicyPath {
		return errors.New("dream policy authorization state does not match its anchor")
	}
	return nil
}

func validateTargetLedger(anchor coordinationAnchor, ledger loadedLedger) error {
	if !ledger.Exists {
		return nil
	}
	previousGeneration := 0
	var previousRevision uint64
	for _, item := range ledger.History {
		var value targetAuthorizationState
		if strictJSON(item.State, &value) != nil || value.Version != coordinationVersion || value.TargetKey != anchor.TargetKey || value.StateHome != anchor.StateHome || value.ConfigID != anchor.ConfigID || value.ConfigPath != anchor.ConfigPath || value.HotRoot != anchor.HotRoot || value.PolicyPath != anchor.PolicyPath || value.PolicyKey != anchor.PolicyKey || value.Partition != anchor.Partition || value.HighestGeneration < 1 || value.HighestGeneration < previousGeneration || value.AuthorizationRevision < 1 || value.AuthorizationRevision < previousRevision || !validProjectionDigest(value.PolicyDigest) || !validProjectionDigest(value.BindingDigest) || value.MountID == "" || value.CanonicalPersonID == "" || !validAuthorizationState(value.State, value.Reason) || value.LastCommittedHOTDigest != "" && !validProjectionDigest(value.LastCommittedHOTDigest) {
			return errors.New("dream target authorization history is invalid")
		}
		previousGeneration, previousRevision = value.HighestGeneration, value.AuthorizationRevision
	}
	value, err := decodeTargetState(ledger)
	if err != nil || value.TargetKey != anchor.TargetKey || value.StateHome != anchor.StateHome || value.ConfigID != anchor.ConfigID || value.ConfigPath != anchor.ConfigPath || value.HotRoot != anchor.HotRoot || value.PolicyPath != anchor.PolicyPath || value.PolicyKey != anchor.PolicyKey || value.Partition != anchor.Partition {
		return errors.New("dream target authorization state does not match its anchor")
	}
	return nil
}

func validAuthorizationState(value, reason string) bool {
	return value == "active" && reason == "" || value == "invalid" && reason != ""
}

func CoordinateSharedPublish(ctx context.Context, target CoordinationTarget, prepare func() (AuthorizationObservation, SharedHOTBuilder, error), publish func(HOTBundle) error) (HOTBundle, AuthorizationStatus, error) {
	var bundle HOTBundle
	anchor, anchorPath, releaseAnchor, err := anchorForTarget(ctx, target, true)
	if err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	defer releaseAnchor()
	if err = validateAnchorTarget(anchor, target); err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	_, anchorStatErr := os.Lstat(anchorPath)
	initialized := anchorStatErr == nil
	var policyRoot, targetRoot string
	if initialized {
		policyRoot = filepath.Join(anchor.CoordinationRoot, "policies", anchor.PolicyKey)
		targetRoot = filepath.Join(anchor.CoordinationRoot, "targets", anchor.TargetKey)
		if validatePrivateDir(anchor.CoordinationRoot) != nil || validatePrivateDir(policyRoot) != nil || validatePrivateDir(targetRoot) != nil {
			return bundle, AuthorizationStatus{}, errors.New("dream coordination ledger is missing or unsafe for an initialized anchor")
		}
	} else {
		policyRoot, err = ledgerPath(anchor.CoordinationRoot, "policies", anchor.PolicyKey)
		if err == nil {
			targetRoot, err = ledgerPath(anchor.CoordinationRoot, "targets", anchor.TargetKey)
		}
		if err != nil {
			return bundle, AuthorizationStatus{}, err
		}
	}
	releasePolicy, err := acquireCoordinationLock(ctx, filepath.Join(policyRoot, ".lock"), !initialized)
	if err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	defer releasePolicy()
	releaseTarget, err := acquireCoordinationLock(ctx, filepath.Join(targetRoot, ".lock"), !initialized)
	if err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	defer releaseTarget()
	policyLedger, err := loadLedger(policyRoot)
	if err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	targetLedger, err := loadLedger(targetRoot)
	if err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	if initialized && (!policyLedger.Exists || !targetLedger.Exists) {
		return bundle, AuthorizationStatus{}, errors.New("dream coordination ledger is missing for an initialized anchor")
	}
	if err = errors.Join(validatePolicyLedger(anchor, policyLedger), validateTargetLedger(anchor, targetLedger)); err != nil {
		return bundle, AuthorizationStatus{}, err
	}
	observation, builder, prepareErr := prepare()
	if prepareErr != nil {
		var authorizationErr AuthorizationFailure
		if errors.As(prepareErr, &authorizationErr) {
			policyLedger, targetLedger, err = invalidateLedgers(policyRoot, targetRoot, policyLedger, targetLedger, anchor, "runtime", authorizationErr.Error())
			if err != nil {
				return bundle, AuthorizationStatus{}, errors.Join(prepareErr, err)
			}
		}
		status, statusErr := statusFromLedgers(anchor, policyLedger, targetLedger)
		return bundle, status, errors.Join(prepareErr, statusErr)
	}
	policyState, targetState, policyLedger, targetLedger, err := authorizeObservation(policyRoot, targetRoot, policyLedger, targetLedger, anchor, observation)
	if err != nil {
		status, statusErr := statusFromLedgers(anchor, policyLedger, targetLedger)
		return bundle, status, errors.Join(err, statusErr)
	}
	if _, statErr := os.Lstat(anchorPath); errors.Is(statErr, os.ErrNotExist) {
		if err = writeAtomicJSON(filepath.Dir(anchorPath), filepath.Base(anchorPath), anchor); err != nil {
			return bundle, AuthorizationStatus{}, err
		}
	} else if statErr != nil {
		return bundle, AuthorizationStatus{}, statErr
	}
	authority := HOTAuthority{ID: anchor.TargetKey, Revision: targetState.AuthorizationRevision}
	bundle, err = builder(authority)
	if err != nil {
		var authorizationErr AuthorizationFailure
		if errors.As(err, &authorizationErr) {
			policyLedger, targetLedger, invalidateErr := invalidateLedgers(policyRoot, targetRoot, policyLedger, targetLedger, anchor, "runtime", authorizationErr.Error())
			if invalidateErr != nil {
				return HOTBundle{}, AuthorizationStatus{}, errors.Join(err, invalidateErr)
			}
			status, statusErr := statusFromLedgers(anchor, policyLedger, targetLedger)
			return HOTBundle{}, status, errors.Join(err, statusErr)
		}
		return HOTBundle{}, statusFromStates(anchor, policyState, targetState, policyLedger.NeedsRecovery || targetLedger.NeedsRecovery), err
	}
	if err = publish(bundle); err != nil {
		return HOTBundle{}, statusFromStates(anchor, policyState, targetState, policyLedger.NeedsRecovery || targetLedger.NeedsRecovery), err
	}
	if targetState.LastCommittedHOTDigest != bundle.Digest {
		targetState.LastCommittedHOTDigest = bundle.Digest
		targetLedger, err = appendLedger(targetRoot, targetLedger, targetState, "runtime", "commit shared HOT "+bundle.Digest)
		if err != nil {
			return bundle, statusFromStates(anchor, policyState, targetState, true), fmt.Errorf("shared HOT was published but coordination commit is uncertain: %w", err)
		}
	}
	return bundle, statusFromStates(anchor, policyState, targetState, policyLedger.NeedsRecovery || targetLedger.NeedsRecovery), nil
}

func authorizeObservation(policyRoot, targetRoot string, pLedger, tLedger loadedLedger, anchor coordinationAnchor, o AuthorizationObservation) (policyAuthorizationState, targetAuthorizationState, loadedLedger, loadedLedger, error) {
	if o.Generation < 1 || !validProjectionDigest(o.PolicyDigest) || !validProjectionDigest(o.BindingDigest) || o.MountID == "" || o.CanonicalPersonID == "" {
		return policyAuthorizationState{}, targetAuthorizationState{}, pLedger, tLedger, errors.New("invalid dream authorization observation")
	}
	p, pErr := decodePolicyState(pLedger)
	if errors.Is(pErr, os.ErrNotExist) {
		p = policyAuthorizationState{Version: coordinationVersion, PolicyKey: anchor.PolicyKey, PolicyPath: anchor.PolicyPath, State: "active"}
	} else if pErr != nil {
		return p, targetAuthorizationState{}, pLedger, tLedger, pErr
	}
	if p.PolicyKey != anchor.PolicyKey || p.PolicyPath != anchor.PolicyPath {
		return p, targetAuthorizationState{}, pLedger, tLedger, errors.New("dream policy ledger identity mismatch")
	}
	if o.Generation < p.HighestGeneration {
		return p, targetAuthorizationState{}, pLedger, tLedger, errors.New("dream sharing policy generation rolled back")
	}
	if o.Generation == p.HighestGeneration && p.HighestGeneration > 0 && (p.State != "active" || p.PolicyDigest != o.PolicyDigest) {
		p.State, p.Reason = "invalid", "same generation cannot change or restore policy authorization"
		var appendErr error
		pLedger, appendErr = appendLedger(policyRoot, pLedger, p, "runtime", p.Reason)
		return p, targetAuthorizationState{}, pLedger, tLedger, errors.Join(errors.New(p.Reason), appendErr)
	}
	if o.Generation > p.HighestGeneration {
		p.HighestGeneration, p.PolicyDigest, p.State, p.Reason = o.Generation, o.PolicyDigest, "active", ""
		var err error
		pLedger, err = appendLedger(policyRoot, pLedger, p, "runtime", "accept higher policy generation")
		if err != nil {
			return p, targetAuthorizationState{}, pLedger, tLedger, err
		}
	}
	t, tErr := decodeTargetState(tLedger)
	if errors.Is(tErr, os.ErrNotExist) {
		t = targetAuthorizationState{Version: coordinationVersion, TargetKey: anchor.TargetKey, StateHome: anchor.StateHome, ConfigID: anchor.ConfigID, ConfigPath: anchor.ConfigPath, HotRoot: anchor.HotRoot, PolicyPath: anchor.PolicyPath, PolicyKey: anchor.PolicyKey, Partition: anchor.Partition, State: "active"}
	} else if tErr != nil {
		return p, t, pLedger, tLedger, tErr
	}
	if t.TargetKey != anchor.TargetKey || t.StateHome != anchor.StateHome || t.ConfigID != anchor.ConfigID || t.ConfigPath != anchor.ConfigPath || t.HotRoot != anchor.HotRoot || t.PolicyPath != anchor.PolicyPath || t.PolicyKey != anchor.PolicyKey || t.Partition != anchor.Partition {
		return p, t, pLedger, tLedger, errors.New("dream target authorization ledger identity mismatch")
	}
	if o.Generation < t.HighestGeneration {
		return p, t, pLedger, tLedger, errors.New("dream target authorization generation rolled back")
	}
	changed := t.PolicyDigest != o.PolicyDigest || t.BindingDigest != o.BindingDigest || t.MountID != o.MountID || t.CanonicalPersonID != o.CanonicalPersonID || t.LocalTarget != o.LocalTarget
	if o.Generation == t.HighestGeneration && t.HighestGeneration > 0 && (t.State != "active" || changed) {
		reason := "same generation cannot change or restore target authorization"
		p.State, p.Reason = "invalid", reason
		var appendErr error
		pLedger, appendErr = appendLedger(policyRoot, pLedger, p, "runtime", reason)
		if appendErr != nil {
			return p, t, pLedger, tLedger, errors.Join(errors.New(reason), appendErr)
		}
		if t.State != "invalid" || t.Reason != reason {
			t.AuthorizationRevision++
		}
		t.State, t.Reason = "invalid", reason
		tLedger, appendErr = appendLedger(targetRoot, tLedger, t, "runtime", reason)
		return p, t, pLedger, tLedger, errors.Join(errors.New(reason), appendErr)
	}
	if o.Generation > t.HighestGeneration || t.HighestGeneration == 0 {
		t.HighestGeneration, t.PolicyDigest, t.BindingDigest, t.MountID, t.CanonicalPersonID, t.LocalTarget, t.State, t.Reason = o.Generation, o.PolicyDigest, o.BindingDigest, o.MountID, o.CanonicalPersonID, o.LocalTarget, "active", ""
		t.AuthorizationRevision++
		var err error
		tLedger, err = appendLedger(targetRoot, tLedger, t, "runtime", "accept target authorization")
		if err != nil {
			return p, t, pLedger, tLedger, err
		}
	}
	return p, t, pLedger, tLedger, nil
}

func invalidateLedgers(policyRoot, targetRoot string, pLedger, tLedger loadedLedger, anchor coordinationAnchor, actor, reason string) (loadedLedger, loadedLedger, error) {
	p, err := decodePolicyState(pLedger)
	if errors.Is(err, os.ErrNotExist) {
		return pLedger, tLedger, errors.New("cannot invalidate missing initialized policy ledger")
	}
	if err != nil {
		return pLedger, tLedger, err
	}
	t, err := decodeTargetState(tLedger)
	if errors.Is(err, os.ErrNotExist) {
		return pLedger, tLedger, errors.New("cannot invalidate missing initialized target ledger")
	}
	if err != nil {
		return pLedger, tLedger, err
	}
	if p.State != "invalid" || p.Reason != reason {
		p.State, p.Reason = "invalid", reason
	}
	pLedger, err = appendLedger(policyRoot, pLedger, p, actor, reason)
	if err != nil {
		return pLedger, tLedger, err
	}
	if t.State != "invalid" || t.Reason != reason {
		t.State, t.Reason = "invalid", reason
		t.AuthorizationRevision++
	}
	tLedger, err = appendLedger(targetRoot, tLedger, t, actor, reason)
	return pLedger, tLedger, err
}

func statusFromLedgers(anchor coordinationAnchor, pLedger, tLedger loadedLedger) (AuthorizationStatus, error) {
	p, err := decodePolicyState(pLedger)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	t, err := decodeTargetState(tLedger)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	if p.State == "invalid" {
		t.State, t.Reason = "invalid", p.Reason
	} else if t.HighestGeneration < p.HighestGeneration {
		t.State, t.Reason = "invalid", "target has not observed the current policy generation"
	}
	return statusFromStates(anchor, p, t, pLedger.NeedsRecovery || tLedger.NeedsRecovery), nil
}
func statusFromStates(anchor coordinationAnchor, p policyAuthorizationState, t targetAuthorizationState, recovery bool) AuthorizationStatus {
	generation := t.HighestGeneration
	if p.HighestGeneration > generation {
		generation = p.HighestGeneration
	}
	return AuthorizationStatus{TargetKey: anchor.TargetKey, PolicyKey: anchor.PolicyKey, State: t.State, Reason: t.Reason, HighestGeneration: generation, PolicyDigest: t.PolicyDigest, BindingDigest: t.BindingDigest, MountID: t.MountID, CanonicalPersonID: t.CanonicalPersonID, LocalTarget: t.LocalTarget, AuthorizationRevision: t.AuthorizationRevision, LastCommittedHOTDigest: t.LastCommittedHOTDigest, NeedsRecovery: recovery, Partition: anchor.Partition}
}

func withExistingAuthorization(ctx context.Context, target CoordinationTarget, fn func(coordinationAnchor, string, string, loadedLedger, loadedLedger) (loadedLedger, loadedLedger, error)) (AuthorizationStatus, error) {
	anchor, _, releaseAnchor, err := anchorForTarget(ctx, target, false)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	defer releaseAnchor()
	if err = validateAnchorTarget(anchor, target); err != nil {
		return AuthorizationStatus{}, err
	}
	policyRoot := filepath.Join(anchor.CoordinationRoot, "policies", anchor.PolicyKey)
	targetRoot := filepath.Join(anchor.CoordinationRoot, "targets", anchor.TargetKey)
	if validatePrivateDir(policyRoot) != nil || validatePrivateDir(targetRoot) != nil {
		return AuthorizationStatus{}, errors.New("dream coordination ledger is missing or unsafe")
	}
	rp, err := acquireCoordinationLock(ctx, filepath.Join(policyRoot, ".lock"), false)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	defer rp()
	rt, err := acquireCoordinationLock(ctx, filepath.Join(targetRoot, ".lock"), false)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	defer rt()
	pl, err := loadLedger(policyRoot)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	tl, err := loadLedger(targetRoot)
	if err != nil {
		return AuthorizationStatus{}, err
	}
	if !pl.Exists || !tl.Exists {
		return AuthorizationStatus{}, errors.New("dream coordination ledger is missing for an initialized anchor")
	}
	if err = errors.Join(validatePolicyLedger(anchor, pl), validateTargetLedger(anchor, tl)); err != nil {
		return AuthorizationStatus{}, err
	}
	pl, tl, err = fn(anchor, policyRoot, targetRoot, pl, tl)
	err = errors.Join(err, validatePolicyLedger(anchor, pl), validateTargetLedger(anchor, tl))
	status, statusErr := statusFromLedgers(anchor, pl, tl)
	return status, errors.Join(err, statusErr)
}

func ReadAuthorization(ctx context.Context, target CoordinationTarget) (AuthorizationStatus, error) {
	return withExistingAuthorization(ctx, target, func(_ coordinationAnchor, _, _ string, p, t loadedLedger) (loadedLedger, loadedLedger, error) {
		return p, t, nil
	})
}

// WithAuthorizationLock holds the anchor, global policy and target locks while
// fn runs. It is the narrow ordering seam for later current/version maintenance;
// callers must acquire the partition publication lock only from inside fn.
func WithAuthorizationLock(ctx context.Context, target CoordinationTarget, fn func(AuthorizationStatus) error) error {
	if fn == nil {
		return errors.New("dream authorization lock callback is required")
	}
	_, err := withExistingAuthorization(ctx, target, func(anchor coordinationAnchor, _, _ string, p, t loadedLedger) (loadedLedger, loadedLedger, error) {
		status, statusErr := statusFromLedgers(anchor, p, t)
		if statusErr != nil {
			return p, t, statusErr
		}
		return p, t, fn(status)
	})
	return err
}

func RevokeAuthorization(ctx context.Context, target CoordinationTarget, actor, reason string) (AuthorizationStatus, error) {
	return withExistingAuthorization(ctx, target, func(anchor coordinationAnchor, pr, tr string, p, t loadedLedger) (loadedLedger, loadedLedger, error) {
		return invalidateLedgers(pr, tr, p, t, anchor, actor, reason)
	})
}

func InvalidateAuthorizationIfPresent(ctx context.Context, target CoordinationTarget, actor, reason string) (bool, error) {
	anchorPath, _, err := anchorPaths(target.StateHome, target.Partition, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(anchorPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	_, err = RevokeAuthorization(ctx, target, actor, reason)
	return err == nil, err
}

// InvalidateAuthorizationsForConfig invalidates anchors previously registered
// by one trusted target config. It never creates an anchor or coordination root.
func InvalidateAuthorizationsForConfig(ctx context.Context, home, configPath, actor, reason string) error {
	base := filepath.Join(home, ".dream-coordination", "targets")
	coordinationExists, err := existingPrivateDirectory(filepath.Dir(base))
	if err != nil {
		return err
	}
	if !coordinationExists {
		return nil
	}
	targetsExists, err := existingPrivateDirectory(base)
	if err != nil {
		return err
	}
	if !targetsExists {
		return errors.New("dream coordination targets directory is missing")
	}
	dir, err := os.Open(base)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(257)
	dir.Close()
	if err != nil {
		return err
	}
	if len(entries) > 256 {
		return errors.New("dream coordination anchor limit exceeded")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := readPrivateRegular(filepath.Join(base, entry.Name()), coordinationFileLimit)
		if readErr != nil {
			return readErr
		}
		var anchor coordinationAnchor
		if strictJSON(raw, &anchor) != nil {
			return errors.New("dream coordination anchor is invalid")
		}
		if anchor.ConfigPath != configPath {
			continue
		}
		_, readErr = RevokeAuthorization(ctx, CoordinationTarget{StateHome: anchor.StateHome, Partition: anchor.Partition}, actor, reason)
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

// ApplyHOTAuthority attaches durable authorization evidence and recomputes the
// v2 digest. Legacy v1 bundles remain byte-for-byte unchanged when authority is nil.
func ApplyHOTAuthority(bundle HOTBundle, authority HOTAuthority) (HOTBundle, error) {
	if bundle.Version != "dream-hot-v2" || bundle.Projection == nil || authority.ID == "" || authority.Revision < 1 {
		return HOTBundle{}, errors.New("HOT authority requires a shared v2 bundle")
	}
	bundle.Authority = &authority
	bundle.Digest = hotBundleDigest(bundle)
	return bundle, nil
}
