package cli

import (
	"context"
	"errors"

	"agent-host/internal/channel"
	"agent-host/internal/channel/feishu"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
)

func feishuIMConfig(cfg feishumeeting.Config) feishu.Config {
	return feishu.Config{Profile: cfg.Profile, AppID: cfg.AppID, TenantID: cfg.TenantID, BotOpenID: cfg.BotOpenID, OwnerOpenID: cfg.OwnerOpenID}
}

// Source kind selects the reply transport, independent of task text and agent.
type feishuTextReplier struct {
	IM      channel.TextReplier
	Meeting channel.TextReplier
}

func (r feishuTextReplier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	if target.Kind == state.ReplyTargetFeishu && r.IM != nil {
		return r.IM.ReplyText(ctx, target, text)
	}
	if target.Kind == state.ReplyTargetFeishuMeeting && r.Meeting != nil {
		return r.Meeting.ReplyText(ctx, target, text)
	}
	return feishumeeting.Failed, errors.New("Feishu reply transport is not enabled for this destination")
}

func (c agentCatalog) selectFeishuAgent(account string, message channel.Message) (channel.AgentSelection, error) {
	id, audience := c.DefaultAgent, state.OwnerAudienceID
	if message.ConversationType == "2" {
		audience = channel.Key("group-audience-v1", state.ReplyTargetFeishu, account, message.TenantID, message.ConversationID)
		for _, binding := range c.GroupBindings {
			if binding.ConversationID == message.ConversationID {
				id = binding.AgentID
				break
			}
		}
	}
	if _, err := c.definition(id); err != nil {
		return channel.AgentSelection{}, err
	}
	return channel.AgentSelection{AgentID: id, AudienceID: audience}, nil
}
