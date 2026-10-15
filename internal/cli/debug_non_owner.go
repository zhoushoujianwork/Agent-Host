package cli

import "agent-host/internal/host"

func applyNonOwnerDebugPolicy(o *options) {
	if !o.debugNonOwner {
		return
	}
	o.permissionMode = "dontAsk"
	o.hostInstructions += "\n\n" + channelAuthorizationInstructions("", false) + "\nLocal CLI debug simulation: this request has no authenticated channel owner and no approval delivery transport. Only read tools are available. Do not claim an owner was mentioned or an approval was queued. Ask the owner to submit blocked work through a configured channel."
}

func configureNonOwnerDebugHost(h *host.Host) {
	h.Config.RestrictedReadOnly = true
	h.Config.MCPServers = nil
	h.Config.KnowledgeHints = nil
	h.Config.ValidateKnowledge = nil
	h.Config.HistoryHome = ""
	h.Config.ContextHome = ""
	h.Config.SkillsPreselected = false
	h.Config.ExpectedSkillDigest = ""
	h.Config.RequestApproval = nil
	h.Config.ApprovalExecutable = ""
}
