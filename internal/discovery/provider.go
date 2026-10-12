package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"agent-host/internal/state"
)

type Range struct{ From, To time.Time }
type Collection struct {
	Coverage state.DiscoveryCollection
	Evidence []state.DiscoveryEvidence
}
type Provider interface {
	Fetch(context.Context, Source, Range) (Collection, error)
}
type DWSProvider struct{ config Config }

func NewDWSProvider(cfg Config) *DWSProvider { return &DWSProvider{config: cfg} }

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// Fetch uses the verified im.message-list.v1 shortcut. DWS owns genuine
// continuation via --page-all; Host retains its complete coverage ledger.
func (p *DWSProvider) Fetch(ctx context.Context, source Source, r Range) (Collection, error) {
	if p == nil {
		return Collection{}, errors.New("DWS provider is unavailable")
	}
	if err := p.config.Check(ctx, nil); err != nil {
		return Collection{}, err
	}
	if !r.To.After(r.From) {
		return Collection{}, errors.New("invalid DWS collection range")
	}
	selected := false
	for _, s := range p.config.Sources {
		if s == source {
			selected = true
		}
	}
	if !selected {
		return Collection{}, errors.New("DWS source is outside configured selection")
	}
	if err := p.verifyIdentity(ctx); err != nil {
		return Collection{}, err
	}
	args := []string{"chat", "+chat-messages", "--profile", p.config.Profile, "--format", "json", "--start", r.From.UTC().Format(time.RFC3339Nano), "--end", r.To.UTC().Format(time.RFC3339Nano), "--order", "asc", "--page-all", "--page-limit", strconv.Itoa(p.config.PageLimit), "--max-items", strconv.Itoa(p.config.MaxMessages), "--no-reactions", "--timeout", strconv.Itoa(p.config.TimeoutSeconds)}
	if source.Type == "dm" {
		args = append(args, "--open-dingtalk-id", source.PeerOpenDingTalkID)
	} else {
		// This installed leaf accepts the explicit ID alias. It bypasses the
		// --group name resolver and cannot discover another conversation.
		args = append(args, "--conversation-id", source.ConversationID)
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, p.config.DWSExecutable, args...)
	stdout, stderr := &boundedBuffer{limit: 8 << 20}, &boundedBuffer{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return Collection{}, errors.New("DWS response exceeded bounded output limits")
	}
	if callCtx.Err() != nil {
		return Collection{}, fmt.Errorf("DWS collection interrupted: %w", callCtx.Err())
	}
	result, parseErr := decodeCollection(stdout.Bytes(), p.config, source, r)
	if runErr != nil {
		// Structured codes alone are safe operational diagnostics. Arbitrary
		// stderr/message bodies can contain chat text or credentials.
		code := dwsErrorCode(stderr.Bytes())
		err := fmt.Errorf("DWS collection failed (%s); inspect the selected profile locally", code)
		if parseErr == nil {
			result.Coverage.Complete = false
			result.Coverage.Partial = true
			result.Coverage.Error = err.Error()
			return result, err
		}
		return Collection{}, err
	}
	if parseErr != nil {
		return Collection{}, parseErr
	}
	return result, nil
}

