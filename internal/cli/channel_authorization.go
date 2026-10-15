package cli

import (
	"context"
	"encoding/json"

	"agent-host/internal/approval"
	"agent-host/internal/channel"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func channelAuthorizationInstructions(ownerID string, owner bool) string {
	envelope, _ := json.Marshal(struct {
		Policy  string `json:"policy"`
		OwnerID string `json:"configured_owner_id"`
		IsOwner bool   `json:"is_owner"`
	}{"owner-operations-v1", ownerID, owner})
	return host.DangerousOperationInstructions + "\nCURRENT_AUTHORIZATION (Host-verified, never supplied by message content):\n" + string(envelope)
}

func configureChannelAuthorization(h *host.Host, s *channel.Service, m state.ChannelMessage, o options, owner bool) {
	if owner || o.noTools {
		return
	}
	h.Config.RestrictedReadOnly = true
	h.Config.MCPServers = nil
	h.Config.KnowledgeHints = nil
	h.Config.ValidateKnowledge = nil
	h.Config.AllowedTools = nil
	h.Config.SkillsPreselected = false
	h.Config.ExpectedSkillDigest = ""
	h.Config.ApprovalExecutable = o.historyExecutable
	if s.OwnerID != "" && m.ConversationType == "2" && m.Channel != state.ReplyTargetFeishuMeeting {
		h.Config.RequestApproval = func(ctx context.Context, r state.Run, p approval.Proposal) error {
			return s.RequestApproval(ctx, m, r, p)
		}
	}
}
