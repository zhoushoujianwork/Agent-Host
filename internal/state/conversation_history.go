package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const ConversationHistoryPolicy = "conversation-history-v1"

// ErrHistoryUnavailable identifies a receipt whose attribution cannot authorize
// history. The business task may still execute without this capability.
var ErrHistoryUnavailable = errors.New("conversation history is unavailable for this task scope")

// ConversationHistoryBinding is a receipt-derived capability. Callers cannot
// choose identities or grants; every operation derives them again from MessageID.
type ConversationHistoryBinding struct {
	MessageID   string `json:"-"`
	Fingerprint string `json:"-"`
}

type historyGrant struct {
	ID    string
	Scope string
}

type historyScope struct {
	Channel, Account, Tenant, Agent, Audience, Conversation, ConversationType string
	Grants                                                                    []historyGrant
}

type historyAuthority struct {
	historyScope
	Snapshot int64
}

type ConversationHistorySearch struct {
	Query  string    `json:"query"`
	After  time.Time `json:"after,omitempty"`
	Before time.Time `json:"before,omitempty"`
	Limit  int       `json:"limit"`
	Cursor string    `json:"cursor,omitempty"`
}

type ConversationHistoryRead struct {
	Reference      string `json:"reference"`
	Before         int    `json:"before"`
	After          int    `json:"after"`
	Offset         int    `json:"offset"`
	Limit          int    `json:"limit"`
	ExpectedDigest string `json:"expected_content_sha256,omitempty"`
}

// HistoryText uses Unicode character offsets into the exact stored text. Each
// material has its own length; a read's offset applies independently to all.
type HistoryText struct {
	Text       string `json:"text"`
	Characters int    `json:"characters"`
	Offset     int    `json:"offset"`
	NextOffset *int   `json:"next_offset,omitempty"`
	Partial    bool   `json:"partial"`
}

type HistoryQuote struct {
	MessageID   string      `json:"message_id,omitempty"`
	MessageType string      `json:"message_type,omitempty"`
	Attribution string      `json:"attribution"`
	Body        HistoryText `json:"body"`
	Title       HistoryText `json:"title"`
	SummaryOnly bool        `json:"summary_only"`
	Truncated   bool        `json:"source_truncated"`
}

type HistoryAttachment struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"reported_size,omitempty"`
}

// HistoryTurn deliberately omits raw runs, errors, routing audits, local paths,
// reply capabilities and transport credentials. Reply is saved ReplyText only.
type HistoryTurn struct {
	Reference       string              `json:"reference"`
	ReceiptID       string              `json:"receipt_id"`
	MessageID       string              `json:"message_id"`
	RunID           string              `json:"run_id,omitempty"`
	ConversationID  string              `json:"conversation_id"`
	AuthorID        string              `json:"author_id"`
	ReplyAuthor     string              `json:"reply_author"`
	ReceivedAt      time.Time           `json:"received_at"`
	SentAt          time.Time           `json:"source_sent_at,omitempty"`
	Kind            string              `json:"kind"`
	MessageType     string              `json:"message_type"`
	Projects        []string            `json:"projects"`
	ReceiptStatus   string              `json:"receipt_status"`
	ExecutionStatus string              `json:"execution_status"`
	DeliveryStatus  string              `json:"delivery_status"`
	Input           HistoryText         `json:"input"`
	Reply           HistoryText         `json:"reply"`
	Quote           *HistoryQuote       `json:"quote,omitempty"`
	Attachments     []HistoryAttachment `json:"attachments,omitempty"`
	Digest          string              `json:"full_content_sha256"`
}

type HistoryCoverage struct {
	Source          string   `json:"source"`
	ConversationID  string   `json:"conversation_id"`
	AgentID         string   `json:"agent_id"`
	AudienceID      string   `json:"audience_id"`
	Projects        []string `json:"projects"`
	Snapshot        int64    `json:"receipt_snapshot"`
	CurrentExcluded bool     `json:"current_request_excluded"`
	Partial         bool     `json:"partial"`
	Limitations     []string `json:"limitations"`
}

type ConversationHistoryResult struct {
	Turns      []HistoryTurn   `json:"turns"`
	Coverage   HistoryCoverage `json:"coverage"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func historyFingerprint(scope historyScope) string {
	raw, _ := json.Marshal(struct {
		Policy string
		Scope  historyScope
	}{ConversationHistoryPolicy, scope})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ConversationHistoryBinding resolves only a durable admitted channel receipt.
// Its fingerprint excludes receipt/run/session IDs, permitting native continuation.
func (s *Store) ConversationHistoryBinding(ctx context.Context, messageID string) (ConversationHistoryBinding, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ConversationHistoryBinding{}, err
	}
	defer tx.Rollback()
	authority, err := historyCurrent(ctx, tx, messageID)
	if err != nil {
		return ConversationHistoryBinding{}, err
	}
	return ConversationHistoryBinding{MessageID: messageID, Fingerprint: historyFingerprint(authority.historyScope)}, nil
}

