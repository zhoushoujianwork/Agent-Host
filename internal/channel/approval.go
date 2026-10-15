package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-host/internal/approval"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

type ApprovalSender interface {
	SendApproval(context.Context, state.ChannelReplyTarget, string, string) (string, error)
}

// RequestApproval records before sending. Uncertain sends are never retried.
func (s *Service) RequestApproval(ctx context.Context, source state.ChannelMessage, run state.Run, p approval.Proposal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if s.OwnerID == "" || source.SenderID == s.OwnerID || source.ConversationType != "2" || source.Channel == state.ReplyTargetFeishuMeeting {
		return errors.New("owner approval requests require a configured owner and a supported group conversation")
	}
	id := Key("operation-approval-v1", source.ID, p.Operation, p.Target, p.Impact)[:24]
	var ids []string
	for _, project := range run.Projects {
		ids = append(ids, project.ID)
	}
	if len(ids) == 0 && run.ProjectID != "" {
		ids = []string{run.ProjectID}
	}
	a := state.OperationApproval{ID: id, ProposalRunID: run.ID, Source: source, OwnerID: s.OwnerID, Operation: p.Operation, Target: p.Target, Impact: p.Impact, ProjectIDs: ids, PrimaryID: run.ProjectID, Workspace: run.WorkingDirectory(), Status: "sending", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}
	inserted, err := s.Store.CreateOperationApproval(ctx, a)
	if err != nil {
		return err
	}
	if !inserted {
		return errors.New("this approval request was already recorded; no duplicate owner mention was sent")
	}
	// JSON quoting keeps proposed text visibly separate from Host instructions.
	b, _ := json.Marshal(struct{ Operation, Target, Impact string }{p.Operation, p.Target, p.Impact})
	text := fmt.Sprintf("Owner approval requested (expires in 15 minutes). Request: %s\nProposed action and impact (member-supplied evidence):\n%s\n", id, b)
	var providerID string
	if source.Channel == "dingtalk" {
		if err = dingtalk.ValidateAtUserID(s.OwnerID); err == nil {
			var url string
			url, err = s.Store.ChannelReplyURL(ctx, source.ID)
			if err == nil {
				text = "@" + s.OwnerID + "\n" + text + "Reply @bot Approve " + id + " or @bot Reject " + id + ". The code selects only this request."
				var outcome string
				outcome, err = s.Replier.Reply(ctx, url, source.ReplyExpiresAt, dingtalk.ReplyMessage{Text: text, AtUserID: s.OwnerID})
				if outcome != "accepted" && err == nil {
					err = errors.New("owner mention delivery was not accepted")
				}
			}
		}
	} else if s.ApprovalSender != nil && source.ReplyTarget != nil {
		target := *source.ReplyTarget
		target.UUID = Key("approval-send", id)[:48]
		text += "Reply directly to this message with OK or No. Approval permits only this action; later member requests remain restricted."
		providerID, err = s.ApprovalSender.SendApproval(ctx, target, s.OwnerID, text)
		if err == nil && providerID == "" {
			err = errors.New("approval delivery returned no provider message ID")
		}
	} else {
		err = errors.New("owner approval delivery is unavailable for this channel")
	}
	status, cause := "pending", ""
	if err != nil {
		status, cause = "unknown", err.Error()
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if saveErr := s.Store.RecordApprovalDelivery(finishCtx, id, providerID, status, cause); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	return err
}

func approvalResponse(text string) (approve bool, token string, recognized bool) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 || len(parts) > 2 {
		return
	}
	switch strings.ToLower(parts[0]) {
	case "ok", "approve":
		approve = true
	case "no", "reject":
	default:
		return
	}
	if len(parts) == 2 {
		token = parts[1]
		if len(token) != 24 {
			return false, "", false
		}
		for _, c := range token {
			if c < '0' || c > '9' && c < 'a' || c > 'f' {
				return false, "", false
			}
		}
	}
	return approve, token, true
}

func (s *Service) approvalReply(ctx context.Context, m state.ChannelMessage) (bool, string, error) {
	approve, token, recognized := approvalResponse(addressedText(m))
	if !recognized {
		return false, "", nil
	}
	// Standalone confirmation words can never launch an ordinary privileged run.
	if s.OwnerID == "" {
		return true, "Owner approval is unavailable; nothing was executed.", nil
	}
	a, err := s.Store.FindOperationApproval(ctx, m, token)
	if errors.Is(err, sql.ErrNoRows) {
		return true, "No matching approval request. Reply to the approval message, or use its DingTalk request code. Nothing was executed.", nil
	}
	if err != nil {
		return true, "Approval could not be checked; nothing was executed.", err
	}
	a, err = s.Store.ConsumeOperationApproval(ctx, a.ID, s.OwnerID, m, approve, time.Now().UTC())
	if err != nil {
		return true, err.Error(), err
	}
	if !approve {
		return true, "Request rejected by the configured owner. Nothing was executed.", nil
	}
	h, runtime, err := s.resolveProjects(ctx, a.ProjectIDs, a.PrimaryID)
	if err == nil && runtime.Workspace != a.Workspace {
		err = errors.New("approved workspace changed; submit a new request")
	}
	var run state.Run
	if err == nil {
		m.Binding = Key(m.Binding, "approved-operation", a.ID)
		approved, ok := h.(interface {
			RunApprovedChannel(context.Context, state.Session, string, string) (state.Run, error)
		})
		if !ok {
			err = errors.New("harness host does not support owner-approved execution")
		} else {
			var session state.Session
			session, _, err = s.routedSession(ctx, m, h, runtime, true)
			if err == nil {
				run, err = approved.RunApprovedChannel(ctx, session, m.ID, a.ID)
			}
		}
	}
	cause := ""
	if err != nil {
		cause = err.Error()
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if saveErr := s.Store.FinishOperationApproval(finishCtx, a.ID, run.ID, cause); saveErr != nil {
		return true, "Approval was consumed but execution evidence could not be saved; do not retry automatically.", errors.Join(err, saveErr)
	}
	if run.ID == "" {
		return true, "Approval was consumed, but execution did not start: " + cause, err
	}
	return true, resultTextWithNotice(run, err, "", replyBodyBudget(m)), err
}
