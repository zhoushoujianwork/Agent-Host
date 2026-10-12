package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EnqueueChannelMessage commits receipt before transport acknowledgement. A
// repeated transport message never changes the original route or execution policy.
func (s *Store) EnqueueChannelMessage(ctx context.Context, m ChannelMessage, replyURL string, inputCapabilities ...[]string) (bool, error) {
	if m.ConversationType != "" && m.ConversationType != "1" && m.ConversationType != "2" {
		return false, errors.New("unsupported channel conversation type")
	}
	if m.ConversationType == "2" && (m.AgentID == "" || m.AudienceID == "" || m.AudienceID == OwnerAudienceID) {
		return false, errors.New("group channel message requires an explicit non-owner partition")
	}
	for name, value := range map[string]string{
		"ID": m.ID, "channel": m.Channel, "account": m.AccountID, "tenant": m.TenantID,
		"message": m.MessageID, "conversation": m.ConversationID, "sender": m.SenderID,
		"profile": m.Profile, "binding": m.Binding, "scope": m.Scope,
		"message type": m.MessageType,
	} {
		if strings.TrimSpace(value) == "" {
			return false, fmt.Errorf("channel message %s is required", name)
		}
	}
	if m.MessageType == "text" && strings.TrimSpace(m.Text) == "" {
		return false, errors.New("channel text message cannot be empty")
	}
	if len(m.Text) > 16*1024 {
		return false, errors.New("channel message text exceeds 16 KiB")
	}
	if m.ReplyTarget == nil {
		if strings.TrimSpace(replyURL) == "" {
			return false, errors.New("channel message reply URL is required")
		}
		if m.ReplyExpiresAt.IsZero() {
			return false, errors.New("channel reply expiration is required")
		}
	} else {
		target := m.ReplyTarget
		if target.Kind != ReplyTargetFeishuMeeting || m.Channel != target.Kind || target.ID == "" || target.ID != m.ConversationID || strings.TrimSpace(target.UUID) == "" || len(target.UUID) > 128 {
			return false, errors.New("channel API reply target is invalid")
		}
		if replyURL != "" || !m.ReplyExpiresAt.IsZero() || !m.DisableReactions || m.ConversationType != "2" {
			return false, errors.New("meeting reply requires a shared conversation without webhook expiry or reactions")
		}
	}
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now().UTC()
	}
	m.Status, m.ReplyStatus = "queued", "pending"
	m.SessionID, m.RunID, m.Error, m.ReplyText, m.ReplyError = "", "", "", "", ""
	m.Reactions = nil
	m.ProjectID, m.Workspace, m.ResolvedScope, m.RuntimeFingerprint, m.RouteBinding = "", "", "", "", ""
	m.Projects, m.AdditionalDirectories, m.Supplements = nil, nil, nil
	m.RoutingDecision = nil
	var codes []string
	if len(inputCapabilities) > 1 {
		return false, errors.New("multiple channel input capability lists")
	}
	if len(inputCapabilities) == 1 {
		codes = inputCapabilities[0]
	}
	if m.Input != nil && len(m.Input.Attachments) != len(codes) || m.Input == nil && len(codes) != 0 {
		return false, errors.New("attachment capabilities differ from receipt")
	}
	if len(codes) > 4 {
		return false, errors.New("too many attachments")
	}
	for _, code := range codes {
		if strings.TrimSpace(code) == "" || len(code) > 8192 {
			return false, errors.New("invalid attachment capability")
		}
	}
	m.Input = receiptInput(m.Input)
	capabilities, err := json.Marshal(codes)
	if err != nil {
		return false, err
	}
	inserted := false
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var payload []byte
		err := tx.QueryRowContext(ctx, "SELECT payload FROM channel_messages WHERE id=? OR (channel=? AND account_id=? AND message_id=?)", m.ID, m.Channel, m.AccountID, m.MessageID).Scan(&payload)
		if err == nil {
			var old ChannelMessage
			if err = json.Unmarshal(payload, &old); err != nil {
				return err
			}
			if conversationType(old.ConversationType) != conversationType(m.ConversationType) || old.ID != m.ID || old.Channel != m.Channel || old.AccountID != m.AccountID || old.TenantID != m.TenantID || old.MessageID != m.MessageID || old.ConversationID != m.ConversationID || old.SenderID != m.SenderID || old.MessageType != m.MessageType {
				return errors.New("channel message identity or content collision")
			}
			if (old.ReplyTarget == nil) != (m.ReplyTarget == nil) || old.ReplyTarget != nil && *old.ReplyTarget != *m.ReplyTarget || old.DisableReactions != m.DisableReactions {
				return errors.New("channel reply destination or feedback capability collision")
			}
			forwardedMarkdown := old.MessageType == "markdown" && strings.TrimSpace(m.Text) != "" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "markdown" && strings.TrimSpace(m.Input.Quote.Body) != ""
			legacyUnsupported := (old.MessageType == "richText" || (old.MessageType == "picture" || old.MessageType == "file") && m.Input != nil && len(m.Input.Attachments) > 0 || forwardedMarkdown) && old.Text == "" && old.Input == nil && old.Status == "failed" && old.Error == "unsupported message type" && old.RunID == "" && old.SessionID == "" && old.RouteBinding == ""
			if !sameInput(old.Input, m.Input) && !legacyUnsupported {
				return errors.New("channel input material collision")
			}
			if old.Text != m.Text && !legacyUnsupported {
				// Earlier parsers discarded richText before the host explicitly
				// rejected it. Upgraded redelivery remains a duplicate, with no
				// payload repair, execution, or second reply.
				legacyRejection := old.MessageType == "richText" && old.Text == "" &&
					old.Status == "failed" && old.Error == "unsupported message type" &&
					old.SessionID == "" && old.RunID == "" && old.RouteBinding == "" && strings.TrimSpace(m.Text) != ""
				if !legacyRejection {
					return errors.New("channel message identity or content collision")
				}
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		payload, err = json.Marshal(m)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload,input_capabilities) VALUES(?,?,?,?,?,?,?,?,?,?)", m.ID, m.Channel, m.AccountID, m.MessageID, m.Profile, m.Status, m.ReceivedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), replyURL, payload, capabilities)
		inserted = err == nil
		if err != nil {
			return err
		}
		return queueChannelReaction(ctx, tx, m, "received")
	})
	return inserted && err == nil, wrap("enqueue channel message", err)
}

