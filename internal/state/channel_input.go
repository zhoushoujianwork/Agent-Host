package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// ChannelInput contains task material, never transport download credentials.
type ChannelInput struct {
	Attachments []ChannelAttachment `json:"attachments,omitempty"`
	Quote       *ChannelQuote       `json:"quote,omitempty"`
}

type ChannelAttachment struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	MediaType   string `json:"media_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Path        string `json:"path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
}

type ChannelQuote struct {
	MessageID   string `json:"message_id,omitempty"`
	MessageType string `json:"message_type,omitempty"`
	Body        string `json:"body,omitempty"`
	Title       string `json:"title,omitempty"`
	SummaryOnly bool   `json:"summary_only,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

const schemaChannelInputV11 = `
ALTER TABLE channel_messages ADD COLUMN input_capabilities BLOB NOT NULL DEFAULT '[]';
PRAGMA user_version=11;
`

func receiptInput(input *ChannelInput) *ChannelInput {
	if input == nil {
		return nil
	}
	copy := *input
	copy.Attachments = append([]ChannelAttachment(nil), input.Attachments...)
	for i := range copy.Attachments {
		copy.Attachments[i].Path, copy.Attachments[i].SHA256, copy.Attachments[i].ContentType, copy.Attachments[i].Bytes = "", "", "", 0
	}
	return &copy
}

func sameInput(a, b *ChannelInput) bool { return reflect.DeepEqual(receiptInput(a), receiptInput(b)) }

// ChannelInputCapabilities reads protected receipt credentials only for an
// unexecuted claimed input. Public record/list methods never select this column.
func (s *Store) ChannelInputCapabilities(ctx context.Context, id string) ([]string, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT input_capabilities FROM channel_messages WHERE id=? AND status='claimed' AND COALESCE(json_extract(payload,'$.run_id'),'')=''`, id).Scan(&raw)
	if err != nil {
		return nil, wrap("read channel input capabilities", err)
	}
	var codes []string
	err = json.Unmarshal(raw, &codes)
	return codes, err
}

// SaveChannelAttachments records verified local bytes before a native run can
// start. It preserves the originally received names, kinds and size claims.
func (s *Store) SaveChannelAttachments(ctx context.Context, id string, attachments []ChannelAttachment) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != "claimed" || m.RunID != "" || m.Input == nil {
			return errors.New("attachments require an unexecuted claimed input")
		}
		next := *m.Input
		next.Attachments = attachments
		if !sameInput(m.Input, &next) {
			return errors.New("downloaded attachments differ from receipt")
		}
		for _, a := range attachments {
			if a.Path == "" || len(a.SHA256) != 64 || a.ContentType == "" || a.Bytes <= 0 {
				return errors.New("attachment evidence is incomplete")
			}
		}
		m.Input = &next
		return writeChannelMessage(ctx, tx, m)
	})
}
