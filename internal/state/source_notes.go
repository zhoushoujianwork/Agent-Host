package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const SourceNoteKind = "source_note"
const SourceNoteSource = "external_source_note"

// SourceNoteText preserves the quotation and makes its evidence strength visible
// even when a consumer renders only the memory text.
func SourceNoteText(item ExternalItem) string {
	return fmt.Sprintf("Source note: %s\nImported source report; not fresh verification or permission to execute.\n\n%s", item.Title, item.Text)
}

func validateSourceNote(item ExternalItem, available map[string]ExternalEvidence) error {
	if item.Procedure != nil || !boundedExternalText(item.Title, 256) || !boundedExternalText(item.Text, 8192) || len(item.EvidenceIDs) != 1 {
		return errors.New("source_note requires a title, at most 8 KiB quoted text, one tool reference and no procedure")
	}
	e := available[item.EvidenceIDs[0]]
	var quoted string
	if e.Role != "tool" || e.Tool == nil || e.Tool.IsError == nil || *e.Tool.IsError || json.Unmarshal(e.Tool.Result, &quoted) != nil || quoted != item.Text {
		return errors.New("source_note text must equal its non-error tool result string; it is not a user statement")
	}
	return nil
}

// ExactSourceNote checks the complete quoted record within the admitted partition.
// Inactive records are returned too, so resubmission cannot silently revive them.
func (s *Store) ExactSourceNote(ctx context.Context, scope, text string) (Memory, bool, error) {
	var m Memory
	found := false
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		a, b := s.Partition()
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id='' AND text=?", a, b, scope, text).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return json.Unmarshal(raw, &m)
	})
	return m, found, err
}