func readChannelMessage(ctx context.Context, q rowQuerier, id string) (ChannelMessage, error) {
	var m ChannelMessage
	var payload []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM channel_messages WHERE id=?", id).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &m)
	}
	return m, wrap("read channel message", err)
}

func writeChannelMessage(ctx context.Context, tx *sql.Tx, m ChannelMessage) error {
	m.Reactions = nil
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE channel_messages SET status=?,payload=? WHERE id=?", m.Status, payload, m.ID)
	return wrap("update channel message", err)
}

func (s *Store) ChannelMessage(ctx context.Context, id string) (ChannelMessage, error) {
	m, err := readChannelMessage(ctx, s.db, id)
	if err != nil {
		return m, err
	}
	m.Reactions, err = s.ChannelReactions(ctx, id)
	return m, err
}

// ChannelMessages returns metadata only, newest receipt first.
func (s *Store) ChannelMessages(ctx context.Context, limit int) ([]ChannelMessage, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("channel message limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM channel_messages ORDER BY received_at DESC,rowid DESC LIMIT ?", limit)
	if err != nil {
		return nil, wrap("list channel messages", err)
	}
	defer rows.Close()
	messages := []ChannelMessage{}
	for rows.Next() {
		var payload []byte
		var m ChannelMessage
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &m); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].Reactions, err = s.ChannelReactions(ctx, messages[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// ClaimNextChannelMessage selects the oldest queued input in this configuration.
// Claiming is durable before a native run is created.
func (s *Store) ClaimNextChannelMessage(ctx context.Context, profile string) (ChannelMessage, error) {
	return s.ClaimNextEligibleChannelMessage(ctx, profile, nil)
}

// ClaimNextEligibleChannelMessage excludes active bindings and memory partitions.
// Different conversations sharing an agent/audience cannot finalize against stale
// memory snapshots. Legacy missing ownership fields normalize to default/owner.
func (s *Store) ClaimNextEligibleChannelMessage(ctx context.Context, profile string, busy []ChannelMessage) (ChannelMessage, error) {
	var m ChannelMessage
	if strings.TrimSpace(profile) == "" {
		return m, errors.New("channel profile is required")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var id string
		query := "SELECT id FROM channel_messages WHERE profile=? AND status='queued'"
		args := []any{profile}
		for _, active := range busy {
			agent, audience := normalizedPartition(active.AgentID, active.AudienceID)
			query += " AND NOT (json_extract(CAST(payload AS TEXT),'$.binding')=? OR (COALESCE(NULLIF(json_extract(CAST(payload AS TEXT),'$.agent_id'),''),'default')=? AND COALESCE(NULLIF(json_extract(CAST(payload AS TEXT),'$.audience_id'),''),'owner')=?))"
			args = append(args, active.Binding, agent, audience)
		}
		query += " ORDER BY received_at,rowid LIMIT 1"
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			return err
		}
		var err error
		m, err = readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		m.Status = "claimed"
		if err := writeChannelMessage(ctx, tx, m); err != nil {
			return err
		}
		return queueChannelReaction(ctx, tx, m, "processing")
	})
	return m, wrap("claim channel message", err)
}

