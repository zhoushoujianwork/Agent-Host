package harness

import (
	"context"
	"encoding/json"
)

// Request carries explicit native execution policy. An empty PermissionMode
// preserves native defaults; business instances may explicitly select bypass.
type Request struct {
	MonitorResumeCommand    []string
	MonitorResumeDirectory  string
	CallerRunID             string // Owning Host run correlation, never execution authority.
	HostSessionID           string // Stable Host conversation identity; never a native thread ID.
	NativeSessionProfile    string // Canonical execution profile recorded by the previous turn.
	WriteIntent             bool   // Task intent only; does not grant permissions or provide a sandbox.
	NativeProfile           string
	PermissionMode          string
	HostInstructions        string
	Prompt                  string
	Images                  []Image
	AdditionalDirectories   []string
	Workspace               string
	Model                   string
	NativeSessionID         string
	ForkFromNativeSessionID string // Immutable completed parent; never resume it in place.
	Resume                  bool
	ToolsDisabled           bool
	RestrictedReadOnly      bool // Fixed member capability profile; never grants execution or mutation.
	AllowedTools            []string
	MCPServers              []MCPServer
	JSONSchema              string
	Extraction              bool   // Isolated, tool-free, nonpersistent extraction invocation.
	SkillPluginDir          string // Explicit task-scoped, instruction-only native package.
	SkillPluginDigest       string // Expected snapshot digest; cache content cannot change it.
}

const (
	MaxImages     = 4
	MaxImageBytes = 5 << 20
)

// Image carries in-memory input bytes for the native invocation. Durable source
// references belong to the Host; binary payloads are not journal metadata.
type Image struct {
	Name      string
	MediaType string
	Data      []byte `json:"-"`
}

// MCPServer describes one explicitly selected stdio capability. Credentials are
// resolved by its command; they are never embedded in native configuration.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
	// SessionKey is Host-derived stable authority for a receipt-bound server.
	// The current Args still launch the tool; this key is never sent to it.
	SessionKey string `json:"-"`
}

// Observation contains protocol evidence and normalized UI fields.
type Observation struct {
	Type            string
	Message         string
	Delta           string
	Model           string
	NativeSessionID string
	Raw             json.RawMessage
	Tool            *ToolObservation
	SkillMount      *SkillMount
}

// SkillMount identifies a Host-controlled package copy used by the native
// engine. It is trusted adapter metadata, never parsed from native output.
type SkillMount struct {
	Digest    string `json:"digest"`
	Directory string `json:"directory"`
}

// ToolObservation normalizes native calls/results without inventing verification.
type ToolObservation struct {
	ID      string          `json:"id"`
	Name    string          `json:"name,omitempty"`
	Stage   string          `json:"stage"` // call or result
	Input   json.RawMessage `json:"input,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	IsError bool            `json:"is_error"`
}

type Result struct {
	Status           string             `json:"status"` // completed, failed, canceled, unknown
	Text             string             `json:"text"`
	Model            string             `json:"model"`
	NativeSessionID  string             `json:"native_session_id"`
	ExitCode         int                `json:"exit_code"`
	Error            string             `json:"error,omitempty"`
	StructuredOutput json.RawMessage    `json:"structured_output,omitempty"`
	Evidence         *ExecutionEvidence `json:"execution_evidence,omitempty"`
}

// ExecutionEvidence preserves attributed executor records separately from the
// normalized outcome. Raw may contain task text and belongs in private storage,
// never an unfiltered UI observation or a business acceptance judgment.
type ExecutionEvidence struct {
	RunID          string          `json:"run_id,omitempty"`
	CallerRunID    string          `json:"caller_run_id,omitempty"`
	Profile        string          `json:"profile,omitempty"`
	Backend        string          `json:"backend,omitempty"`
	NativeProtocol string          `json:"native_protocol,omitempty"`
	Status         string          `json:"status,omitempty"`
	ErrorKind      string          `json:"error_kind,omitempty"`
	Usage          json.RawMessage `json:"usage,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	HistoryStatus  string          `json:"history_status,omitempty"`
	HistoryError   string          `json:"history_error,omitempty"`
}

type Runner interface {
	Run(context.Context, Request, func(Observation) error) (Result, error)
}

// Identity describes the execution boundary, independently of the logical Agent.
type Identity struct {
	Agent     string `json:"agent"`
	Component string `json:"component"`
	Engine    string `json:"engine"`
	Transport string `json:"transport"`
	Version   string `json:"version,omitempty"`
}

func RunnerIdentity(r Runner) Identity {
	if described, ok := r.(interface{ Identity() Identity }); ok {
		return described.Identity()
	}
	return Identity{Agent: "local-claude", Component: "harness.claude", Engine: "claude", Transport: "print"}
}