func historyCurrent(ctx context.Context, tx *sql.Tx, messageID string) (historyAuthority, error) {
	var authority historyAuthority
	if strings.TrimSpace(messageID) == "" {
		return authority, ErrHistoryUnavailable
	}
	var rowID int64
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT rowid,payload FROM channel_messages WHERE id=?", messageID).Scan(&rowID, &raw); err != nil {
		return authority, fmt.Errorf("history receipt unavailable: %w", err)
	}
	var m ChannelMessage
	if json.Unmarshal(raw, &m) != nil || m.ID != messageID || !historyIdentityValid(m) {
		return authority, ErrHistoryUnavailable
	}
	run, err := historyRun(ctx, tx, m)
	if err != nil {
		return authority, err
	}
	grants, known, err := historyGrants(ctx, tx, m, run)
	if err != nil || !known {
		return authority, ErrHistoryUnavailable
	}
	agent, audience := normalizedPartition(m.AgentID, m.AudienceID)
	authority.historyScope = historyScope{Channel: m.Channel, Account: m.AccountID, Tenant: m.TenantID,
		Agent: agent, Audience: audience, Conversation: m.ConversationID, ConversationType: conversationType(m.ConversationType), Grants: grants}
	authority.Snapshot = rowID - 1
	return authority, nil
}

func historyIdentityValid(m ChannelMessage) bool {
	supported := m.Channel == "dingtalk" || m.Channel == ReplyTargetFeishuMeeting && m.ReplyTarget != nil &&
		m.ReplyTarget.Kind == ReplyTargetFeishuMeeting && m.ReplyTarget.ID == m.ConversationID && m.ReplyTarget.UUID != "" && m.DisableReactions && m.ConversationType == "2"
	if !supported || m.AccountID == "" || m.TenantID == "" || m.ConversationID == "" || m.MessageID == "" || m.SenderID == "" || m.Binding == "" || m.Scope == "" || m.ReceivedAt.IsZero() {
		return false
	}
	if m.ConversationType != "" && m.ConversationType != "1" && m.ConversationType != "2" {
		return false
	}
	return m.ConversationType != "2" || m.AgentID != "" && m.AudienceID != "" && m.AudienceID != OwnerAudienceID
}

func historyRun(ctx context.Context, tx *sql.Tx, m ChannelMessage) (*Run, error) {
	if m.RunID == "" {
		return nil, nil
	}
	var raw []byte
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT execution_status,payload FROM runs WHERE id=?", m.RunID).Scan(&status, &raw); err != nil {
		return nil, errors.New("history execution evidence unavailable")
	}
	var run Run
	if json.Unmarshal(raw, &run) != nil || run.Source == nil {
		return nil, errors.New("history execution evidence is invalid")
	}
	source := run.Source
	scope := m.Scope
	if m.RouteBinding != "" {
		scope = m.ResolvedScope
	}
	if run.ID != m.RunID || run.SessionID != m.SessionID || run.ExecutionStatus != status || run.Scope != scope || run.Input != m.Text || !samePartition(run.AgentID, run.AudienceID, m.AgentID, m.AudienceID) ||
		source.ChannelMessageID != m.ID || source.Channel != m.Channel || source.AccountID != m.AccountID || source.TenantID != m.TenantID || source.MessageID != m.MessageID || source.ConversationID != m.ConversationID || source.SenderID != m.SenderID || !samePartition(source.AgentID, source.AudienceID, m.AgentID, m.AudienceID) || conversationType(source.ConversationType) != conversationType(m.ConversationType) {
		return nil, errors.New("history execution scope does not match its receipt")
	}
	if m.RouteBinding != "" && (run.ProjectID != m.ProjectID || !sameProjectRefs(run.Projects, m.Projects) || run.Workspace != m.Workspace || run.RuntimeFingerprint != m.RuntimeFingerprint) {
		return nil, errors.New("history execution projects do not match their receipt")
	}
	return &run, nil
}