// ChannelReplyURL exposes the capability only to the delivery path. It is never
// included in message payloads, run provenance, or inspection results.
func (s *Store) ChannelReplyURL(ctx context.Context, id string) (string, error) {
	var replyURL string
	err := s.db.QueryRowContext(ctx, "SELECT reply_url FROM channel_messages WHERE id=?", id).Scan(&replyURL)
	return replyURL, wrap("read channel reply URL", err)
}

func (s *Store) ChannelSession(ctx context.Context, binding string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT session_id FROM channel_sessions WHERE binding=?", binding).Scan(&id)
	return id, wrap("read channel session", err)
}

// BindChannelSession is also used for an explicit new-session request. It never
// changes or resumes native state itself.
func (s *Store) BindChannelSession(ctx context.Context, binding, sessionID string) error {
	if strings.TrimSpace(binding) == "" || strings.TrimSpace(sessionID) == "" {
		return errors.New("channel binding and session ID are required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readSession(ctx, tx, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO channel_sessions(binding,session_id) VALUES(?,?) ON CONFLICT(binding) DO UPDATE SET session_id=excluded.session_id", binding, sessionID)
		return wrap("bind channel session", err)
	})
}

// RecoverChannelSession atomically records why the new session exists and swaps
// its binding. No prior run, native transcript, or pending memory is rewritten.
func (s *Store) RecoverChannelSession(ctx context.Context, binding, previousID, sessionID, runID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var boundID string
		if err := tx.QueryRowContext(ctx, "SELECT session_id FROM channel_sessions WHERE binding=?", binding).Scan(&boundID); err != nil {
			return err
		}
		if boundID != previousID || previousID == sessionID {
			return errors.New("session recovery binding changed")
		}
		var cause *SessionResumeError
		if err := sessionCanResume(ctx, tx, previousID); !errors.As(err, &cause) || !cause.CanStartFresh() || cause.RunID != runID {
			return errors.New("session is not eligible for automatic recovery")
		}
		session, err := readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		previous, err := readSession(ctx, tx, previousID)
		if err != nil {
			return err
		}
		if !samePartition(previous.AgentID, previous.AudienceID, session.AgentID, session.AudienceID) {
			return errors.New("session recovery cannot cross an agent or audience")
		}
		var used bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE session_id=?)", sessionID).Scan(&used); err != nil {
			return err
		}
		if used || session.PreviousSessionID != "" {
			return errors.New("session recovery requires an unused new session")
		}
		session.PreviousSessionID, session.RecoveryRunID = previousID, runID
		payload, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET payload=? WHERE id=?", payload, sessionID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE channel_sessions SET session_id=? WHERE binding=?", sessionID, binding)
		return err
	})
}

// ResolveChannelRoute pins execution only after a queued message is claimed.
// Receipt's Binding and Scope remain the original queue and policy identity.
func (s *Store) ResolveChannelRoute(ctx context.Context, messageID, routeBinding, projectID, workspace, scope, fingerprint string) error {
	return s.ResolveChannelProjects(ctx, messageID, routeBinding, projectID, workspace, scope, fingerprint, nil, nil)
}

