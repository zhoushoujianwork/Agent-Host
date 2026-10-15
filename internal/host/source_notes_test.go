package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func sourceNoteSubmission() state.ExternalSubmission {
	now := time.Now().UTC()
	no := false
	text := "# Konga lookup\nHistorical note: Konga's whitelist was visible through the portal. Not rechecked today."
	return state.ExternalSubmission{SchemaVersion: 1, Key: "note", Source: state.ExternalSource{Entry: "source-import-test", ObservedAt: now}, Items: []state.ExternalItem{{Kind: state.SourceNoteKind, Title: "Konga lookup", Text: text, EvidenceIDs: []string{"read"}, Objects: []state.ExternalObject{{Name: "Konga", Kind: "system", EvidenceID: "read"}}}}, Evidence: []state.ExternalEvidence{{ID: "read", Role: "tool", Author: "source-import-test", Text: "Selected historical Markdown, read now; not an execution of its procedure.", ObservedAt: now, Tool: &state.ExternalTool{Name: "Read", Input: json.RawMessage(`{"path":"/fixture/Konga.md"}`), Result: mustNoteJSON(text), IsError: &no}}}}
}

func mustNoteJSON(text string) json.RawMessage {
	raw, _ := json.Marshal(text)
	return raw
}

func TestSourceNoteImportsWithoutImpersonatingUserOrExecution(t *testing.T) {
	h, c := externalHost(t)
	h.Decider, h.Judge = nil, nil
	v := sourceNoteSubmission()
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	r = processedExternal(t, h, r)
	if r.Results[0].Status != "published" || r.Results[0].Reason != "imported_source_note" {
		t.Fatal(r.Results)
	}
	m, err := h.Store.ContextMemory(t.Context(), c.Scope, r.Results[0].MemoryID, 1)
	if err != nil || m.Kind != state.SourceNoteKind || m.SourceType != state.SourceNoteSource || m.SourceRunID != "" || !strings.Contains(m.Text, v.Items[0].Text) || len(m.Steps) != 0 {
		t.Fatal(m, err)
	}
	if _, err := h.Store.ContextMemoryEvidence(t.Context(), c.Scope, m.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.WithPartition("foreign", "owner").ContextMemory(t.Context(), c.Scope, m.ID, 1); err == nil {
		t.Fatal("note escaped its partition")
	}
	if hits, err := h.Store.Search(t.Context(), c.Scope, "Konga whitelist", 10); err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	if comparisons, err := h.Store.DecisionMemories(t.Context(), c.Scope, "Konga whitelist", 1); err != nil || len(comparisons) != 0 {
		t.Fatal("quotation was treated as an admitted assertion", comparisons, err)
	}
	v.Key = "same-note"
	repeat, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	repeat = processedExternal(t, h, repeat)
	if repeat.Results[0].MemoryID != m.ID || repeat.Results[0].Reason != "duplicate_existing_memory" {
		t.Fatal(repeat.Results)
	}
	if err := h.Store.InvalidateMemory(t.Context(), m.ID, "withdraw this source"); err != nil {
		t.Fatal(err)
	}
	v.Key = "withdrawn-note"
	withdrawn, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	withdrawn = processedExternal(t, h, withdrawn)
	if withdrawn.Results[0].Status != "excluded" {
		t.Fatal("withdrawn note was revived", withdrawn.Results)
	}
}

func TestSourceNoteRequiresExactNonErrorToolQuotation(t *testing.T) {
	for _, mode := range []string{"changed-text", "error", "user-impersonation", "structured-result", "procedure", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			v := sourceNoteSubmission()
			switch mode {
			case "changed-text":
				v.Items[0].Text += " Invented conclusion."
			case "error":
				*v.Evidence[0].Tool.IsError = true
			case "user-impersonation":
				v.Evidence[0].Role, v.Evidence[0].Tool = "user", nil
			case "structured-result":
				v.Evidence[0].Tool.Result = json.RawMessage(`{"content":"different"}`)
			case "procedure":
				v.Items[0].Procedure = &state.ExperienceDraft{}
			case "oversize":
				v.Items[0].Text = strings.Repeat("x", 8193)
				v.Evidence[0].Tool.Result = mustNoteJSON(v.Items[0].Text)
			}
			if err := v.Validate(); err == nil {
				t.Fatal("invalid source note was admitted")
			}
		})
	}
}

