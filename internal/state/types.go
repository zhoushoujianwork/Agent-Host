package state

import (
	"encoding/json"
	"time"

	"agent-host/internal/event"
)

type Session struct {
	AgentID               string       `json:"agent_id,omitempty"`
	AudienceID            string       `json:"audience_id,omitempty"`
	ID                    string       `json:"id"`
	Scope                 string       `json:"scope"`
	Model                 string       `json:"model"`
	NativeSessionID       string       `json:"native_session_id"`
	CreatedAt             time.Time    `json:"created_at"`
	ProjectID             string       `json:"project_id,omitempty"`
	ProjectName           string       `json:"project_name,omitempty"`
	Workspace             string       `json:"workspace,omitempty"`
	RuntimeFingerprint    string       `json:"runtime_fingerprint,omitempty"`
	Projects              []ProjectRef `json:"projects,omitempty"`
	AdditionalDirectories []string     `json:"additional_directories,omitempty"`
	PreviousSessionID     string       `json:"previous_session_id,omitempty"`
	RecoveryRunID         string       `json:"recovery_run_id,omitempty"`
}

// ProjectRef records the project identity and directory authorized for one run.
type ProjectRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Scope     string `json:"scope"`
}

// UserSupplement preserves a later clarification separately from the original input.
type UserSupplement struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// WorkingDirectory interprets old directory-scoped sessions without rewriting them.
func (s Session) WorkingDirectory() string {
	if s.Workspace != "" {
		return s.Workspace
	}
	return s.Scope
}

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Workspace   string    `json:"workspace"`
	Scope       string    `json:"scope"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Description string    `json:"description,omitempty"`
	Aliases     []string  `json:"aliases,omitempty"`
}

type Memory struct {
	SenderID         string          `json:"sender_id,omitempty"`
	AgentID          string          `json:"agent_id,omitempty"`
	AudienceID       string          `json:"audience_id,omitempty"`
	ID               string          `json:"id"`
	Scope            string          `json:"scope"`
	Kind             string          `json:"kind"`
	Text             string          `json:"text"`
	Status           string          `json:"status"` // active, conflicted, or invalidated
	Revision         int             `json:"revision"`
	SourceRunID      string          `json:"source_run_id"`
	CreatedAt        time.Time       `json:"created_at"`
	SourceType       string          `json:"source_type,omitempty"` // user_statement or reviewed_experience
	CandidateID      string          `json:"candidate_id,omitempty"`
	Conditions       string          `json:"conditions,omitempty"`
	Steps            []string        `json:"steps,omitempty"`
	Avoid            []string        `json:"avoid,omitempty"`
	RevisionDecision json.RawMessage `json:"revision_decision,omitempty"`
	Entities         []string        `json:"entities,omitempty"`
	EvidenceIDs      []string        `json:"evidence_ids,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	ReviewNote       string          `json:"review_note,omitempty"`
}

type Run struct {
	Preparation           *PreparationSource `json:"preparation,omitempty"`
	AgentID               string             `json:"agent_id,omitempty"`
	AudienceID            string             `json:"audience_id,omitempty"`
	ID                    string             `json:"id"`
	SessionID             string             `json:"session_id"`
	Scope                 string             `json:"scope"`
	Input                 string             `json:"input"`
	Agent                 string             `json:"agent"`
	RequestedModel        string             `json:"requested_model"`
	Model                 string             `json:"model,omitempty"`
	NativeSessionID       string             `json:"native_session_id"`
	ExecutionStatus       string             `json:"execution_status"`
	MemoryStatus          string             `json:"memory_status"`
	MemoryAttribution     string             `json:"memory_attribution,omitempty"` // unresolved pauses automatic writeback
	AcceptanceStatus      string             `json:"acceptance_status"`
	Output                string             `json:"output"`
	Error                 string             `json:"error,omitempty"`
	MemoryError           string             `json:"memory_error,omitempty"`
	Context               []Memory           `json:"context"`
	Decision              json.RawMessage    `json:"decision,omitempty"`
	CreatedAt             time.Time          `json:"created_at"`
	Events                []event.Event      `json:"events,omitempty"`
	ExperienceStatus      string             `json:"experience_status,omitempty"`
	ExperienceError       string             `json:"experience_error,omitempty"`
	ExperienceDecision    json.RawMessage    `json:"experience_decision,omitempty"`
	Source                *RunSource         `json:"source,omitempty"`
	ProjectID             string             `json:"project_id,omitempty"`
	ProjectName           string             `json:"project_name,omitempty"`
	Workspace             string             `json:"workspace,omitempty"`
	RuntimeFingerprint    string             `json:"runtime_fingerprint,omitempty"`
	Projects              []ProjectRef       `json:"projects,omitempty"`
	AdditionalDirectories []string           `json:"additional_directories,omitempty"`
	Supplements           []UserSupplement   `json:"supplements,omitempty"`
}

// AutomaticMemorySkipReason keeps execution independent from whether its
// results can safely be attributed to one durable memory namespace.
func (r Run) AutomaticMemorySkipReason() string {
	if r.MemoryAttribution == "unresolved" {
		return "unresolved_project_memory_attribution"
	}
	if len(r.Projects) > 1 {
		return "multi_project_requires_explicit_memory_attribution"
	}
	return ""
}

// WorkingDirectory interprets old directory-scoped runs without rewriting evidence.
func (r Run) WorkingDirectory() string {
	if r.Workspace != "" {
		return r.Workspace
	}
	return r.Scope
}