func (s *Store) ResolveChannelProjects(ctx context.Context, messageID, routeBinding, projectID, workspace, scope, fingerprint string, projects []ProjectRef, additionalDirectories []string) error {
	if strings.TrimSpace(routeBinding) == "" || strings.TrimSpace(scope) == "" || strings.TrimSpace(fingerprint) == "" {
		return errors.New("resolved route binding, scope and runtime fingerprint are required")
	}
	path, err := CanonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if m.Status != "claimed" || m.RunID != "" {
			return errors.New("route resolution requires a claimed message without a previous run")
		}
		if err := validateChannelProjects(ctx, tx, projects, path, additionalDirectories); err != nil {
			return err
		}
		if projectID != "" {
			p, err := readProject(ctx, tx, projectID)
			if err != nil {
				return err
			}
			if p.Workspace != path || p.Scope != scope {
				return errors.New("resolved route does not match its registered project")
			}
		}
		if m.RouteBinding != "" {
			if m.RouteBinding == routeBinding && m.ProjectID == projectID && m.Workspace == path && m.ResolvedScope == scope && m.RuntimeFingerprint == fingerprint && sameProjectRefs(m.Projects, projects) && sameDirectories(m.AdditionalDirectories, additionalDirectories) {
				return nil
			}
			return errors.New("resolved channel route cannot be changed")
		}
		var sessionID string
		err = tx.QueryRowContext(ctx, "SELECT session_id FROM channel_sessions WHERE binding=?", routeBinding).Scan(&sessionID)
		if err == nil {
			session, err := readSession(ctx, tx, sessionID)
			if err != nil {
				return err
			}
			if !samePartition(session.AgentID, session.AudienceID, m.AgentID, m.AudienceID) || session.ProjectID != projectID || session.WorkingDirectory() != path || session.Scope != scope || session.RuntimeFingerprint != fingerprint || !sameProjectRefs(session.Projects, projects) || !sameDirectories(session.AdditionalDirectories, additionalDirectories) {
				return errors.New("resolved route session does not match runtime")
			}
			m.SessionID = sessionID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		m.RouteBinding, m.ProjectID, m.Workspace, m.ResolvedScope, m.RuntimeFingerprint = routeBinding, projectID, path, scope, fingerprint
		m.Projects, m.AdditionalDirectories = append([]ProjectRef(nil), projects...), append([]string(nil), additionalDirectories...)
		if err := writeChannelMessage(ctx, tx, m); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE channel_messages SET route_order=(SELECT COALESCE(MAX(route_order),0)+1 FROM channel_messages) WHERE id=?", m.ID)
		return err
	})
}

