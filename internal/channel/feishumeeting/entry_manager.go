package feishumeeting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const entryLimit = 4

var entryMeetingNumberPattern = regexp.MustCompile(`^[0-9]{9}$`)

type EntryHooks struct {
	// StartMeeting returns after baseline readiness; its runtime uses this ctx.
	StartMeeting func(context.Context, Config) error
	StopMeeting  func(string) error
	Observe      func(kind, status string, data map[string]any) error
}

type EntryMeeting struct {
	Key           string `json:"key"`
	MeetingNumber string `json:"meeting_number,omitempty"`
	MeetingID     string `json:"meeting_id,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	SenderOpenID  string `json:"sender_open_id,omitempty"`
	CallID        string `json:"call_id,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Held          bool   `json:"held"`
	NeedsCheck    bool   `json:"needs_check,omitempty"`
	StopPending   bool   `json:"stop_pending,omitempty"`
}

type entryState struct {
	Version  int                     `json:"version"`
	Scope    string                  `json:"scope"`
	Seen     map[string]bool         `json:"seen"`
	Meetings map[string]EntryMeeting `json:"meetings"`
	Ended    map[string]bool         `json:"ended"`
}

type EntrySnapshot struct {
	Meetings []EntryMeeting `json:"meetings"`
	Seen     int            `json:"seen_count"`
	Ready    bool           `json:"ready"`
}

type entryRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  bool
}

type EntryManager struct {
	config         Config
	path           string
	client         Client
	hooks          EntryHooks
	opened         time.Time
	mu             sync.Mutex
	state          entryState
	runtimes       map[string]entryRuntime
	wake           chan struct{}
	ready, running bool
	failure        error
}