func TestSourceNoteDoesNotEstablishAliasAuthority(t *testing.T) {
	h, c := externalHost(t)
	v := sourceNoteSubmission()
	v.Items[0].Text = "订单服务（简称交易服务）的数据库使用 PostgreSQL。"
	v.Items[0].Objects = []state.ExternalObject{{Name: "订单服务", Kind: "service", EvidenceID: "read"}}
	v.Evidence[0].Tool.Result = mustNoteJSON(v.Items[0].Text)
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	r = processedExternal(t, h, r)
	if r.Results[0].Status != "published" {
		t.Fatal(r.Results)
	}
	got, err := h.Store.Retrieve(t.Context(), c.Scope, "交易服务的数据库", 10)
	if err != nil || len(got.Matches) != 0 || len(got.Query.Unknown) == 0 {
		t.Fatal("quoted alias became registry authority", got, err)
	}
}

func TestSourceNotesCannotShadowAdmittedProcedureSubjects(t *testing.T) {
	h, c := externalHost(t)
	known := sourceNoteSubmission()
	known.Key = "known-cbs-service"
	known.Evidence[0].Tool.Input = json.RawMessage(`{"command":"tccli cbs DescribeDisks --profile default"}`)
	known.Evidence[0].Tool.Result = mustNoteJSON("Fixture: tccli cbs returned DiskSet")
	known.Items[0] = state.ExternalItem{Kind: "procedure", EvidenceIDs: []string{"read"}, Objects: []state.ExternalObject{{Name: "tccli", Kind: "tool", EvidenceID: "read"}, {Name: "cbs", Kind: "service", EvidenceID: "read"}}, Procedure: &state.ExperienceDraft{Title: "tccli cbs DescribeDisks", Summary: "Fixture reported a disk lookup.", Conditions: "For the fixture's disk lookup.", Steps: []string{"Use tccli cbs DescribeDisks --profile default."}, EvidenceIDs: []string{"read"}}}
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), known)
	if err != nil {
		t.Fatal(err)
	}
	r = processedExternal(t, h, r)
	if r.Results[0].Status != "published" {
		t.Fatal(r.Results)
	}
	procedureID := r.Results[0].MemoryID
	quote := sourceNoteSubmission()
	quote.Key = "different-cbs-tool"
	quote.Items[0].Title = "tccli cbs historical note"
	quote.Items[0].Text = strings.Repeat("tccli cbs DescribeDisks historical reference. ", 20)
	quote.Items[0].Objects = []state.ExternalObject{{Name: "tccli", Kind: "tool", EvidenceID: "read"}, {Name: "cbs", Kind: "tool", EvidenceID: "read"}}
	quote.Evidence[0].Tool.Result = mustNoteJSON(quote.Items[0].Text)
	note, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), quote)
	if err != nil {
		t.Fatal(err)
	}
	note = processedExternal(t, h, note)
	if note.Results[0].Status != "published" {
		t.Fatal(note.Results)
	}
	got, err := h.Store.Retrieve(t.Context(), c.Scope, "tccli DescribeDisks", 10)
	if err != nil || len(got.Matches) != 2 || got.Matches[0].ID != procedureID {
		t.Fatal("source quote hid the admitted procedure or its own explicit subject", got, err)
	}
	limited, err := h.Store.Retrieve(t.Context(), c.Scope, "tccli DescribeDisks", 1)
	if err != nil || len(limited.Matches) != 1 || limited.Matches[0].ID != procedureID {
		t.Fatal("quotation displaced admitted knowledge at the recall limit", limited, err)
	}
	ambiguous, err := h.Store.Retrieve(t.Context(), c.Scope, "cbs", 10)
	if err != nil || len(ambiguous.Query.Ambiguous) == 0 || len(ambiguous.Matches) != 0 {
		t.Fatal("caller query guessed between different cbs identities", ambiguous, err)
	}
}