// ChannelRoute returns the latest pinned execution environment for the original
// channel binding. Unresolved messages do not hide its compatibility information.
func (s *Store) ChannelRoute(ctx context.Context, binding, projectID string) (ChannelMessage, error) {
	var m ChannelMessage
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM channel_messages WHERE json_extract(payload,'$.binding')=? AND COALESCE(json_extract(payload,'$.project_id'),'')=? AND COALESCE(json_extract(payload,'$.route_binding'),'')<>'' ORDER BY route_order DESC,received_at DESC,rowid DESC LIMIT 1`, binding, projectID).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &m)
	}
	return m, wrap("read channel route", err)
}

// CreateChannelRun atomically links a claimed message to its only native run.
// Provenance is derived from durable receipt, never accepted from a caller.
func (s *Store) CreateChannelRun(ctx context.Context, run Run, messageID string) error {
	if err := s.assignPartition(&run.AgentID, &run.AudienceID); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if !samePartition(m.AgentID, m.AudienceID, run.AgentID, run.AudienceID) {
			return errors.New("channel run does not match its agent and audience")
		}
		if m.Status != "claimed" || m.RunID != "" {
			return errors.New("channel run requires a claimed message without a previous run")
		}
		binding, scope := m.Binding, m.Scope
		if m.RouteBinding != "" {
			binding, scope = m.RouteBinding, m.ResolvedScope
			if m.ProjectID != run.ProjectID || m.Workspace != run.Workspace || m.RuntimeFingerprint != run.RuntimeFingerprint || !sameProjectRefs(m.Projects, run.Projects) || !sameDirectories(m.AdditionalDirectories, run.AdditionalDirectories) {
				return errors.New("channel run does not match its resolved project, workspace or runtime configuration")
			}
		}
		if m.SessionID != "" && m.SessionID != run.SessionID {
			return errors.New("channel run does not match its pinned native session")
		}
		var sessionID string
		if err = tx.QueryRowContext(ctx, "SELECT session_id FROM channel_sessions WHERE binding=?", binding).Scan(&sessionID); err != nil {
			return wrap("read channel run binding", err)
		}
		if sessionID != run.SessionID || scope != run.Scope || m.Text != run.Input {
			return errors.New("channel run does not match its message session, scope, or text")
		}
		run.Source = &RunSource{Input: m.Input, AgentID: run.AgentID, AudienceID: run.AudienceID, ConversationType: m.ConversationType, Channel: m.Channel, ChannelMessageID: m.ID, AccountID: m.AccountID, TenantID: m.TenantID, MessageID: m.MessageID, ConversationID: m.ConversationID, SenderID: m.SenderID}
		run.MemoryAttribution, err = memoryAttributionForChannel(ctx, tx, m)
		if err != nil {
			return err
		}
		run.Supplements = append([]UserSupplement(nil), m.Supplements...)
		if err = createRun(ctx, tx, run); err != nil {
			return err
		}
		m.SessionID, m.RunID = run.SessionID, run.ID
		return writeChannelMessage(ctx, tx, m)
	})
}

func (s *Store) FinishChannelMessage(ctx context.Context, id, status, errorText string) error {
	if status != "completed" && status != "failed" {
		return fmt.Errorf("invalid channel message terminal status %q", status)
	}
	if status == "failed" && strings.TrimSpace(errorText) == "" || status == "completed" && errorText != "" {
		return errors.New("channel failure requires an error; completion cannot include an error")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != "claimed" {
			return errors.New("only claimed channel messages can finish")
		}
		m.Status, m.Error = status, errorText
		return writeChannelMessage(ctx, tx, m)
	})
}

// PrepareChannelReply commits the exact outbound text before attempting delivery.
// An interrupted send cannot be retried implicitly through this API.
func (s *Store) PrepareChannelReply(ctx context.Context, id, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("channel reply text is required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != "completed" && m.Status != "failed" && m.Status != "unknown" && m.Status != "awaiting" {
			return errors.New("channel reply requires a terminal message")
		}
		if m.ReplyStatus != "pending" {
			return errors.New("channel reply was already prepared; replay is forbidden")
		}
		m.ReplyStatus, m.ReplyText = "sending", text
		if err := writeChannelMessage(ctx, tx, m); err != nil {
			return err
		}
		if m.ConversationType != "" {
			return nil
		}
		return queueChannelReaction(ctx, tx, m, "replying")
	})
}

func (s *Store) FinishChannelReply(ctx context.Context, id, status, errorText string) error {
	if status != "accepted" && status != "failed" && status != "unknown" {
		return fmt.Errorf("invalid channel reply status %q", status)
	}
	if status == "accepted" && errorText != "" || status != "accepted" && strings.TrimSpace(errorText) == "" {
		return errors.New("channel reply failure or unknown outcome requires an error; acceptance cannot include an error")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.ReplyStatus != "sending" {
			return errors.New("only sending channel replies can finish")
		}
		m.ReplyStatus, m.ReplyError = status, errorText
		if err := writeChannelMessage(ctx, tx, m); err != nil {
			return err
		}
		if m.Status != "awaiting" {
			if err := queueChannelReaction(ctx, tx, m, terminalReactionStage(m)); err != nil {
				return err
			}
		}
		return finishChoiceReply(ctx, tx, m)
	})
}

// RecoverChannels preserves queued inputs, but never replays claimed execution
// or an attempted reply. Call alongside Recover before accepting new work.
func (s *Store) RecoverChannels(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT payload FROM channel_messages")
		if err != nil {
			return err
		}
		var changes []ChannelMessage
		for rows.Next() {
			var payload []byte
			var m ChannelMessage
			if err = rows.Scan(&payload); err != nil {
				return errors.Join(err, rows.Close())
			}
			if err = json.Unmarshal(payload, &m); err != nil {
				return errors.Join(err, rows.Close())
			}
			changed := false
			if m.Status == "claimed" {
				m.Status = "unknown"
				m.Error = "host stopped after claiming the message; execution was not replayed"
				changed = true
			}
			if m.ReplyStatus == "sending" {
				m.ReplyStatus = "unknown"
				m.ReplyError = "host stopped before recording delivery acceptance; reply was not replayed"
				changed = true
			}
			if changed {
				changes = append(changes, m)
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, m := range changes {
			if err = writeChannelMessage(ctx, tx, m); err != nil {
				return err
			}
		}
		if err := recoverChannelChoices(ctx, tx); err != nil {
			return err
		}
		return recoverChannelReactions(ctx, tx)
	})
}
