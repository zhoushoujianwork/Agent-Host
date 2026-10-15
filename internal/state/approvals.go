package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const schemaApprovals = `CREATE TABLE IF NOT EXISTS operation_approvals (
 id TEXT PRIMARY KEY, status TEXT NOT NULL, payload BLOB NOT NULL
);`

// OperationApproval preserves the proposal, actual approver and execution as
// separate evidence. Consumed grants are never automatically replayed.
type OperationApproval struct {
	ID                string         `json:"id"`
	Source            ChannelMessage `json:"source"`
	OwnerID           string         `json:"owner_id"`
	Operation         string         `json:"operation"`
	Target            string         `json:"target"`
	Impact            string         `json:"impact"`
	ProjectIDs        []string       `json:"project_ids,omitempty"`
	PrimaryID         string         `json:"primary_id,omitempty"`
	Workspace         string         `json:"workspace"`
	Status            string         `json:"status"`
	MessageID         string         `json:"message_id,omitempty"`
	ApprovalReceiptID string         `json:"approval_receipt_id,omitempty"`
	RunID             string         `json:"run_id,omitempty"`
	ProposalRunID     string         `json:"proposal_run_id,omitempty"`
	Error             string         `json:"error,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	ExpiresAt         time.Time      `json:"expires_at"`
}

func (s *Store) CreateOperationApproval(ctx context.Context, a OperationApproval) (bool, error) {
	if a.ID == "" || a.OwnerID == "" || a.Source.ID == "" || a.Source.Channel == "" || a.Operation == "" || a.Target == "" || a.Impact == "" || a.Status != "sending" || !a.ExpiresAt.After(a.CreatedAt) {
		return false, errors.New("invalid operation approval")
	}
	b, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	r, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO operation_approvals(id,status,payload) VALUES(?,?,?)", a.ID, a.Status, b)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) OperationApproval(ctx context.Context, id string) (OperationApproval, error) {
	var a OperationApproval
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM operation_approvals WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &a)
	}
	return a, err
}

func (s *Store) updateApproval(ctx context.Context, a OperationApproval, from string) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, "UPDATE operation_approvals SET status=?,payload=? WHERE id=? AND status=?", a.Status, b, a.ID, from)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("approval already handled or changed")
	}
	return err
}

func (s *Store) RecordApprovalDelivery(ctx context.Context, id, providerID, status, cause string) error {
	a, err := s.OperationApproval(ctx, id)
	if err != nil {
		return err
	}
	if status != "pending" && status != "failed" && status != "unknown" {
		return errors.New("invalid approval delivery state")
	}
	a.MessageID, a.Status, a.Error = providerID, status, cause
	return s.updateApproval(ctx, a, "sending")
}

// FindOperationApproval uses only provider reply metadata or an explicit opaque
// request code. A caller still must validate the authenticated owner's identity.
func (s *Store) FindOperationApproval(ctx context.Context, m ChannelMessage, token string) (OperationApproval, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM operation_approvals")
	if err != nil {
		return OperationApproval{}, err
	}
	defer rows.Close()
	var found OperationApproval
	count := 0
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return found, err
		}
		var a OperationApproval
		if err := json.Unmarshal(b, &a); err != nil {
			return found, err
		}
		if a.Source.Channel != m.Channel || a.Source.AccountID != m.AccountID || a.Source.TenantID != m.TenantID || a.Source.ConversationID != m.ConversationID {
			continue
		}
		if token != "" {
			if a.ID != token || m.Channel != "dingtalk" {
				continue
			}
		} else if m.ReplyToID == "" || a.MessageID == "" || m.ReplyToID != a.MessageID {
			continue
		}
		found = a
		count++
	}
	if err := rows.Err(); err != nil {
		return found, err
	}
	if count != 1 {
		return found, sql.ErrNoRows
	}
	return found, nil
}

func (s *Store) ConsumeOperationApproval(ctx context.Context, id, currentOwner string, reply ChannelMessage, approve bool, now time.Time) (OperationApproval, error) {
	a, err := s.OperationApproval(ctx, id)
	if err != nil {
		return a, err
	}
	if currentOwner == "" || currentOwner != a.OwnerID || reply.SenderID != currentOwner || a.Source.Channel != reply.Channel || a.Source.AccountID != reply.AccountID || a.Source.TenantID != reply.TenantID || a.Source.ConversationID != reply.ConversationID {
		return a, errors.New("approval requires its configured owner and conversation")
	}
	stored, err := s.ChannelMessage(ctx, reply.ID)
	if err != nil || reply.ID == "" || reply.MessageID == "" || stored.SenderID != reply.SenderID || stored.Channel != reply.Channel || stored.AccountID != reply.AccountID || stored.TenantID != reply.TenantID || stored.ConversationID != reply.ConversationID || stored.MessageID != reply.MessageID || stored.ReplyToID != reply.ReplyToID || stored.Text != reply.Text {
		return a, errors.New("approval requires the actual persisted reply receipt")
	}
	parts := strings.Fields(strings.TrimSpace(reply.Text))
	correlated := false
	if reply.Channel == "dingtalk" {
		if len(parts) == 3 && strings.HasPrefix(parts[0], "@") {
			parts = parts[1:]
		}
		correlated = len(parts) == 2 && parts[1] == a.ID
	} else {
		correlated = len(parts) == 1 && a.MessageID != "" && reply.ReplyToID == a.MessageID
	}
	if !correlated || len(parts) == 0 {
		return a, errors.New("approval reply does not select this request")
	}
	word := strings.ToLower(parts[0])
	if approve && word != "ok" && word != "approve" || !approve && word != "no" && word != "reject" {
		return a, errors.New("approval reply does not match the recorded decision")
	}
	if a.Status != "pending" {
		return a, errors.New("approval is not pending; no action replayed")
	}
	a.ApprovalReceiptID = reply.ID
	if !now.Before(a.ExpiresAt) {
		a.Status = "expired"
	} else if approve {
		a.Status = "consumed"
	} else {
		a.Status = "rejected"
	}
	if err = s.updateApproval(ctx, a, "pending"); err != nil {
		return a, err
	}
	if a.Status == "expired" {
		return a, errors.New("approval expired; action was not executed")
	}
	return a, nil
}

func (s *Store) FinishOperationApproval(ctx context.Context, id, runID, cause string) error {
	a, err := s.OperationApproval(ctx, id)
	if err != nil {
		return err
	}
	a.RunID, a.Error = runID, cause
	// Keep consumed even after failure: retries need a new explicit owner request.
	return s.updateApproval(ctx, a, "consumed")
}

func (s *Store) BindOperationApprovalRun(ctx context.Context, id, receiptID, runID string) error {
	a, err := s.OperationApproval(ctx, id)
	if err != nil {
		return err
	}
	if a.Status != "consumed" || a.RunID != "" || a.ApprovalReceiptID != receiptID || runID == "" {
		return errors.New("approval run grant already used or invalid")
	}
	a.RunID = runID
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, "UPDATE operation_approvals SET payload=? WHERE id=? AND status='consumed' AND COALESCE(json_extract(payload,'$.run_id'),'')=''", b, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("approval run grant already used")
	}
	return err
}

// RefreshOperationApprovals expires grants and permanently invalidates old owner
// bindings at worker startup, even when no approval reply arrives.
func (s *Store) RefreshOperationApprovals(ctx context.Context, channel, account, currentOwner string, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM operation_approvals WHERE status IN ('pending','sending')")
	if err != nil {
		return err
	}
	var changed []OperationApproval
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		var a OperationApproval
		if err := json.Unmarshal(b, &a); err != nil {
			rows.Close()
			return err
		}
		if a.Source.Channel != channel || a.Source.AccountID != account {
			continue
		}
		if a.OwnerID != currentOwner {
			a.Error = "configured owner changed"
			changed = append(changed, a)
		} else if !now.Before(a.ExpiresAt) {
			a.Error = "approval expired"
			changed = append(changed, a)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, a := range changed {
		from := a.Status
		if a.OwnerID != currentOwner {
			a.Status = "canceled"
		} else {
			a.Status = "expired"
		}
		if err := s.updateApproval(ctx, a, from); err != nil {
			return err
		}
	}
	return nil
}