func historyGrants(ctx context.Context, tx *sql.Tx, m ChannelMessage, run *Run) ([]historyGrant, bool, error) {
	attribution, err := memoryAttributionForChannel(ctx, tx, m)
	if err != nil {
		return nil, false, err
	}
	if run != nil && run.MemoryAttribution != attribution {
		return nil, false, nil
	}
	if run == nil && m.RouteBinding == "" {
		return nil, false, nil
	}
	scope, projectID, projects := m.ResolvedScope, m.ProjectID, m.Projects
	if m.RouteBinding == "" && run != nil {
		scope, projectID, projects = run.Scope, run.ProjectID, run.Projects
	}
	if scope == "" {
		return nil, false, nil
	}
	// Historical unresolved handoffs are readable in their saved default scope.
	// Their execution and skipped learning records remain unchanged.
	if attribution == "unresolved" && (scope != m.Scope || projectID != "" || len(projects) != 0) {
		return nil, false, nil
	}
	var grants []historyGrant
	seen := map[string]bool{}
	for _, project := range projects {
		if project.ID == "" || project.Scope == "" || seen[project.ID] {
			return nil, false, errors.New("invalid history project evidence")
		}
		seen[project.ID] = true
		grants = append(grants, historyGrant{ID: project.ID, Scope: project.Scope})
	}
	if projectID != "" && len(projects) != 0 && !seen[projectID] {
		return nil, false, errors.New("invalid history primary project evidence")
	}
	if len(grants) == 0 {
		if projectID == "" {
			projectID = "default"
		}
		grants = []historyGrant{{ID: projectID, Scope: scope}}
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	return grants, true, nil
}

type historyRecord struct {
	Order int64
	Turn  HistoryTurn
}

// historyView performs audience and project eligibility before matching, limits,
// pagination or neighbors. It never uses the Host-wide administrative queue API.
func historyView(ctx context.Context, tx *sql.Tx, binding ConversationHistoryBinding) (historyAuthority, []historyRecord, error) {
	authority, err := historyCurrent(ctx, tx, binding.MessageID)
	if err != nil {
		return authority, nil, err
	}
	if binding.Fingerprint == "" || historyFingerprint(authority.historyScope) != binding.Fingerprint {
		return authority, nil, errors.New("conversation history access changed; start a new session")
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid,id,channel,account_id,message_id,payload FROM channel_messages
		WHERE rowid<=? AND channel=? AND account_id=?
		AND json_extract(payload,'$.tenant_id')=? AND json_extract(payload,'$.conversation_id')=?
		AND COALESCE(NULLIF(json_extract(payload,'$.agent_id'),''),'default')=?
		AND COALESCE(NULLIF(json_extract(payload,'$.audience_id'),''),'owner')=?
		AND COALESCE(NULLIF(json_extract(payload,'$.conversation_type'),''),'1')=? ORDER BY rowid DESC`,
		authority.Snapshot, authority.Channel, authority.Account, authority.Tenant, authority.Conversation, authority.Agent, authority.Audience, authority.ConversationType)
	if err != nil {
		return authority, nil, errors.New("conversation history source unavailable")
	}
	type received struct {
		order int64
		m     ChannelMessage
	}
	var receipts []received
	for rows.Next() {
		var order int64
		var id, channel, account, message string
		var raw []byte
		if err = rows.Scan(&order, &id, &channel, &account, &message, &raw); err != nil {
			rows.Close()
			return authority, nil, errors.New("conversation history source unreadable")
		}
		var m ChannelMessage
		if json.Unmarshal(raw, &m) != nil || m.ID != id || m.Channel != channel || m.AccountID != account || m.MessageID != message {
			rows.Close()
			return authority, nil, errors.New("conversation history receipt is invalid")
		}
		if historyIdentityValid(m) {
			receipts = append(receipts, received{order, m})
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return authority, nil, errors.New("conversation history source unreadable")
	}
	records := []historyRecord{}
	for _, receipt := range receipts {
		m := receipt.m
		run, err := historyRun(ctx, tx, m)
		if err != nil {
			return authority, nil, err
		}
		grants, known, err := historyGrants(ctx, tx, m, run)
		if err != nil {
			return authority, nil, errors.New("conversation history attribution is invalid")
		}
		kind := "task"
		if !known {
			// Unrouted chatter, queued work and /status can contain unproven
			// project material. Only generic standalone controls are safe here.
			control := strings.TrimSpace(m.Text)
			if m.Input != nil || run != nil || m.RouteBinding != "" || len(authority.Grants) != 1 || authority.Grants[0].ID != "default" || authority.Grants[0].Scope != m.Scope || control != "/help" && control != "/new" {
				continue
			}
			grants, kind = []historyGrant{{ID: "default", Scope: m.Scope}}, "control"
		}
		allowed := true
		for _, needed := range grants {
			found := false
			for _, grant := range authority.Grants {
				found = found || grant == needed
			}
			allowed = allowed && found
		}
		if !allowed {
			continue
		}
		turn := historyTurn(m, run, grants, kind)
		records = append(records, historyRecord{Order: receipt.order, Turn: turn})
	}
	return authority, records, nil
}

func historyTurn(m ChannelMessage, run *Run, grants []historyGrant, kind string) HistoryTurn {
	agent, _ := normalizedPartition(m.AgentID, m.AudienceID)
	turn := HistoryTurn{Reference: "host-history:" + m.ID, ReceiptID: m.ID, MessageID: m.MessageID, RunID: m.RunID,
		ConversationID: m.ConversationID, AuthorID: m.SenderID, ReplyAuthor: agent, ReceivedAt: m.ReceivedAt, SentAt: m.SentAt,
		Kind: kind, MessageType: m.MessageType, Projects: []string{}, ReceiptStatus: m.Status, ExecutionStatus: "not_started",
		DeliveryStatus: m.ReplyStatus, Input: historyExcerpt(m.Text, 0, -1), Reply: historyExcerpt(m.ReplyText, 0, -1)}
	if run != nil {
		turn.ExecutionStatus = run.ExecutionStatus
	}
	for _, grant := range grants {
		turn.Projects = append(turn.Projects, grant.ID)
	}
	if m.Input != nil {
		for _, attachment := range m.Input.Attachments {
			turn.Attachments = append(turn.Attachments, HistoryAttachment{Kind: boundedMetadata(attachment.Kind), Name: boundedMetadata(attachment.Name), MediaType: boundedMetadata(attachment.MediaType), Size: attachment.Size})
		}
		if q := m.Input.Quote; q != nil {
			turn.Quote = &HistoryQuote{MessageID: boundedMetadata(q.MessageID), MessageType: boundedMetadata(q.MessageType),
				Attribution: "quoted material; original author unavailable in callback", Body: historyExcerpt(q.Body, 0, -1), Title: historyExcerpt(q.Title, 0, -1), SummaryOnly: q.SummaryOnly, Truncated: q.Truncated}
		}
	}
	raw, _ := json.Marshal(turn)
	digest := sha256.Sum256(raw)
	turn.Digest = hex.EncodeToString(digest[:])
	return turn
}

func boundedMetadata(value string) string {
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256])
	}
	return value
}

func historyExcerpt(value string, offset, limit int) HistoryText {
	runes := []rune(value)
	length := len(runes)
	start := min(offset, length)
	end := length
	if limit >= 0 {
		end = min(start+limit, length)
	}
	result := HistoryText{Text: string(runes[start:end]), Characters: length, Offset: offset, Partial: offset != 0 || end < length}
	if end < length {
		result.NextOffset = &end
	}
	return result
}

func historyBound(turn HistoryTurn, offset, limit int) HistoryTurn {
	turn.Input = historyExcerpt(turn.Input.Text, offset, limit)
	turn.Reply = historyExcerpt(turn.Reply.Text, offset, limit)
	if turn.Quote != nil {
		quote := *turn.Quote
		quote.Body = historyExcerpt(quote.Body.Text, offset, limit)
		quote.Title = historyExcerpt(quote.Title.Text, offset, limit)
		turn.Quote = &quote
	}
	return turn
}

func historySnippet(turn HistoryTurn, query string) HistoryTurn {
	excerpt := func(value string) HistoryText {
		offset := 0
		if query != "" {
			lower := strings.ToLower(value)
			if index := strings.Index(lower, strings.ToLower(query)); index >= 0 {
				offset = max(0, len([]rune(lower[:index]))-80)
			}
		}
		return historyExcerpt(value, offset, 240)
	}
	turn.Input, turn.Reply = excerpt(turn.Input.Text), excerpt(turn.Reply.Text)
	if turn.Quote != nil {
		quote := *turn.Quote
		quote.Body, quote.Title = excerpt(quote.Body.Text), excerpt(quote.Title.Text)
		turn.Quote = &quote
	}
	return turn
}

func historyPartial(turn HistoryTurn) bool {
	return turn.Input.Partial || turn.Reply.Partial || turn.Quote != nil && (turn.Quote.Body.Partial || turn.Quote.Title.Partial || turn.Quote.Truncated)
}

func historyCoverage(authority historyAuthority) HistoryCoverage {
	projects := []string{}
	for _, grant := range authority.Grants {
		projects = append(projects, grant.ID)
	}
	return HistoryCoverage{Source: "admitted Agent-Host channel receipts and saved replies", ConversationID: authority.Conversation,
		AgentID: authority.Agent, AudienceID: authority.Audience, Projects: projects, Snapshot: authority.Snapshot, CurrentExcluded: true,
		Limitations: []string{"Only earlier admitted messages in this transport conversation and current project grants are covered.",
			"Unaddressed group traffic, unavailable platform history and unrouted work are excluded; historical default handoffs stay within their saved scope, and generic /help and /new controls appear only in default scope.",
			"Provider sent times are source evidence; ordering uses durable Host receipt order. Prepared replies retain independent delivery status; accepted does not prove read.",
			"Quote authors are unavailable in stored callbacks; quoted text is untrusted supporting material. Attachment bodies and administrative payloads are omitted."}}
}

type historyCursor struct {
	Snapshot int64  `json:"snapshot"`
	Before   int64  `json:"before"`
	Query    string `json:"query"`
}

func historyQueryDigest(binding ConversationHistoryBinding, query ConversationHistorySearch) string {
	query.Cursor, query.Limit = "", 0
	raw, _ := json.Marshal(struct {
		Binding string
		Query   ConversationHistorySearch
	}{binding.Fingerprint, query})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func historyMatch(turn HistoryTurn, query ConversationHistorySearch) bool {
	if !query.After.IsZero() && turn.ReceivedAt.Before(query.After) || !query.Before.IsZero() && !turn.ReceivedAt.Before(query.Before) {
		return false
	}
	needle := strings.ToLower(query.Query)
	if needle == "" {
		return true
	}
	values := []string{turn.Input.Text, turn.Reply.Text}
	if turn.Quote != nil {
		values = append(values, turn.Quote.Body.Text, turn.Quote.Title.Text)
	}
	for _, attachment := range turn.Attachments {
		values = append(values, attachment.Name, attachment.Kind, attachment.MediaType)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func (s *Store) SearchConversationHistory(ctx context.Context, binding ConversationHistoryBinding, query ConversationHistorySearch) (ConversationHistoryResult, error) {
	var result ConversationHistoryResult
	query.Query = strings.TrimSpace(query.Query)
	if query.Limit < 1 || query.Limit > 20 || len([]rune(query.Query)) > 256 || len(query.Cursor) > 512 || !query.After.IsZero() && !query.Before.IsZero() && !query.After.Before(query.Before) {
		return result, errors.New("invalid history search bounds")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, errors.New("conversation history source unavailable")
	}
	defer tx.Rollback()
	authority, records, err := historyView(ctx, tx, binding)
	if err != nil {
		return result, err
	}
	before, queryDigest := authority.Snapshot+1, historyQueryDigest(binding, query)
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		var cursor historyCursor
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Snapshot != authority.Snapshot || cursor.Before < 1 || cursor.Before > authority.Snapshot || cursor.Query != queryDigest {
			return result, errors.New("history cursor does not match this query and receipt snapshot")
		}
		before = cursor.Before
	}
	result.Turns, result.Coverage = []HistoryTurn{}, historyCoverage(authority)
	var last int64
	for _, record := range records {
		if record.Order >= before || !historyMatch(record.Turn, query) {
			continue
		}
		if len(result.Turns) == query.Limit {
			result.Coverage.Partial = true
			raw, _ := json.Marshal(historyCursor{Snapshot: authority.Snapshot, Before: last, Query: queryDigest})
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		turn := historySnippet(record.Turn, query.Query)
		result.Turns = append(result.Turns, turn)
		result.Coverage.Partial = result.Coverage.Partial || historyPartial(turn)
		last = record.Order
	}
	return result, nil
}

func (s *Store) ReadConversationHistory(ctx context.Context, binding ConversationHistoryBinding, query ConversationHistoryRead) (ConversationHistoryResult, error) {
	var result ConversationHistoryResult
	if !strings.HasPrefix(query.Reference, "host-history:") || len(query.Reference) > 512 || query.Before < 0 || query.Before > 3 || query.After < 0 || query.After > 3 || query.Offset < 0 || query.Limit < 1 || query.Limit > 2048 || query.Offset > 0 && (query.ExpectedDigest == "" || query.Before != 0 || query.After != 0) {
		return result, errors.New("invalid history read bounds; continuation requires an unchanged digest and no neighbors")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, errors.New("conversation history source unavailable")
	}
	defer tx.Rollback()
	authority, records, err := historyView(ctx, tx, binding)
	if err != nil {
		return result, err
	}
	index := -1
	for i, record := range records {
		if record.Turn.Reference == query.Reference {
			index = i
			break
		}
	}
	if index == -1 {
		return result, errors.New("history reference is unavailable in this conversation and project scope")
	}
	anchor := records[index].Turn
	if query.ExpectedDigest != "" && query.ExpectedDigest != anchor.Digest {
		return result, errors.New("history turn changed; search or read it again from offset zero")
	}
	maxCharacters := max(anchor.Input.Characters, anchor.Reply.Characters)
	if anchor.Quote != nil {
		maxCharacters = max(maxCharacters, anchor.Quote.Body.Characters, anchor.Quote.Title.Characters)
	}
	if query.Offset > maxCharacters {
		return result, errors.New("history text offset exceeds the referenced turn")
	}
	result.Turns, result.Coverage = []HistoryTurn{}, historyCoverage(authority)
	// Records are newest first; response windows are chronological.
	start, end := max(0, index-query.After), min(len(records), index+query.Before+1)
	for i := end - 1; i >= start; i-- {
		offset, limit := 0, min(query.Limit, 240)
		if i == index {
			offset, limit = query.Offset, query.Limit
		}
		turn := historyBound(records[i].Turn, offset, limit)
		result.Coverage.Partial = result.Coverage.Partial || historyPartial(turn)
		result.Turns = append(result.Turns, turn)
	}
	result.Coverage.Partial = result.Coverage.Partial || start > 0 || end < len(records)
	return result, nil
}
