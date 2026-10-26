// Package owneranswering coordinates explicitly delegated personal DM work.
// Native harnesses retain their tool loops; this package owns claims and delivery.
package owneranswering

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agent-host/internal/discovery"
	"agent-host/internal/state"
)

type Decision struct {
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	Clarification string `json:"clarification"`
}
type Outcome struct {
	Outcome       string `json:"outcome"`
	Text          string `json:"text"`
	Reason        string `json:"reason"`
	Clarification string `json:"clarification"`
	RunID         string `json:"-"`
	Warning       string `json:"-"`
}
type Receipt struct {
	Status     string          `json:"status"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	MessageIDs []string        `json:"message_ids,omitempty"`
	Error      string          `json:"error,omitempty"`
	TaskID     string          `json:"open_task_id,omitempty"`
	StatusRaw  json.RawMessage `json:"status_raw,omitempty"`
}
type Question struct {
	// OwnerActivity records actual bound outgoing messages, not viewing/read
	// receipts or a claim that the owner resolved the underlying problem.
	OwnerActivity  []OwnerActivity           `json:"owner_activity,omitempty"`
	ID             string                    `json:"id"`
	Generation     int                       `json:"generation"`
	Policy         string                    `json:"policy"`
	Source         discovery.Source          `json:"source"`
	Evidence       []state.DiscoveryEvidence `json:"evidence"`
	Clarification  string                    `json:"clarification,omitempty"`
	Status         string                    `json:"status"`
	Reason         string                    `json:"reason,omitempty"`
	Needed         string                    `json:"needed,omitempty"`
	Attempted      bool                      `json:"attempted"`
	RunID          string                    `json:"run_id,omitempty"`
	HandlerOutcome Outcome                   `json:"handler_outcome"`
	Warning        string                    `json:"warning,omitempty"`
	DraftEvidence  json.RawMessage           `json:"draft_evidence,omitempty"`
	OwnerTakeover  bool                      `json:"owner_takeover,omitempty"`
	Answer         string                    `json:"answer,omitempty"`
	Peer           Receipt                   `json:"peer"`
	Escalation     Receipt                   `json:"escalation"`
}

type OwnerActivity struct {
	MessageID  string    `json:"message_id"`
	EvidenceID string    `json:"evidence_id"`
	At         time.Time `json:"at"`
	Type       string    `json:"type"`
}

type messageObservation struct {
	MessageID string `json:"message_id"`
	Digest    string `json:"digest"`
}

func (q Question) Reference() string { return q.ID + "." + strconv.Itoa(q.Generation) }

type sourceState struct {
	ActivationBaseline time.Time `json:"activation_baseline"`
	Policy             string    `json:"policy"`
	Through            time.Time `json:"through"`
	Blocked            string    `json:"blocked,omitempty"`
	Active             string    `json:"active_question,omitempty"`
}
type Coordinator struct {
	Store     *state.Store
	Config    discovery.Config
	Provider  discovery.Provider
	Classify  func(context.Context, Question) (Decision, error)
	Handle    func(context.Context, Question) (Outcome, error)
	Draft     func(context.Context, Question) (Outcome, error)
	GetStatus func(context.Context, Question) (Receipt, error)
	Send      func(context.Context, Question, string) (Receipt, error)
	Notify    func(context.Context, Question, string) (Receipt, error)
	Validate  func(context.Context) error
	Now       func() time.Time
	Timeout   time.Duration
	gate      chan struct{}
	gateOnce  sync.Once
}

func (c *Coordinator) acquire(ctx context.Context) error {
	c.gateOnce.Do(func() { c.gate = make(chan struct{}, 1) })
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Coordinator) release() { <-c.gate }

func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:32]
}
func (c *Coordinator) policy() string {
	return hash(struct {
		Profile string
		Policy  discovery.OwnerAnsweringConfig
		Sources []discovery.Source
	}{c.Config.Profile, c.Config.OwnerAnswering, c.Config.Sources})
}
func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c *Coordinator) binding(s discovery.Source) (discovery.AnsweringBinding, bool) {
	selected := false
	for _, source := range c.Config.Sources {
		if source == s {
			selected = true
			break
		}
	}
	if !selected {
		return discovery.AnsweringBinding{}, false
	}
	for _, b := range c.Config.OwnerAnswering.Bindings {
		if s.Type == "dm" && s.ConversationID == b.ConversationID && s.PeerOpenDingTalkID == b.PeerOpenDingTalkID {
			return b, true
		}
	}
	return discovery.AnsweringBinding{}, false
}
func (c *Coordinator) save(ctx context.Context, r *state.OwnerAnsweringRecord, v any) error {
	r.Payload, _ = json.Marshal(v)
	return c.Store.CompareAndSaveOwnerAnswering(ctx, r)
}

// Activate runs once before polling. New policy revisions start at now, never
// historical imports. Recovery converts reserved effects to unknown, without replay.
func (c *Coordinator) Activate(ctx context.Context) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if !c.Config.OwnerAnswering.Enabled {
		return errors.New("owner answering disabled")
	}
	if err := c.Config.CheckOwnerAnswering(); err != nil {
		return err
	}
	records, err := c.Store.OwnerAnsweringRecords(ctx)
	if err != nil {
		return err
	}
	var outstanding []Question
	for _, r := range records {
		if !strings.HasPrefix(r.Key, "question:") {
			continue
		}
		var q Question
		if err = json.Unmarshal(r.Payload, &q); err != nil {
			return err
		}
		changed := false
		if q.Status == "executing" || q.Status == "ready" || q.Status == "drafting" {
			q.Status = "waiting_owner"
			q.Answer = ""
			q.Reason = "Execution or drafting was interrupted; tools will not be replayed."
			changed = true
		}
		if q.Peer.Status == "reserved" {
			q.Peer.Status = "unknown"
			q.Status = "waiting_owner"
			q.Answer = ""
			q.Reason = "Peer delivery was interrupted; inspect or reconcile without resending."
			changed = true
		}
		if q.Escalation.Status == "reserved" {
			q.Escalation.Status = "unknown"
			changed = true
		}
		if changed {
			if err = c.save(ctx, &r, q); err != nil {
				return err
			}
		}
		if !terminal(q) {
			outstanding = append(outstanding, q)
		}
	}
	for _, s := range c.Config.Sources {
		if _, ok := c.binding(s); !ok {
			continue
		}
		r, err := c.Store.OwnerAnsweringRecord(ctx, "source:"+s.Attribution(c.Config).ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var st sourceState
		if len(r.Payload) > 0 {
			if err = json.Unmarshal(r.Payload, &st); err != nil {
				return err
			}
		}
		if st.Policy != c.policy() {
			st.Policy = c.policy()
			st.Through = c.now()
			st.ActivationBaseline = st.Through
			st.Blocked = ""
		}
		if st.ActivationBaseline.IsZero() {
			st.ActivationBaseline = st.Through
			if st.ActivationBaseline.IsZero() {
				st.ActivationBaseline = c.now()
			}
		}
		// Preserve unresolved effects across policy changes; never create a fresh
		// reply slot merely because the configured policy changed.
		for _, q := range outstanding {
			if q.Source.ConversationID == s.ConversationID {
				if q.Source != s {
					return errors.New("unresolved question target/project changed; reconcile the existing question first")
				}
				if st.Active != "" && st.Active != q.ID {
					return errors.New("multiple outstanding questions require owner reconciliation")
				}
				st.Active = q.ID
			}
		}
		if err = c.save(ctx, &r, st); err != nil {
			return err
		}
	}
	return nil
}

func terminal(q Question) bool {
	if q.Peer.Status == "reserved" || q.Peer.Status == "unknown" || q.Peer.Status == "provider_pending" {
		return false
	}
	switch q.Status {
	case "replied", "ignored", "dismissed", "taken_over", "delivery_failed":
		return true
	}
	return false
}

func (c *Coordinator) intake(ctx context.Context, sr *state.OwnerAnsweringRecord, st sourceState, qr *state.OwnerAnsweringRecord, q *Question, observations ...*state.OwnerAnsweringRecord) error {
	sr.Payload, _ = json.Marshal(st)
	records := []*state.OwnerAnsweringRecord{sr}
	if q != nil {
		qr.Payload, _ = json.Marshal(q)
		records = append(records, qr)
	}
	records = append(records, observations...)
	return c.Store.CompareAndSaveOwnerAnsweringBatch(ctx, records...)
}

func recordOwnerActivity(q *Question, e state.DiscoveryEvidence) {
	for _, prior := range q.OwnerActivity {
		if prior.EvidenceID == e.ID {
			return
		}
	}
	q.OwnerActivity = append(q.OwnerActivity, OwnerActivity{MessageID: e.MessageID, EvidenceID: e.ID, At: e.CreatedAt, Type: "outgoing_message"})
}

func author(e state.DiscoveryEvidence, a discovery.AnsweringAuthor) bool {
	return e.AuthorID == a.ID && e.SenderType == a.Type && (a.Type != "" || a.VerifiedContact)
}
func (c *Coordinator) valid(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.Config.OwnerAnswering.Enabled {
		return errors.New("owner answering disabled")
	}
	if c.Validate != nil {
		return c.Validate(ctx)
	}
	return nil
}

// Consume accepts only complete attributed provider windows. It does not receive
// a bot webhook or synthesize permission from an observed message.
func (c *Coordinator) Consume(ctx context.Context, s discovery.Source, col discovery.Collection) error {
	ctx, cancel := c.attemptContext(ctx)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	b, ok := c.binding(s)
	if !ok {
		return nil
	}
	if err := c.valid(ctx); err != nil {
		return err
	}
	if !col.Coverage.Complete || col.Coverage.Partial || col.Coverage.HasMore || col.Coverage.Error != "" {
		return errors.New("answering requires complete two-way history")
	}
	sr, err := c.Store.OwnerAnsweringRecord(ctx, "source:"+s.Attribution(c.Config).ID)
	if err != nil {
		return err
	}
	var st sourceState
	if err = json.Unmarshal(sr.Payload, &st); err != nil {
		return err
	}
	if st.Policy != c.policy() {
		return errors.New("answering policy changed; reactivate before intake")
	}
	if st.ActivationBaseline.IsZero() {
		st.ActivationBaseline = st.Through
	}
	var q *Question
	var qr state.OwnerAnsweringRecord
	if st.Active != "" {
		qr, err = c.Store.OwnerAnsweringRecord(ctx, "question:"+st.Active)
		if err != nil {
			return err
		}
		q = &Question{}
		if err = json.Unmarshal(qr.Payload, q); err != nil {
			return err
		}
		if q.Source != s {
			return errors.New("active question source binding mismatch")
		}
		if q.Policy != c.policy() {
			if terminal(*q) {
				q = nil
				st.Active = ""
			} else {
				return errors.New("active question has older policy; owner reconciliation required")
			}
		}
		if q != nil && q.Peer.TaskID != "" && (q.Peer.Status == "provider_pending" || q.Peer.Status == "unknown") {
			if err = c.reconcile(ctx, &qr, q); err != nil {
				return err
			}
		}
		if q != nil && terminal(*q) {
			q = nil
			st.Active = ""
		}
	}
	known, err := c.knownAI(ctx)
	if err != nil {
		return err
	}
	assigned, err := c.assignedMessages(ctx, s)
	if err != nil {
		return err
	}
	existing := map[string]state.DiscoveryEvidence{}
	if q != nil {
		for _, e := range q.Evidence {
			existing[e.MessageID] = e
		}
	}
	var peers, owners []state.DiscoveryEvidence
	var observations []*state.OwnerAnsweringRecord
	seenWindow := map[string]bool{}
	for _, e := range col.Evidence {
		_, activeMessage := existing[e.MessageID]
		if e.CreatedAt.Before(st.ActivationBaseline) && !activeMessage {
			continue
		}
		key := "message:" + hash([]string{s.Attribution(c.Config).ID, e.MessageID})
		if seenWindow[key] {
			return errors.New("duplicate provider message in intake window")
		}
		seenWindow[key] = true
		observation, readErr := c.Store.OwnerAnsweringRecord(ctx, key)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		var prior messageObservation
		if len(observation.Payload) > 0 {
			if err = json.Unmarshal(observation.Payload, &prior); err != nil {
				return err
			}
		}
		unchanged := prior.MessageID == e.MessageID && prior.Digest == e.Digest
		reason := ""
		if !c.attributed(s, e) {
			reason = "blocked_identity: invalid source attribution"
		}
		if reason == "" && known[s.ConversationID+"\x00"+e.MessageID] {
			continue
		}
		if reason == "" && !author(e, b.Owner) && !author(e, b.Peer) {
			reason = "blocked_identity: unbound author ID or conflicting sender_type"
		}
		if reason != "" {
			st.Blocked = reason
			if q != nil {
				q.Answer = ""
				q.Status = "waiting_owner"
				q.Reason = reason
			}
			if err = c.intake(ctx, &sr, st, &qr, q); err != nil {
				return err
			}
			return errors.New(reason)
		}
		if !unchanged {
			observation.Payload, _ = json.Marshal(messageObservation{MessageID: e.MessageID, Digest: e.Digest})
			observations = append(observations, &observation)
		}
		if author(e, b.Owner) {
			owners = append(owners, e)
			continue
		}
		if unchanged {
			continue
		}
		if (e.MessageType == "" || e.MessageType == "text") && strings.TrimSpace(e.Text) != "" && (len(e.Forwarded) == 0 || string(e.Forwarded) == "null" || string(e.Forwarded) == "false") {
			if activeMessage || !assigned[e.MessageID] {
				peers = append(peers, e)
			}
		}
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].CreatedAt.Equal(peers[j].CreatedAt) {
			return peers[i].MessageID < peers[j].MessageID
		}
		return peers[i].CreatedAt.Before(peers[j].CreatedAt)
	})
	changed := false
	if q == nil && len(peers) > 0 {
		q = &Question{ID: hash([]string{s.Attribution(c.Config).ID, peers[0].MessageID}), Generation: 1, Policy: c.policy(), Source: s, Status: "pending"}
		qr = state.OwnerAnsweringRecord{Key: "question:" + q.ID}
		// Stable message anchoring prevents reuse even if a cursor was restored.
		if prior, e := c.Store.OwnerAnsweringRecord(ctx, qr.Key); e == nil {
			qr = prior
			if e = json.Unmarshal(qr.Payload, q); e != nil {
				return e
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		st.Active = q.ID
		changed = true
	}
	if q != nil {
		positions := map[string]int{}
		for i, e := range q.Evidence {
			positions[e.MessageID] = i
		}
		for _, e := range peers {
			if i, exists := positions[e.MessageID]; exists {
				if q.Evidence[i].ID != e.ID {
					q.Evidence[i] = e
					changed = true
				}
			} else {
				positions[e.MessageID] = len(q.Evidence)
				q.Evidence = append(q.Evidence, e)
				changed = true
			}
		}
		if changed {
			if len(existing) > 0 {
				q.Generation++
			}
			q.Answer = ""
			if q.Peer.Status == "" {
				if q.Attempted {
					q.Status = "waiting_owner"
					q.Reason = "Additional peer details retained; prior tools will not be replayed."
				} else {
					q.Status = "pending"
				}
			}
		}
		if len(q.Evidence) > 0 {
			first := q.Evidence[0].CreatedAt
			for _, e := range q.Evidence {
				if e.CreatedAt.Before(first) {
					first = e.CreatedAt
				}
			}
			for _, e := range owners {
				if !e.CreatedAt.Before(first) {
					recordOwnerActivity(q, e)
					q.OwnerTakeover = true
					q.Answer = ""
					if q.Peer.Status == "" {
						q.Status = "taken_over"
					}
					q.Reason = "owner takeover observed"
				}
			}
		}
	}
	st.Blocked = ""
	if col.Coverage.To.After(st.Through) {
		st.Through = col.Coverage.To
	}
	// Watermark, active link and complete question revision commit together
	// BEFORE classification or consequential tools.
	if err = c.intake(ctx, &sr, st, &qr, q, observations...); err != nil {
		return err
	}
	if q == nil {
		return nil
	}
	if q.Policy != c.policy() {
		return errors.New("active question has older policy; owner reconciliation required")
	}
	if q.Status == "pending" {
		return c.evaluate(ctx, &qr, q, false)
	}
	if q.Status == "waiting_owner" && q.Escalation.Status == "" {
		return c.escalate(ctx, &qr, q)
	}
	return nil
}

func (c *Coordinator) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = time.Duration(c.Config.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}
func finishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
}

// Message membership survives cursor-save interruption and overlapping windows.
// A changed burst must never package an already attempted request as a new task.
func (c *Coordinator) assignedMessages(ctx context.Context, s discovery.Source) (map[string]bool, error) {
	records, err := c.Store.OwnerAnsweringRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range records {
		if !strings.HasPrefix(r.Key, "question:") {
			continue
		}
		var q Question
		if err = json.Unmarshal(r.Payload, &q); err != nil {
			return nil, err
		}
		if q.Source == s {
			for _, e := range q.Evidence {
				out[e.MessageID] = true
			}
		}
	}
	return out, nil
}
func (c *Coordinator) attributed(s discovery.Source, e state.DiscoveryEvidence) bool {
	a := s.Attribution(c.Config)
	p := state.PrepareDiscoveryEvidence(e)
	return e.SourceID == a.ID && e.Profile == a.Profile && e.PrincipalID == a.PrincipalID && e.TenantID == a.TenantID && e.ProjectID == s.ProjectID && e.ConversationID == s.ConversationID && e.Availability == "available" && e.ID == p.ID && e.Digest == p.Digest
}
func (c *Coordinator) knownAI(ctx context.Context) (map[string]bool, error) {
	r, err := c.Store.OwnerAnsweringRecords(ctx)
	out := map[string]bool{}
	if err != nil {
		return nil, err
	}
	for _, v := range r {
		if !strings.HasPrefix(v.Key, "question:") {
			continue
		}
		var q Question
		if err = json.Unmarshal(v.Payload, &q); err != nil {
			return nil, err
		}
		if q.Policy == c.policy() && q.Peer.Status == "accepted" {
			for _, id := range q.Peer.MessageIDs {
				out[q.Source.ConversationID+"\x00"+id] = true
			}
		}
	}
	return out, nil
}
func (c *Coordinator) wait(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question, reason, needed string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if strings.TrimSpace(reason) == "" {
		reason = "The request has no supported handling result."
	}
	if strings.TrimSpace(needed) == "" {
		needed = "Clarify the intended outcome or inspect the recorded effects and take over."
	}
	q.Status = "waiting_owner"
	q.Answer = ""
	q.Reason = reason
	q.Needed = needed
	if err := c.save(ctx, r, q); err != nil {
		return err
	}
	return c.escalate(ctx, r, q)
}
func (c *Coordinator) evaluate(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question, draftOnly bool) (resultErr error) {
	ctx, cancel := c.attemptContext(ctx)
	defer cancel()
	if err := c.valid(ctx); err != nil {
		return c.cancelOrWait(ctx, r, q, err)
	}
	if q.Peer.Status != "" {
		return c.wait(ctx, r, q, "A peer delivery slot has already been attempted; it cannot be replayed.", "Inspect the saved delivery status without resending.")
	}
	if q.OwnerTakeover {
		return c.cancelOrWait(ctx, r, q, errors.New("owner takeover observed"))
	}
	if err := c.fresh(ctx, q); err != nil {
		return c.cancelOrWait(ctx, r, q, err)
	}
	if q.Attempted && !draftOnly {
		return c.wait(ctx, r, q, q.Reason, "Clarify privately with an explicit answer for tool-free drafting, or take over.")
	}
	if !draftOnly {
		d, err := c.Classify(ctx, *q)
		if ctx.Err() != nil {
			return c.wait(ctx, r, q, "Evaluation canceled before handling: "+ctx.Err().Error(), "Clarify or take over; no peer reply was sent.")
		}
		if err != nil {
			return c.wait(ctx, r, q, "Classification failed.", "Please clarify the intended request.")
		}
		switch d.Action {
		case "ignore":
			q.Status = "ignored"
			return c.save(ctx, r, q)
		case "ask_owner":
			return c.wait(ctx, r, q, d.Reason, d.Clarification)
		case "handle":
		default:
			return c.wait(ctx, r, q, "Invalid classification result.", "Please clarify the intended request.")
		}
	}
	if err := c.valid(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return c.wait(ctx, r, q, err.Error(), "Clarify or take over.")
	}
	if draftOnly {
		if c.Draft == nil {
			return c.wait(ctx, r, q, "Tool-free drafting is unavailable.", "Please take over; tools will not be replayed.")
		}
		q.Status = "drafting"
	} else {
		q.Attempted = true
		q.Status = "executing"
	}
	if err := c.save(ctx, r, q); err != nil {
		return err
	}
	var o Outcome
	var err error
	if draftOnly {
		o, err = c.Draft(ctx, *q)
	} else {
		o, err = c.Handle(ctx, *q)
	}
	if o.RunID != "" {
		q.RunID = o.RunID
	}
	if !draftOnly {
		q.HandlerOutcome = o
	}
	if o.Warning != "" {
		q.Warning = o.Warning
	}
	if err != nil {
		q.Warning = strings.TrimSpace(q.Warning + " " + err.Error())
	}
	// Save the observed outcome even when the invocation canceled its parent.
	finish, done := finishContext(ctx)
	defer done()
	if saveErr := c.save(finish, r, q); saveErr != nil {
		return saveErr
	}
	if ctx.Err() != nil {
		return c.wait(ctx, r, q, "Evaluation canceled: "+ctx.Err().Error(), "Review retained effects and provide a private answer or take over; tools will not be replayed.")
	}
	if err != nil || o.Outcome != "answer" || strings.TrimSpace(o.Text) == "" || len(o.Text) > 15000 {
		reason := "Handling failed or blocked: " + o.Reason
		if err != nil {
			q.Warning = err.Error()
		}
		return c.wait(ctx, r, q, reason, o.Clarification)
	}
	q.Answer = "AI assistant replying for Owner / AI 代 Owner 回复\n\n" + o.Text
	q.Status = "ready"
	if err = c.save(ctx, r, q); err != nil {
		return c.wait(ctx, r, q, "Could not finalize the answer before deadline.", "Review retained evidence; tools will not be replayed.")
	}
	return c.deliver(ctx, r, q)
}

func (c *Coordinator) deliver(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question) error {
	if q.Peer.Status != "" {
		return c.wait(ctx, r, q, "Peer delivery already attempted; no new slot is allowed.", "Inspect the existing delivery receipt.")
	}
	if err := c.fresh(ctx, q); err != nil {
		return c.cancelOrWait(ctx, r, q, err)
	}
	if err := c.valid(ctx); err != nil {
		return c.cancelOrWait(ctx, r, q, err)
	}
	if err := ctx.Err(); err != nil {
		return c.wait(ctx, r, q, "Evaluation canceled before peer send: "+err.Error(), "Review the retained draft privately.")
	}
	q.Status = "sending"
	q.Peer.Status = "reserved"
	if err := c.save(ctx, r, q); err != nil {
		return c.wait(ctx, r, q, "Delivery reservation failed before sending.", "Inspect the question state.")
	}
	// A canceled evaluation must never start a transport call.
	if err := ctx.Err(); err != nil {
		q.Peer.Status = "unknown"
		return c.wait(ctx, r, q, "Evaluation canceled at delivery reservation: "+err.Error(), "Inspect the saved reservation; no automatic replay.")
	}
	receipt, sendErr := c.Send(ctx, *q, "oa-"+q.ID)
	q.Peer = normalize(receipt, sendErr)
	finish, done := finishContext(ctx)
	defer done()
	switch q.Peer.Status {
	case "accepted":
		q.Status = "replied"
		return c.save(finish, r, q)
	case "provider_pending":
		q.Status = "delivery_pending"
		q.Peer.MessageIDs = nil
		return c.save(finish, r, q)
	default:
		return c.wait(finish, r, q, "Peer delivery is "+q.Peer.Status+"; do not replay.", "Inspect or reconcile the saved delivery receipt.")
	}
}

// Reconciliation is read-only. The original operation receipt and task ID are
// retained separately from the latest status response; no send callback is used.
func (c *Coordinator) reconcile(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question) error {
	if c.GetStatus == nil {
		return nil
	}
	ctx, cancel := c.attemptContext(ctx)
	defer cancel()
	if err := c.valid(ctx); err != nil {
		return err
	}
	receipt, err := c.GetStatus(ctx, *q)
	prior := q.Peer
	next := normalize(receipt, err)
	if next.TaskID != "" && next.TaskID != prior.TaskID {
		next.Status = "unknown"
		next.Error = "status task identity mismatch"
	}
	prior.Status = next.Status
	prior.StatusRaw = next.Raw
	prior.Error = next.Error
	if next.Status == "accepted" {
		prior.MessageIDs = append([]string(nil), next.MessageIDs...)
	} else {
		prior.MessageIDs = nil
	}
	q.Peer = prior
	finish, done := finishContext(ctx)
	defer done()
	switch prior.Status {
	case "accepted":
		q.Status = "replied"
		if q.OwnerTakeover {
			q.Status = "taken_over"
		}
		return c.save(finish, r, q)
	case "provider_pending":
		q.Status = "delivery_pending"
		return c.save(finish, r, q)
	case "failed":
		q.Status = "delivery_failed"
		q.Reason = "Provider reported failed peer delivery; no fresh send is allowed."
		q.Needed = "Inspect the retained receipt and take over."
		if err = c.save(finish, r, q); err != nil {
			return err
		}
		return c.escalate(finish, r, q)
	default:
		return c.wait(finish, r, q, "Peer delivery status is unknown; no fresh send is allowed.", "Inspect the retained task/status evidence.")
	}
}

func (c *Coordinator) cancelOrWait(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question, cause error) error {
	if cause.Error() == "owner takeover observed" {
		ctx, cancel := finishContext(ctx)
		defer cancel()
		q.Status = "taken_over"
		q.OwnerTakeover = true
		q.Reason = cause.Error()
		q.Answer = ""
		return c.save(ctx, r, q)
	}
	return c.wait(ctx, r, q, cause.Error(), "Inspect the latest conversation and clarify or take over.")
}
func normalize(r Receipt, err error) Receipt {
	if err != nil {
		r.Error = err.Error()
	}
	switch r.Status {
	case "accepted", "provider_pending", "failed", "unknown":
	default:
		r.Status = "unknown"
	}
	if err != nil && r.Status == "accepted" {
		r.Status = "unknown"
	}
	return r
}
func (c *Coordinator) escalate(ctx context.Context, r *state.OwnerAnsweringRecord, q *Question) error {
	ctx, cancel := finishContext(ctx)
	defer cancel()
	if q.Escalation.Status != "" {
		return nil
	}
	if err := c.valid(ctx); err != nil {
		return err
	}
	q.Escalation.Status = "reserved"
	if err := c.save(ctx, r, q); err != nil {
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Owner clarification needed [%s]\nPeer: %s\nConversation: %s\nSource question:\n", q.Reference(), q.Source.PeerOpenDingTalkID, q.Source.ConversationID)
	var excerpts strings.Builder
	for _, e := range q.Evidence {
		fmt.Fprintf(&excerpts, "%s (%s), message %s: %s\n", e.AuthorID, bounded(string(e.Author), 120), e.MessageID, bounded(e.Text, 800))
	}
	text.WriteString(bounded(excerpts.String(), 1600))
	fmt.Fprintf(&text, "\nBlocked: %s\nNeeded: %s\nReply /answer %s TEXT or /dismiss %s. Clarification is evaluated privately before any peer reply. Full source evidence remains in the question record.", bounded(q.Reason, 400), bounded(q.Needed, 400), q.Reference(), q.Reference())
	message := text.String()
	receipt, err := c.Notify(ctx, *q, message)
	q.Escalation = normalize(receipt, err)
	finish, done := finishContext(ctx)
	defer done()
	return c.save(finish, r, q)
}
func bounded(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "…"
}

func (c *Coordinator) fresh(ctx context.Context, q *Question) error {
	if len(q.Evidence) == 0 {
		return errors.New("question evidence is unavailable")
	}
	if err := c.valid(ctx); err != nil {
		return err
	}
	if q.Policy != c.policy() {
		return errors.New("answering policy changed")
	}
	b, ok := c.binding(q.Source)
	if !ok {
		return errors.New("answering target changed")
	}
	from := q.Evidence[0].CreatedAt
	for _, e := range q.Evidence {
		if e.CreatedAt.Before(from) {
			from = e.CreatedAt
		}
	}
	to := c.now()
	if !to.After(from) {
		to = from.Add(time.Millisecond)
	}
	col, err := c.Provider.Fetch(ctx, q.Source, discovery.Range{From: from, To: to})
	if err != nil {
		return fmt.Errorf("fresh history unavailable: %w", err)
	}
	if !col.Coverage.Complete || col.Coverage.Partial || col.Coverage.HasMore || col.Coverage.Error != "" || !col.Coverage.From.Equal(from) || !col.Coverage.To.Equal(to) {
		return errors.New("fresh history is incomplete")
	}
	known, err := c.knownAI(ctx)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, e := range q.Evidence {
		expected[e.ID] = true
	}
	seen := map[string]bool{}
	for _, e := range col.Evidence {
		if !c.attributed(q.Source, e) {
			return errors.New("fresh history attribution mismatch")
		}
		if known[q.Source.ConversationID+"\x00"+e.MessageID] {
			continue
		}
		if author(e, b.Owner) {
			recordOwnerActivity(q, e)
			return errors.New("owner takeover observed")
		}
		if !author(e, b.Peer) {
			return errors.New("blocked_identity: fresh author or type unbound")
		}
		if !expected[e.ID] {
			return errors.New("new or changed peer evidence; stale draft canceled")
		}
		seen[e.ID] = true
	}
	if len(seen) != len(expected) {
		return errors.New("fresh history omitted question evidence")
	}
	return nil
}

// Clarify is called only after authenticated owner-private channel admission.
// The explicit reference includes the revision, so stale responses cannot guess.
func (c *Coordinator) Clarify(ctx context.Context, text string) (bool, string, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || (fields[0] != "/answer" && fields[0] != "/dismiss") {
		return false, "", nil
	}
	ctx, cancel := c.attemptContext(ctx)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return true, "Clarification timed out before evaluation; no tools or sends were replayed.", err
	}
	defer c.release()
	if len(fields) < 2 {
		return true, "Use /answer QUESTION.REVISION TEXT or /dismiss QUESTION.REVISION.", nil
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != 2 {
		return true, "A question and revision reference is required.", nil
	}
	r, err := c.Store.OwnerAnsweringRecord(ctx, "question:"+parts[0])
	if errors.Is(err, sql.ErrNoRows) {
		return true, "Unknown question reference; nothing was sent.", nil
	}
	if err != nil {
		return true, "", err
	}
	var q Question
	if err = json.Unmarshal(r.Payload, &q); err != nil {
		return true, "", err
	}
	if q.Reference() != fields[1] || terminal(q) {
		return true, "Stale or closed question reference; current reference: " + q.Reference() + ". Nothing was sent.", nil
	}
	if fields[0] == "/dismiss" {
		q.Status = "dismissed"
		if q.Peer.Status != "" {
			q.OwnerTakeover = true
			return true, "Question dismissed; an existing delivery may still complete. It will only be reconciled, never resent.", c.save(ctx, &r, q)
		}
		return true, "Question dismissed; no peer reply.", c.save(ctx, &r, q)
	}
	if q.Policy != c.policy() || q.Status != "waiting_owner" {
		return true, "Question requires policy/delivery reconciliation; nothing was sent.", nil
	}
	if len(fields) < 3 {
		return true, "Include clarification text after the exact question reference.", nil
	}
	if q.Peer.Status != "" {
		return true, "Peer delivery was already attempted. Inspect or reconcile its receipt; it will not be replayed.", nil
	}
	q.Clarification = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/answer")), fields[1]))
	q.Generation++
	q.Answer = ""
	q.Status = "pending"
	if err = c.save(ctx, &r, q); err != nil {
		return true, "", err
	}
	err = c.evaluate(ctx, &r, &q, q.Attempted)
	return true, "Clarification recorded for " + q.Reference() + "; current state: " + q.Status + ".", err
}
