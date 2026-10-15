package host

import (
	"agent-host/internal/state"
	"context"
	"encoding/json"
	"errors"
)

// RunApprovedChannel retains the owner's actual reply as input evidence while
// supplying only the recorded approved action through trusted Host instructions.
func (h *Host) RunApprovedChannel(ctx context.Context, s state.Session, messageID, approvalID string) (state.Run, error) {
	a, err := h.Store.OperationApproval(ctx, approvalID)
	if err != nil {
		return state.Run{}, err
	}
	m, err := h.Store.ChannelMessage(ctx, messageID)
	if err != nil {
		return state.Run{}, err
	}
	if h.Config.RestrictedReadOnly || a.Status != "consumed" || a.RunID != "" || a.ApprovalReceiptID != messageID || a.OwnerID != m.SenderID || a.Workspace != s.WorkingDirectory() {
		return state.Run{}, errors.New("approved operation grant is unavailable or incompatible")
	}
	payload, _ := json.Marshal(struct{ ApprovalID, Operation, Target, Impact, OriginalReceiptID string }{a.ID, a.Operation, a.Target, a.Impact, a.Source.ID})
	copy := *h
	copy.Config.ApprovedOperationID = a.ID
	copy.Config.HostInstructions += "\n\nThe Host verified this current sender's one-use owner approval. Execute only the recorded operation and target below. Do not expand scope or replay the member's conversation. Original request and approval are separate evidence.\n" + string(payload)
	return copy.RunChannel(ctx, s, m.Text, messageID)
}
