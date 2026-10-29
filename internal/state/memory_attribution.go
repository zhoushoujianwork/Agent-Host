package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

// This projection mirrors the persisted routing decision without importing
// routing, which depends on state. The versioned handoff wrapper is Host data.
type attributionDecision struct {
	Kind                  string            `json:"kind"`
	ProjectIDs            []string          `json:"project_ids,omitempty"`
	PrimaryID             string            `json:"primary_id,omitempty"`
	Reason                string            `json:"reason"`
	CandidateIDs          []string          `json:"candidate_ids,omitempty"`
	Evidence              []json.RawMessage `json:"evidence,omitempty"`
	ContinuationMessageID string            `json:"continuation_message_id,omitempty"`
	ContinuationRunID     string            `json:"continuation_run_id,omitempty"`
	Details               json.RawMessage   `json:"details,omitempty"`
}

func channelMemoryAttribution(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var decision attributionDecision
	if err := json.Unmarshal(raw, &decision); err != nil {
		return "", errors.New("invalid saved channel routing decision")
	}
	var header struct {
		Policy string `json:"policy"`
	}
	if len(decision.Details) == 0 {
		return "", nil
	}
	if err := json.Unmarshal(decision.Details, &header); err != nil {
		return "", errors.New("invalid saved channel routing details")
	}
	if header.Policy != "channel-native-handoff-v1" && header.Policy != "channel-native-handoff-v2" && header.Policy != "channel-default-continuation-v2" {
		return "", nil
	}
	var handoff struct {
		Policy           string              `json:"policy"`
		OriginalDecision attributionDecision `json:"original_decision"`
	}
	decode := func(raw []byte, target any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return d.Decode(target)
	}
	if err := decode(raw, &decision); err != nil {
		return "", errors.New("invalid native handoff decision")
	}
	if err := decode(decision.Details, &handoff); err != nil {
		return "", errors.New("invalid native handoff audit")
	}
	if header.Policy == "channel-default-continuation-v2" {
		original := handoff.OriginalDecision
		if decision.Kind != "default" || original.Kind != "default" || len(decision.ProjectIDs) != 0 || decision.PrimaryID != "" || len(original.ProjectIDs) != 0 || original.PrimaryID != "" || decision.ContinuationRunID == "" && decision.ContinuationMessageID == "" || decision.ContinuationRunID != original.ContinuationRunID || decision.ContinuationMessageID != original.ContinuationMessageID {
			return "", errors.New("default continuation audit must preserve its source without selecting a project")
		}
		return "", nil
	}
	noSelection := func(d attributionDecision) bool {
		return len(d.ProjectIDs) == 0 && d.PrimaryID == "" && d.ContinuationMessageID == "" && d.ContinuationRunID == ""
	}
	if decision.Kind != "default" || handoff.OriginalDecision.Kind != "clarify" || !noSelection(decision) || !noSelection(handoff.OriginalDecision) {
		return "", errors.New("native handoff must preserve default scope without selecting a project or continuation")
	}
	if header.Policy == "channel-native-handoff-v1" {
		return "unresolved", nil
	}
	return "", nil
}