// RunSource links native execution and derived memory to an accepted channel input.
type RunSource struct {
	Input            *ChannelInput `json:"input_material,omitempty"`
	ConversationType string        `json:"conversation_type,omitempty"`
	AgentID          string        `json:"agent_id,omitempty"`
	AudienceID       string        `json:"audience_id,omitempty"`
	Channel          string        `json:"channel"`
	ChannelMessageID string        `json:"channel_message_id"`
	AccountID        string        `json:"account_id"`
	TenantID         string        `json:"tenant_id"`
	MessageID        string        `json:"message_id"`
	ConversationID   string        `json:"conversation_id"`
	SenderID         string        `json:"sender_id"`
}

// ChannelReplyTarget identifies a credential-free API destination. Credentials
// remain in the configured channel adapter; UUID fixes the delivery identity.
type ChannelReplyTarget struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	UUID      string `json:"uuid"`
	MessageID string `json:"message_id,omitempty"`
}

const (
	ReplyTargetFeishu        = "feishu"
	ReplyTargetFeishuMeeting = "feishu-meeting"
)

// ChannelMessage is safe to inspect without disclosing its private reply URL.
// Execution and delivery have independent status fields.
type ChannelMessage struct {
	Input                 *ChannelInput     `json:"input_material,omitempty"`
	ConversationType      string            `json:"conversation_type,omitempty"`
	AgentID               string            `json:"agent_id,omitempty"`
	AudienceID            string            `json:"audience_id,omitempty"`
	ID                    string            `json:"id"`
	Channel               string            `json:"channel"`
	AccountID             string            `json:"account_id"`
	TenantID              string            `json:"tenant_id"`
	MessageID             string            `json:"message_id"`
	ConversationID        string            `json:"conversation_id"`
	SenderID              string            `json:"sender_id"`
	Profile               string            `json:"profile"`
	Binding               string            `json:"binding"`
	Scope                 string            `json:"scope"`
	MessageType           string            `json:"message_type"`
	Text                  string            `json:"text"`
	SentAt                time.Time         `json:"sent_at,omitempty"`
	ReceivedAt            time.Time         `json:"received_at"`
	ReplyExpiresAt        time.Time         `json:"reply_expires_at"`
	Status                string            `json:"status"`
	SessionID             string            `json:"session_id,omitempty"`
	RunID                 string            `json:"run_id,omitempty"`
	Error                 string            `json:"error,omitempty"`
	ReplyStatus           string            `json:"reply_status"`
	ReplyText             string            `json:"reply_text,omitempty"`
	ReplyError            string            `json:"reply_error,omitempty"`
	Reactions             []ChannelReaction `json:"reactions,omitempty"`
	ProjectID             string            `json:"project_id,omitempty"`
	Workspace             string            `json:"workspace,omitempty"`
	ResolvedScope         string            `json:"resolved_scope,omitempty"`
	RuntimeFingerprint    string            `json:"runtime_fingerprint,omitempty"`
	RouteBinding          string            `json:"route_binding,omitempty"`
	Projects              []ProjectRef      `json:"projects,omitempty"`
	AdditionalDirectories []string          `json:"additional_directories,omitempty"`
	Supplements           []UserSupplement  `json:"supplements,omitempty"`
	RoutingDecision       json.RawMessage   `json:"routing_decision,omitempty"`

	ReplyTarget      *ChannelReplyTarget `json:"reply_target,omitempty"`
	DisableReactions bool                `json:"disable_reactions,omitempty"`
}

// ChannelReaction records one independently delivered message-stage marker.
// Active means an accepted marker has not been confirmed removed.
type ChannelReaction struct {
	ID                 int64     `json:"id"`
	ChannelMessageID   string    `json:"channel_message_id"`
	Stage              string    `json:"stage"`
	Label              string    `json:"label"`
	ProviderReactionID string    `json:"provider_reaction_id,omitempty"`
	Status             string    `json:"status"`
	Error              string    `json:"error,omitempty"`
	Active             bool      `json:"active"`
	RemovalStatus      string    `json:"removal_status"`
	RemovalError       string    `json:"removal_error,omitempty"`
	SourceRunID        string    `json:"source_run_id,omitempty"`
	SourceEventSeq     int64     `json:"source_event_seq,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ToolEvidence binds a native call to its observed result in the run journal.
type ToolEvidence struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Result    json.RawMessage `json:"result"`
	IsError   bool            `json:"is_error"`
	CallSeq   int64           `json:"call_seq"`
	ResultSeq int64           `json:"result_seq"`
}

type ExperienceDraft struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Conditions  string   `json:"conditions"`
	Steps       []string `json:"steps"`
	Avoid       []string `json:"avoid,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Candidate struct {
	AgentID    string          `json:"agent_id,omitempty"`
	AudienceID string          `json:"audience_id,omitempty"`
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	Scope      string          `json:"scope"`
	Draft      ExperienceDraft `json:"draft"`
	Entities   []string        `json:"entities"`
	Status     string          `json:"status"` // pending, accepted, rejected
	Model      string          `json:"model"`
	Assessment json.RawMessage `json:"assessment"`
	CreatedAt  time.Time       `json:"created_at"`
	ReviewedAt *time.Time      `json:"reviewed_at,omitempty"`
	ReviewNote string          `json:"review_note,omitempty"`
	MemoryID   string          `json:"memory_id,omitempty"`
}

type CandidateAcceptance struct {
	MemoryID          string
	Note              string
	ExpiresAt         *time.Time
	ConflictIDs       []string
	DuplicateIDs      []string
	ExpectedRevisions map[string]int
	Decision          json.RawMessage
}

// MemoryUpdate appends an exact user statement. Conflicts exclude both sides
// from recall; no model decision silently overwrites a previous statement.
type MemoryUpdate struct {
	Memory       Memory
	ConflictIDs  []string
	DuplicateIDs []string
}