func OpenEntryManager(config Config, path string, client Client, hooks EntryHooks) (*EntryManager, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.AutoJoin {
		return nil, errors.New("entry manager requires explicit automatic entry configuration")
	}
	if !filepath.IsAbs(path) || hooks.StartMeeting == nil || hooks.StopMeeting == nil || hooks.Observe == nil {
		return nil, errors.New("entry manager requires an absolute state path and lifecycle handlers")
	}
	raw, _ := json.Marshal([]string{config.Profile, config.AppID, config.TenantID, config.BotOpenID})
	hash := sha256.Sum256(raw)
	scope := hex.EncodeToString(hash[:])
	m := &EntryManager{config: config, path: path, client: client, hooks: hooks, opened: time.Now().UTC(), runtimes: map[string]entryRuntime{}, wake: make(chan struct{}, 1)}
	m.state = entryState{Version: 1, Scope: scope, Seen: map[string]bool{}, Meetings: map[string]EntryMeeting{}, Ended: map[string]bool{}}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8<<20 {
			return nil, errors.New("entry state must be a bounded private regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &m.state) != nil || m.state.Version != 1 || m.state.Scope != scope || m.state.Seen == nil || m.state.Meetings == nil || m.state.Ended == nil || len(m.state.Seen) > 10000 || len(m.state.Meetings) > 1000 || len(m.state.Ended) > 1000 {
			return nil, errors.New("entry state is invalid or belongs to another identity")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("entry state cannot be read")
	}
	for key, entry := range m.state.Meetings {
		entry.StopPending = false
		switch entry.Status {
		case "active", "starting", "verifying", "checking":
			entry.Status, entry.NeedsCheck = "verifying", true
		case "joining":
			entry.Status, entry.Reason, entry.Held = Unknown, "interrupted_join", true
		case "pending":
			if entry.Held && entry.MeetingID != "" {
				entry.Status, entry.NeedsCheck = "verifying", true
			} else {
				entry.Status, entry.Reason = "skipped", "previous_process_invitation"
			}
		case Unknown, Failed, "ended", "skipped":
		default:
			return nil, errors.New("entry state contains an invalid lifecycle status")
		}
		if entry.Key != key || entry.MeetingID != "" && !meetingID.MatchString(entry.MeetingID) || entry.MeetingNumber != "" && !entryMeetingNumberPattern.MatchString(entry.MeetingNumber) {
			return nil, errors.New("entry state contains an invalid meeting identity")
		}
		m.state.Meetings[key] = entry
	}
	if config.MeetingID != "" && !m.state.Ended[config.MeetingID] {
		key := m.findLocked("", config.MeetingID)
		if key == "" {
			if m.slotsLocked() >= entryLimit {
				return nil, errors.New("entry seed exceeds the active meeting limit")
			}
			if len(m.state.Meetings) >= 1000 {
				return nil, errors.New("entry meeting retention limit reached")
			}
			key = "seed:" + config.MeetingID
			m.state.Meetings[key] = EntryMeeting{Key: key, MeetingID: config.MeetingID, Status: "verifying", Held: true, NeedsCheck: true}
		} else if entry := m.state.Meetings[key]; entry.Status == Failed || entry.Status == "skipped" || entry.Status == Unknown {
			// An explicitly configured seed is read-only checked on every start.
			// A prior absence must not suppress a bot that has since joined.
			if !entry.Held && m.slotsLocked() >= entryLimit {
				return nil, errors.New("entry seed exceeds the active meeting limit")
			}
			entry.Status, entry.Held, entry.NeedsCheck = "verifying", true, true
			m.state.Meetings[key] = entry
		}
	}
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

// Receive returns success only after persistence. The CLI event transport owns
// platform acknowledgements; joins and stop hooks run separately in Run.
func (m *EntryManager) Receive(ctx context.Context, event EntryEvent) (receiveErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	observationStatus := ""
	defer func() {
		m.mu.Unlock()
		if receiveErr == nil && observationStatus != "" {
			receiveErr = m.hooks.Observe("meeting.entry.received", observationStatus, map[string]any{"event_id": event.ID, "meeting_id": event.MeetingID, "meeting_number": event.MeetingNumber})
			if receiveErr == nil {
				m.signal()
			}
		}
	}()
	if m.failure != nil {
		return m.failure
	}
	if event.ID == "" || len(event.ID) > 256 || strings.ContainsAny(event.ID, "\x00\r\n") || event.At.IsZero() || event.At.After(time.Now().Add(time.Minute)) {
		return errors.New("entry event identity or timestamp is invalid")
	}
	if event.Kind != EntryEnd && event.Kind != EntryInvite && event.Kind != EntryMessage {
		return errors.New("unsupported entry event")
	}
	if event.MeetingID != "" && !meetingID.MatchString(event.MeetingID) {
		return errors.New("entry long meeting ID is invalid")
	}
	if event.Kind == EntryEnd && event.MeetingID == "" {
		return errors.New("meeting end requires a long meeting ID")
	}
	if event.Kind != EntryEnd && (!entryMeetingNumberPattern.MatchString(event.MeetingNumber) || len(event.CallID) > 256 || strings.ContainsAny(event.CallID, "\x00\r\n")) {
		return errors.New("entry meeting number or call ID is invalid")
	}
	if m.state.Seen[event.ID] {
		observationStatus = "duplicate"
		return nil
	}
	if len(m.state.Seen) >= 10000 {
		return errors.New("entry event retention limit reached")
	}
	observationStatus = "ignored"
	key := m.findLocked(event.MeetingNumber, event.MeetingID)
	if event.Kind == EntryEnd {
		if len(m.state.Ended) >= 1000 && !m.state.Ended[event.MeetingID] {
			return errors.New("entry terminal meeting retention limit reached")
		}
		m.state.Ended[event.MeetingID] = true
		observationStatus = "ended"
		if key != "" {
			entry := m.state.Meetings[key]
			entry.EventID = event.ID
			entry.Status, entry.Reason, entry.Held, entry.StopPending = "ended", "meeting_ended", false, true
			m.state.Meetings[key] = entry
			m.cancelLocked(key)
		}
	} else if m.allowed(event.SenderOpenID) && !event.At.Before(m.opened.Add(-2*time.Second)) && !m.state.Ended[event.MeetingID] {
		seedRecovery := false
		if previous := m.state.Meetings[key]; event.Kind == EntryMessage && event.MeetingID == "" && previous.Status == Unknown && previous.MeetingID == "" && m.config.MeetingID != "" {
			seedKey := m.findLocked("", m.config.MeetingID)
			seed := m.state.Meetings[seedKey]
			if seed.Status == Failed && seed.Reason == "bot_absent_on_restore" && seed.MeetingNumber == event.MeetingNumber && !m.state.Ended[seed.MeetingID] {
				// Use the configured seed's verified identity, retaining the old
				// uncertain receipt. The fresh request still needs a new probe.
				key, seedRecovery = seedKey, true
			}
		}
		if event.Kind == EntryInvite && key == "" {
			pending := m.state.Meetings[m.findLocked(event.MeetingNumber, "")]
			if pending.Status == "pending" && pending.MeetingID != "" && event.MeetingID != "" && pending.MeetingID != event.MeetingID {
				return errors.New("invitation conflicts with the pending meeting identity")
			}
		}
		// A recurring meeting can reuse its entry number with a new long ID.
		// Only a fresh explicit message without an old long ID may resolve it anew.
		previous := m.state.Meetings[key]
		released := previous.Status == "ended" || !previous.Held && (previous.Status == Failed || previous.Status == "skipped")
		if event.Kind == EntryMessage && event.MeetingID == "" && released && !seedRecovery {
			key = ""
		}
		entry, exists := m.state.Meetings[key]
		if !exists {
			if len(m.state.Meetings) >= 1000 {
				return errors.New("entry meeting retention limit reached")
			}
			key = event.ID
			entry = EntryMeeting{Key: key, MeetingID: event.MeetingID, MeetingNumber: event.MeetingNumber}
		}
		if entry.Status == "pending" && event.Kind == EntryInvite {
			if entry.MeetingID != "" && event.MeetingID != "" && entry.MeetingID != event.MeetingID {
				return errors.New("invitation conflicts with pending meeting identity")
			}
			// An IM entry can precede the invitation that carries VC correlation.
			// Enrich only before the worker freezes and persists its joining snapshot.
			if entry.CallID != "" && event.CallID != "" && entry.CallID != event.CallID {
				// Multiple invitations to the same target have different call IDs.
				// Retain the first pending correlation while acknowledging the source.
				observationStatus = "duplicate"
			} else {
				if entry.MeetingID == "" {
					entry.MeetingID = event.MeetingID
				}
				if entry.CallID == "" {
					entry.CallID = event.CallID
				}
				entry.EventID, entry.SenderOpenID = event.ID, event.SenderOpenID
				m.state.Meetings[key] = entry
				observationStatus = "pending"
			}
		} else if entry.Status != "ended" && entry.Status != "pending" && entry.Status != "joining" && entry.Status != "checking" && entry.Status != "starting" && entry.Status != "verifying" {
			entry.EventID, entry.SenderOpenID, entry.CallID = event.ID, event.SenderOpenID, event.CallID
			entry.MeetingNumber = event.MeetingNumber
			if entry.MeetingID == "" {
				entry.MeetingID = event.MeetingID
			}
			entry.NeedsCheck = entry.Held || entry.Status == "active" || entry.Status == Unknown || seedRecovery
			entry.Status, entry.Reason = "pending", ""
			if !entry.Held && m.slotsLocked() >= entryLimit {
				entry.Status, entry.Reason = Failed, "active_meeting_limit"
			}
			m.state.Meetings[key] = entry
			if entry.Status == "pending" {
				observationStatus = "pending"
			}
		} else if entry.Status != "ended" {
			observationStatus = "duplicate"
		}
	}
	m.state.Seen[event.ID] = true
	if err := m.saveLocked(); err != nil {
		return err
	}
	return nil
}

func (m *EntryManager) allowed(id string) bool {
	if id == m.config.BotOpenID {
		return false
	}
	for _, allowed := range m.config.AllowedSenderOpenIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (m *EntryManager) findLocked(number, id string) string {
	best, bestRank, bestExact := "", -1, false
	for key, entry := range m.state.Meetings {
		exact := id != "" && entry.MeetingID == id
		byNumber := number != "" && entry.MeetingNumber == number && (id == "" || entry.MeetingID == "" || entry.MeetingID == id)
		if !exact && !byNumber {
			continue
		}
		rank := m.lookupRankLocked(entry)
		if rank > bestRank || rank == bestRank && (exact && !bestExact || exact == bestExact && key < best) {
			best, bestRank, bestExact = key, rank, exact
		}
	}
	return best
}

// Keep historical records without allowing a failed seed or an old occurrence
// to shadow the current runtime. Exact identity then key order break equal ranks.
func (m *EntryManager) lookupRankLocked(entry EntryMeeting) int {
	if entry.Status == "ended" {
		return 0
	}
	runtime := m.runtimes[entry.Key]
	if runtime.ready && runtime.ctx != nil && runtime.ctx.Err() == nil {
		return 7
	}
	if entry.Status == "active" {
		return 6
	}
	switch entry.Status {
	case "pending", "checking", "starting", "verifying", "joining":
		return 5
	}
	if entry.Held {
		if entry.MeetingID != "" {
			return 4
		}
		return 3
	}
	if entry.Status == Unknown {
		return 2
	}
	return 1
}

func (m *EntryManager) slotsLocked() int {
	count := 0
	for _, entry := range m.state.Meetings {
		if entry.Held || entry.Status == "pending" || entry.Status == "joining" {
			count++
		}
	}
	return count
}

func (m *EntryManager) CanReply(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.state.Meetings[m.findLocked("", id)]
	runtime := m.runtimes[entry.Key]
	usable := entry.Status == "active" || entry.Status == "pending" || entry.Status == "checking" || entry.Status == "starting"
	return m.failure == nil && m.running && usable && entry.Held && runtime.ready && runtime.ctx != nil && runtime.ctx.Err() == nil
}

func (m *EntryManager) Snapshot() EntrySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := EntrySnapshot{Seen: len(m.state.Seen), Ready: m.ready && m.failure == nil, Meetings: []EntryMeeting{}}
	for _, entry := range m.state.Meetings {
		s.Meetings = append(s.Meetings, entry)
	}
	sort.Slice(s.Meetings, func(i, j int) bool { return s.Meetings[i].Key < s.Meetings[j].Key })
	return s
}

// ReportPollFailure may be called by the receiver that is about to exit. It
// never waits for that receiver's StopMeeting hook from inside its own callback.
// expectedRuntime is the exact StartMeeting context, not a derived poll context;
// a late callback from an older runtime cannot retire its replacement.
func (m *EntryManager) ReportPollFailure(ctx context.Context, id string, expectedRuntime ...context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.findLocked("", id)
	if key == "" || m.state.Meetings[key].Status == "ended" {
		return nil
	}
	if len(expectedRuntime) > 1 {
		return errors.New("poll failure requires at most one runtime context")
	}
	if len(expectedRuntime) == 1 && m.runtimes[key].ctx != expectedRuntime[0] {
		return nil
	}
	entry := m.state.Meetings[key]
	entry.Status, entry.Reason, entry.StopPending = Unknown, "poll_failed", true
	m.state.Meetings[key] = entry
	m.cancelLocked(key)
	if err := m.saveLocked(); err != nil {
		return err
	}
	m.signal()
	return nil
}

func (m *EntryManager) Run(ctx context.Context) (runErr error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("entry manager already running")
	}
	m.running = true
	var restored []string
	for key, entry := range m.state.Meetings {
		if entry.Status == "verifying" {
			restored = append(restored, key)
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running, m.ready = false, false
		var keys []string
		for key := range m.runtimes {
			m.cancelLocked(key)
			keys = append(keys, key)
		}
		m.mu.Unlock()
		for _, key := range keys {
			runErr = errors.Join(runErr, m.stop(key))
		}
	}()
	sort.Strings(restored)
	for _, key := range restored {
		if err := m.process(ctx, key); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.hooks.Observe("meeting.entry.ready", "ready", nil); err != nil {
		return err
	}
	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		if m.failure != nil {
			err := m.failure
			m.mu.Unlock()
			return err
		}
		var pending, stopping []string
		for key, entry := range m.state.Meetings {
			if entry.StopPending {
				stopping = append(stopping, key)
			} else if entry.Status == "pending" {
				pending = append(pending, key)
			}
		}
		m.mu.Unlock()
		sort.Strings(stopping)
		sort.Strings(pending)
		for _, key := range stopping {
			if err := m.stop(key); err != nil {
				return err
			}
		}
		for _, key := range pending {
			if err := m.process(ctx, key); err != nil {
				return err
			}
		}
		if len(pending)+len(stopping) != 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.wake:
		}
	}
}

func (m *EntryManager) process(ctx context.Context, key string) error {
	m.mu.Lock()
	entry := m.state.Meetings[key]
	if entry.Status != "pending" && entry.Status != "verifying" {
		m.mu.Unlock()
		return nil
	}
	restored := entry.Status == "verifying"
	if !restored && !m.allowed(entry.SenderOpenID) {
		entry.Status, entry.Reason = Failed, "sender_not_allowed"
		m.state.Meetings[key] = entry
		err := m.saveLocked()
		m.mu.Unlock()
		return err
	}
	check := entry.NeedsCheck || restored
	entry.Status = "checking"
	m.state.Meetings[key] = entry
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	if runtime := m.runtimes[key]; runtime.ctx == nil || runtime.ctx.Err() != nil {
		meetingCtx, cancel := context.WithCancel(ctx)
		m.runtimes[key] = entryRuntime{ctx: meetingCtx, cancel: cancel}
	}
	meetingCtx := m.runtimes[key].ctx
	m.mu.Unlock()
	if check {
		details, _ := readMeetingMembership(meetingCtx, m.config, m.client, entry.MeetingID, entry.MeetingNumber)
		if details.MeetingNumber != "" {
			m.mu.Lock()
			current := m.state.Meetings[key]
			var err error
			if current.MeetingID == entry.MeetingID && current.MeetingNumber == "" {
				current.MeetingNumber = details.MeetingNumber
				m.state.Meetings[key] = current
				err = m.saveLocked()
			}
			m.mu.Unlock()
			if err != nil {
				return err
			}
		}
		membership := details.Status
		if membership == "ended" {
			return m.finish(key, "ended", "membership_ended", false)
		}
		if membership == Unknown {
			return m.finish(key, Unknown, "membership_unconfirmed", true)
		}
		if membership == "present" {
			return m.start(meetingCtx, key, entry.MeetingID)
		}
		if err := m.stop(key); err != nil {
			return err
		}
		if restored {
			return m.finish(key, Failed, "bot_absent_on_restore", false)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	entry = m.state.Meetings[key]
	if entry.Status == "ended" {
		m.mu.Unlock()
		return nil
	}
	m.cancelLocked(key)
	meetingCtx, cancel := context.WithCancel(ctx)
	m.runtimes[key] = entryRuntime{ctx: meetingCtx, cancel: cancel}
	entry.Status, entry.Held = "joining", true
	m.state.Meetings[key] = entry
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := m.hooks.Observe("meeting.entry.joining", "joining", map[string]any{"event_id": entry.EventID, "meeting_id": entry.MeetingID, "meeting_number": entry.MeetingNumber}); err != nil {
		return err
	}
	id, outcome := joinMeeting(meetingCtx, m.config, m.client, entry)
	if outcome != Accepted {
		return m.finish(key, outcome, "join_"+outcome, outcome == Unknown)
	}
	m.mu.Lock()
	current := m.state.Meetings[key]
	if current.Status == "ended" || m.state.Ended[id] {
		m.mu.Unlock()
		return m.finish(key, "ended", "meeting_ended", false)
	}
	current.MeetingID = id
	m.state.Meetings[key] = current
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	membership, _ := meetingMembership(meetingCtx, m.config, m.client, id, entry.MeetingNumber)
	if membership == "ended" {
		return m.finish(key, "ended", "membership_ended", false)
	}
	if membership != "present" {
		return m.finish(key, Unknown, "joined_presence_unconfirmed", true)
	}
	return m.start(meetingCtx, key, id)
}

func (m *EntryManager) start(ctx context.Context, key, id string) error {
	m.mu.Lock()
	entry := m.state.Meetings[key]
	if entry.Status == "ended" || m.state.Ended[id] {
		m.mu.Unlock()
		return nil
	}
	entry.Status, entry.MeetingID, entry.Held = "starting", id, true
	m.state.Meetings[key] = entry
	alreadyReady := m.runtimes[key].ready
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	c := m.config
	c.MeetingID = id
	if !alreadyReady {
		if err := m.hooks.StartMeeting(ctx, c); err != nil {
			return m.finish(key, Unknown, "receiver_not_ready", true)
		}
	}
	m.mu.Lock()
	entry = m.state.Meetings[key]
	if entry.Status != "starting" || ctx.Err() != nil {
		m.mu.Unlock()
		return m.stop(key)
	}
	entry.Status, entry.Reason, entry.NeedsCheck = "active", "", false
	m.state.Meetings[key] = entry
	runtime := m.runtimes[key]
	runtime.ready = true
	m.runtimes[key] = runtime
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.hooks.Observe("meeting.entry.active", "ready", map[string]any{"meeting_id": id})
}

func (m *EntryManager) finish(key, status, reason string, held bool) error {
	m.mu.Lock()
	entry := m.state.Meetings[key]
	if entry.Status != "ended" {
		entry.Status, entry.Reason, entry.Held = status, reason, held
		if status == "ended" && entry.MeetingID != "" {
			m.state.Ended[entry.MeetingID] = true
		}
	}
	entry.StopPending = true
	m.state.Meetings[key] = entry
	m.cancelLocked(key)
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := m.stop(key); err != nil {
		return err
	}
	return m.hooks.Observe("meeting.entry.state", entry.Status, map[string]any{"meeting_id": entry.MeetingID, "reason": entry.Reason})
}

func (m *EntryManager) cancelLocked(key string) {
	if runtime := m.runtimes[key]; runtime.cancel != nil {
		runtime.cancel()
	}
}

func (m *EntryManager) stop(key string) error {
	m.mu.Lock()
	entry := m.state.Meetings[key]
	observeEnd := entry.Status == "ended" && entry.StopPending
	m.cancelLocked(key)
	delete(m.runtimes, key)
	m.mu.Unlock()
	if entry.MeetingID != "" {
		if err := m.hooks.StopMeeting(entry.MeetingID); err != nil {
			return errors.New("meeting runtime could not be stopped")
		}
	}
	m.mu.Lock()
	entry = m.state.Meetings[key]
	entry.StopPending = false
	m.state.Meetings[key] = entry
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if observeEnd {
		return m.hooks.Observe("meeting.entry.ended", "ended", map[string]any{"event_id": entry.EventID, "meeting_id": entry.MeetingID, "meeting_number": entry.MeetingNumber})
	}
	return nil
}

func (m *EntryManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *EntryManager) saveLocked() error {
	if m.failure != nil {
		return m.failure
	}
	dir := filepath.Dir(m.path)
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(dir, ".meeting-entry-*")
		if err == nil {
			defer os.Remove(file.Name())
			err = json.NewEncoder(file).Encode(m.state)
			if err == nil {
				err = file.Sync()
			}
			err = errors.Join(err, file.Close())
			if err == nil {
				err = os.Rename(file.Name(), m.path)
			}
		}
	}
	if err == nil {
		var directory *os.File
		directory, err = os.Open(dir)
		if err == nil {
			err = errors.Join(directory.Sync(), directory.Close())
		}
	}
	if err != nil {
		m.failure = errors.New("meeting entry state could not be committed")
		for key := range m.runtimes {
			m.cancelLocked(key)
		}
		m.signal()
		return m.failure
	}
	return nil
}