// Legacy continuations retain their terminal source's recorded attribution.
// New default continuations validate that source but use normal default memory.
// Read only the stored projection, never recursively follow its history.
func memoryAttributionForChannel(ctx context.Context, q rowQuerier, message ChannelMessage) (string, error) {
	attribution, err := channelMemoryAttribution(message.RoutingDecision)
	if err != nil || attribution != "" || len(message.RoutingDecision) == 0 {
		return attribution, err
	}
	var decision attributionDecision
	if err := json.Unmarshal(message.RoutingDecision, &decision); err != nil {
		return "", err
	}
	if decision.Kind != "default" || decision.ContinuationRunID == "" && decision.ContinuationMessageID == "" {
		return "", nil
	}
	if len(decision.ProjectIDs) != 0 || decision.PrimaryID != "" {
		return "", errors.New("default continuation cannot select a project")
	}
	runID := decision.ContinuationRunID
	if runID == "" {
		var messageID string
		if err := q.QueryRowContext(ctx, "SELECT id FROM channel_messages WHERE channel=? AND account_id=? AND message_id=?", message.Channel, message.AccountID, decision.ContinuationMessageID).Scan(&messageID); err != nil {
			return "", wrap("read memory attribution source message", err)
		}
		source, err := readChannelMessage(ctx, q, messageID)
		if err != nil {
			return "", err
		}
		runID = source.RunID
	}
	var sourceRun struct {
		ID                string       `json:"id"`
		SessionID         string       `json:"session_id"`
		Scope             string       `json:"scope"`
		AgentID           string       `json:"agent_id"`
		AudienceID        string       `json:"audience_id"`
		Input             string       `json:"input"`
		ExecutionStatus   string       `json:"execution_status"`
		MemoryAttribution string       `json:"memory_attribution"`
		ProjectID         string       `json:"project_id"`
		Projects          []ProjectRef `json:"projects"`
		Source            *RunSource   `json:"source"`
	}
	var status string
	var payload []byte
	if err := q.QueryRowContext(ctx, "SELECT execution_status,payload FROM runs WHERE id=?", runID).Scan(&status, &payload); err != nil {
		return "", wrap("read memory attribution source run", err)
	}
	if err := json.Unmarshal(payload, &sourceRun); err != nil {
		return "", wrap("decode memory attribution source run", err)
	}
	terminal := status == "completed" || status == "failed" || status == "canceled" || status == "unknown"
	if sourceRun.ID != runID || !terminal || sourceRun.ExecutionStatus != status || sourceRun.Source == nil || sourceRun.Source.ChannelMessageID == "" || sourceRun.ProjectID != "" || len(sourceRun.Projects) != 0 || sourceRun.MemoryAttribution != "" && sourceRun.MemoryAttribution != "unresolved" {
		return "", errors.New("memory attribution requires a terminal default source run")
	}
	source, err := readChannelMessage(ctx, q, sourceRun.Source.ChannelMessageID)
	if err != nil {
		return "", err
	}
	provenance := sourceRun.Source
	matchedSource := provenance.ChannelMessageID == source.ID && provenance.Channel == source.Channel && provenance.AccountID == source.AccountID && provenance.TenantID == source.TenantID && provenance.MessageID == source.MessageID && provenance.ConversationID == source.ConversationID && provenance.SenderID == source.SenderID
	sourceScope := source.Scope
	if source.RouteBinding != "" {
		sourceScope = source.ResolvedScope
	}
	if !matchedSource || source.ID == message.ID || source.Binding != message.Binding || source.Channel != message.Channel || source.AccountID != message.AccountID || source.TenantID != message.TenantID || !samePartition(source.AgentID, source.AudienceID, message.AgentID, message.AudienceID) || !samePartition(sourceRun.AgentID, sourceRun.AudienceID, message.AgentID, message.AudienceID) || source.RunID != sourceRun.ID || source.SessionID != sourceRun.SessionID || sourceScope != sourceRun.Scope || source.Text != sourceRun.Input || decision.ContinuationMessageID != "" && decision.ContinuationMessageID != source.MessageID {
		return "", errors.New("memory attribution continuation does not match its authenticated source")
	}
	var header struct {
		Policy string `json:"policy"`
	}
	if len(decision.Details) != 0 {
		if err := json.Unmarshal(decision.Details, &header); err != nil {
			return "", err
		}
	}
	if header.Policy == "channel-default-continuation-v2" {
		return "", nil
	}
	return sourceRun.MemoryAttribution, nil
}

func validateMemoryAttribution(ctx context.Context, q rowQuerier, run Run) error {
	if run.MemoryAttribution != "" && run.MemoryAttribution != "unresolved" {
		return errors.New("run has unsupported memory attribution")
	}
	if run.Source == nil || run.Source.ChannelMessageID == "" {
		if run.MemoryAttribution != "" {
			return errors.New("unresolved memory attribution requires channel routing evidence")
		}
		return nil
	}
	message, err := readChannelMessage(ctx, q, run.Source.ChannelMessageID)
	if err != nil {
		return err
	}
	attribution, err := memoryAttributionForChannel(ctx, q, message)
	if err != nil {
		return err
	}
	if attribution != run.MemoryAttribution || message.Text != run.Input || !samePartition(message.AgentID, message.AudienceID, run.AgentID, run.AudienceID) {
		return errors.New("run memory attribution does not match its saved channel routing evidence")
	}
	if attribution == "unresolved" && (run.ProjectID != "" || len(run.Projects) != 0) {
		return errors.New("unresolved memory attribution requires a default channel run")
	}
	return nil
}

func automaticMemorySkipAudit(run Run, reason string) json.RawMessage {
	policy := "multi-project-writeback-disabled"
	if run.MemoryAttribution == "unresolved" {
		policy = "unresolved-memory-attribution-writeback-disabled"
	}
	if reason == MemoryPluginDisabledReason {
		policy = "memory-plugin-disabled"
	}
	raw, _ := json.Marshal(map[string]string{"policy": policy, "reason": reason})
	return raw
}