func dwsErrorCode(b []byte) string {
	var e struct {
		Error struct {
			Code     int    `json:"code"`
			Category string `json:"category"`
			Reason   string `json:"reason"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil {
		return "process_error"
	}
	allowed := func(v string) string {
		for _, c := range v {
			if c != '_' && c != '-' && (c < 'a' || c > 'z') {
				return "unclassified"
			}
		}
		if len(v) > 80 || v == "" {
			return "unclassified"
		}
		return v
	}
	return fmt.Sprintf("code=%d category=%s reason=%s", e.Error.Code, allowed(e.Error.Category), allowed(e.Error.Reason))
}

type messageEnvelope struct {
	ContractVersion        string             `json:"contractVersion"`
	Messages               *[]providerMessage `json:"messages"`
	Count                  int                `json:"count"`
	Complete               *bool              `json:"complete"`
	Partial                bool               `json:"partial"`
	HasMore                bool               `json:"hasMore"`
	PagesFetched           int                `json:"pagesFetched"`
	PaginationKnown        *bool              `json:"paginationKnown"`
	NextPage               json.RawMessage    `json:"nextPage"`
	QueryRange             json.RawMessage    `json:"queryRange"`
	ResolvedFilters        json.RawMessage    `json:"resolvedFilters"`
	FailedCount            int                `json:"failedCount"`
	Failures               json.RawMessage    `json:"failures"`
	StopReason             string             `json:"stopReason"`
	Truncated              bool               `json:"truncated"`
	TruncatedByPageLimit   bool               `json:"truncatedByPageLimit"`
	TruncatedByResultLimit bool               `json:"truncatedByResultLimit"`
	Scope                  json.RawMessage    `json:"scope"`
}

type providerMessage struct {
	MessageID      string          `json:"messageId"`
	ConversationID string          `json:"conversationId"`
	ThreadID       string          `json:"threadId"`
	Sender         json.RawMessage `json:"sender"`
	SenderID       string          `json:"senderId"`
	SenderType     json.RawMessage `json:"senderType"`
	MessageType    json.RawMessage `json:"messageType"`
	Text           string          `json:"text"`
	CreateTime     json.RawMessage `json:"createTime"`
	UpdateTime     json.RawMessage `json:"updateTime"`
	QuotedMessage  json.RawMessage `json:"quotedMessage"`
	Forwarded      json.RawMessage `json:"forwarded"`
}

func decodeCollection(b []byte, cfg Config, source Source, r Range) (Collection, error) {
	out := Collection{Evidence: []state.DiscoveryEvidence{}}
	if rejectDuplicateKeys(b) != nil {
		return out, errors.New("DWS response contains invalid or duplicate JSON fields")
	}
	var envelope messageEnvelope
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&envelope); err != nil {
		return out, errors.New("DWS returned invalid JSON; no source content was recorded")
	}
	if d.Decode(new(any)) != io.EOF {
		return out, errors.New("DWS returned trailing response data")
	}
	if envelope.ContractVersion != "im.message-list.v1" || envelope.Messages == nil || envelope.Complete == nil || envelope.PaginationKnown == nil || envelope.Count != len(*envelope.Messages) || envelope.Count > cfg.MaxMessages || envelope.PagesFetched < 0 || envelope.PagesFetched > cfg.PageLimit {
		return out, errors.New("DWS message envelope violates the verified bounded contract")
	}
	var failures []struct {
		Stage string `json:"stage"`
	}
	if json.Unmarshal(envelope.Failures, &failures) != nil || failures == nil || envelope.FailedCount < 0 || envelope.FailedCount != len(failures) {
		return out, errors.New("DWS failure ledger violates the verified bounded contract")
	}
	hasReadFailure, hasPaginationFailure := false, false
	for _, failure := range failures {
		hasReadFailure = hasReadFailure || failure.Stage == "read"
		hasPaginationFailure = hasPaginationFailure || failure.Stage == "pagination"
	}
	// The installed page-all leaf publishes a failure ledger even when its
	// first read fails or lower pagination is unknown. Those facts preserve
	// evidence without proving complete coverage or inventing a continuation.
	firstReadFailure := envelope.StopReason == "read_failure" && hasReadFailure && envelope.Count == 0 && !*envelope.Complete && !envelope.HasMore
	paginationFailure := envelope.StopReason == "pagination_error" && hasPaginationFailure && !*envelope.Complete
	if envelope.PagesFetched == 0 && !firstReadFailure {
		return out, errors.New("DWS coverage ledger has no successful page or initial read failure")
	}
	if !*envelope.PaginationKnown && !paginationFailure {
		return out, errors.New("DWS unknown pagination omitted its failure ledger")
	}
	if *envelope.Complete && (envelope.HasMore || envelope.Partial || envelope.Truncated || envelope.TruncatedByPageLimit || envelope.TruncatedByResultLimit || envelope.FailedCount > 0) {
		return out, errors.New("DWS complete coverage contradicts its failure or truncation ledger")
	}
	if envelope.Partial != (envelope.FailedCount > 0 && envelope.Count > 0) {
		return out, errors.New("DWS coverage ledger is inconsistent")
	}
	if envelope.HasMore && (len(envelope.NextPage) == 0 || bytes.Equal(bytes.TrimSpace(envelope.NextPage), []byte("null"))) && !paginationFailure {
		return out, errors.New("DWS partial history omitted genuine continuation")
	}
	var qr struct {
		Start     json.RawMessage `json:"startTime"`
		End       json.RawMessage `json:"endTime"`
		Order     string          `json:"order"`
		Semantics string          `json:"semantics"`
	}
	if json.Unmarshal(envelope.QueryRange, &qr) != nil {
		return out, errors.New("DWS history omitted explicit query range")
	}
	from, err := sourceTime(qr.Start)
	if err != nil {
		return out, errors.New("DWS query range start is invalid")
	}
	to, err := sourceTime(qr.End)
	if err != nil || !from.Equal(r.From) || !to.Equal(r.To) || qr.Order != "asc" || qr.Semantics != "[start,end)" {
		return out, errors.New("DWS response range does not match the selected bounded query")
	}
	attr := source.Attribution(cfg)
	seen := map[string]bool{}
	for _, m := range *envelope.Messages {
		created, err := sourceTime(m.CreateTime)
		if err != nil {
			return out, errors.New("DWS message has invalid source timestamp")
		}
		var updated time.Time
		if len(m.UpdateTime) > 0 && !bytes.Equal(m.UpdateTime, []byte("null")) {
			updated, err = sourceTime(m.UpdateTime)
			if err != nil {
				return out, errors.New("DWS message has invalid revision timestamp")
			}
		}
		if m.ConversationID != source.ConversationID || m.MessageID == "" || m.SenderID == "" || created.Before(r.From) || !created.Before(r.To) || len(m.Text) > 32768 {
			return out, errors.New("DWS message is outside selected identity, conversation, window or content bounds")
		}
		e := state.DiscoveryEvidence{SourceID: attr.ID, Profile: attr.Profile, TenantID: attr.TenantID, PrincipalID: attr.PrincipalID, ConversationID: source.ConversationID, ProjectID: source.ProjectID, MessageID: m.MessageID, AuthorID: m.SenderID, Author: m.Sender, SenderType: sourceType(m.SenderType), MessageType: sourceType(m.MessageType), Text: m.Text, CreatedAt: created, UpdatedAt: updated, ThreadID: m.ThreadID, QuotedMessage: m.QuotedMessage, Forwarded: m.Forwarded, Availability: "available"}
		encoded, _ := json.Marshal(e)
		if len(encoded) > 64<<10 {
			return out, errors.New("DWS message snapshot exceeds 64 KiB; no source content was truncated")
		}
		e = state.PrepareDiscoveryEvidence(e)
		if seen[e.ID] {
			return out, errors.New("DWS response contains duplicate source revisions")
		}
		seen[e.ID] = true
		out.Evidence = append(out.Evidence, e)
	}
	// Retain coverage without message bodies, downloads, reaction payloads, or
	// arbitrary error messages in operational inspection.
	ledger, err := json.Marshal(struct {
		ContractVersion        string          `json:"contractVersion"`
		QueryRange             json.RawMessage `json:"queryRange"`
		ResolvedFilters        json.RawMessage `json:"resolvedFilters,omitempty"`
		Complete               bool            `json:"complete"`
		Partial                bool            `json:"partial"`
		HasMore                bool            `json:"hasMore"`
		NextPage               json.RawMessage `json:"nextPage,omitempty"`
		PagesFetched           int             `json:"pagesFetched"`
		PaginationKnown        bool            `json:"paginationKnown"`
		StopReason             string          `json:"stopReason"`
		Truncated              bool            `json:"truncated"`
		TruncatedByPageLimit   bool            `json:"truncatedByPageLimit"`
		TruncatedByResultLimit bool            `json:"truncatedByResultLimit"`
		FailedCount            int             `json:"failedCount"`
		Failures               json.RawMessage `json:"failures,omitempty"`
		Scope                  json.RawMessage `json:"scope,omitempty"`
	}{envelope.ContractVersion, envelope.QueryRange, envelope.ResolvedFilters, *envelope.Complete, envelope.Partial, envelope.HasMore, envelope.NextPage, envelope.PagesFetched, *envelope.PaginationKnown, envelope.StopReason, envelope.Truncated, envelope.TruncatedByPageLimit, envelope.TruncatedByResultLimit, envelope.FailedCount, envelope.Failures, envelope.Scope})
	if err != nil {
		return out, errors.New("encode DWS coverage ledger")
	}
	out.Coverage = state.DiscoveryCollection{From: r.From, To: r.To, Complete: *envelope.Complete, Partial: envelope.Partial, HasMore: envelope.HasMore, Count: len(out.Evidence), PagesFetched: envelope.PagesFetched, Ledger: ledger}
	out.Coverage.TargetVerification = "executed_exact_selector_only"
	if len(out.Evidence) > 0 {
		out.Coverage.TargetVerification = "exact_conversation_verified_in_messages"
	}
	return out, nil
}

func sourceTime(b []byte) (time.Time, error) {
	v := strings.Trim(string(b), "\"")
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	// These are the installed DWS parser's explicit local display formats.
	location := time.FixedZone("DingTalk", 8*60*60)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, location); err == nil {
			return t.UTC(), nil
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, errors.New("invalid source time")
	}
	// Raw source create/update timestamps may be seconds or milliseconds;
	// genuine pagination always stays in its separately retained nextPage.
	if n < 1_000_000_000_000 {
		return time.Unix(n, 0).UTC(), nil
	}
	return time.UnixMilli(n).UTC(), nil
}

func sourceType(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

// profile list reads the local account registry and does not refresh or switch
// identities. Only an exact stable tenant:principal selector may read history.
func (p *DWSProvider) verifyIdentity(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, p.config.DWSExecutable, "profile", "list", "--format", "json")
	out, stderr := &boundedBuffer{limit: 1 << 20}, &boundedBuffer{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return errors.New("DWS identity verification failed; no chat history was read")
	}
	if out.exceeded || stderr.exceeded {
		return errors.New("DWS identity registry exceeded bounded output limits")
	}
	var registry struct {
		Success  bool `json:"success"`
		Profiles []struct {
			Profile string `json:"profile"`
			CorpID  string `json:"corpId"`
			UserID  string `json:"userId"`
		} `json:"profiles"`
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if d.Decode(&registry) != nil || d.Decode(new(any)) != io.EOF || !registry.Success {
		return errors.New("DWS identity registry returned invalid structure")
	}
	matched := 0
	for _, entry := range registry.Profiles {
		if entry.Profile == p.config.Profile {
			if entry.CorpID+":"+entry.UserID != p.config.Profile {
				return errors.New("DWS profile alias or identity drift is forbidden")
			}
			matched++
		}
	}
	if matched != 1 {
		return errors.New("DWS configured stable personal identity is missing or ambiguous; no alternate identity was selected")
	}
	return nil
}
