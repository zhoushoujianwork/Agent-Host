package feishu

import (
	"context"
	"encoding/json"
	"errors"

	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
)

const maxReactionIDBytes = 512

type Reactor struct {
	Config Config
	Client feishumeeting.Client
}

// AddReaction makes one mutation. Only a confirmed, own-app acknowledgement
// returns a provider ID for the Host to retain before a later removal.
func (r Reactor) AddReaction(ctx context.Context, target state.ChannelReplyTarget, label string) (string, string, error) {
	if err := r.validateTarget(ctx, target); err != nil {
		return "", Failed, err
	}
	emoji := reactionEmoji(label)
	if emoji == "" {
		return "", Failed, errors.New("Feishu reaction stage is unsupported")
	}
	params, _ := json.Marshal(map[string]string{"message_id": target.MessageID})
	data, _ := json.Marshal(map[string]map[string]string{"reaction_type": {"emoji_type": emoji}})
	args := []string{"--profile", r.Config.Profile, "im", "reactions", "create", "--as", "bot",
		"--params", string(params), "--data", string(data), "--format", "json"}
	return r.mutate(ctx, target, args, "", emoji)
}

// RemoveReaction uses only the Host's saved provider ID. It does not list or
// infer another operator's reaction, and ambiguous mutations are not replayed.
func (r Reactor) RemoveReaction(ctx context.Context, target state.ChannelReplyTarget, providerID string) (string, error) {
	if err := r.validateTarget(ctx, target); err != nil {
		return Failed, err
	}
	if !validReactionID(providerID, target) {
		return Failed, errors.New("Feishu saved reaction ID is invalid")
	}
	params, _ := json.Marshal(map[string]string{"message_id": target.MessageID, "reaction_id": providerID})
	args := []string{"--profile", r.Config.Profile, "im", "reactions", "delete", "--as", "bot",
		"--params", string(params), "--format", "json"}
	_, status, err := r.mutate(ctx, target, args, providerID, "")
	return status, err
}

func (r Reactor) validateTarget(ctx context.Context, target state.ChannelReplyTarget) error {
	if err := r.Config.Validate(); err != nil {
		return err
	}
	if target.Kind != state.ReplyTargetFeishu || !chatID.MatchString(target.ID) || !messageID.MatchString(target.MessageID) {
		return errors.New("Feishu reaction requires an original IM message target")
	}
	return ctx.Err()
}

func reactionEmoji(label string) string {
	switch label {
	case "已收到":
		return "Get"
	case "处理中":
		return "Typing"
	case "已完成":
		return "DONE"
	case "打叉":
		return "ERROR"
	default:
		return ""
	}
}

func stageEmoji(emoji string) bool {
	return emoji == "Get" || emoji == "Typing" || emoji == "DONE" || emoji == "ERROR"
}

func validReactionID(id string, target state.ChannelReplyTarget) bool {
	// Reaction IDs are opaque platform strings, not IM message or chat IDs.
	return identifier(id, maxReactionIDBytes) && id != target.MessageID && id != target.ID
}

func (r Reactor) mutate(ctx context.Context, target state.ChannelReplyTarget, args []string, expectedID, expectedEmoji string) (string, string, error) {
	client := r.Client
	if client == nil {
		client = feishumeeting.CLIClient{}
	}
	out, runErr := client.Run(ctx, args...)
	if !out.Started {
		return "", Failed, errors.New("Feishu reaction command did not start")
	}
	e, fromStdout, err := replyEnvelope(out)
	if err != nil {
		return "", Unknown, errors.New("Feishu reaction outcome is unconfirmed; do not retry automatically")
	}
	ok, present := e["ok"]
	if !present || string(ok) != "true" && string(ok) != "false" {
		return "", Unknown, errors.New("Feishu reaction result is invalid")
	}
	identity, err := stringField(e, "identity")
	if err != nil || identity != "" && identity != "bot" {
		return "", Unknown, errors.New("Feishu reaction identity is unconfirmed")
	}
	if string(ok) == "false" {
		if reactionRejected(e["error"]) {
			return "", Failed, errors.New("Feishu rejected reaction validation or authorization")
		}
		return "", Unknown, errors.New("Feishu reaction outcome is unconfirmed; do not retry automatically")
	}
	if runErr != nil || identity != "bot" || !fromStdout {
		return "", Unknown, errors.New("Feishu reaction outcome is unconfirmed; do not retry automatically")
	}
	data, err := object(e["data"])
	if err != nil {
		return "", Unknown, errors.New("Feishu reaction acknowledgement is invalid")
	}
	id, idErr := stringField(data, "reaction_id")
	operator, operatorErr := object(data["operator"])
	operatorType, typeErr := stringField(operator, "operator_type")
	operatorID, operatorIDErr := stringField(operator, "operator_id")
	reaction, reactionErr := object(data["reaction_type"])
	emoji, emojiErr := stringField(reaction, "emoji_type")
	if idErr != nil || operatorErr != nil || typeErr != nil || operatorIDErr != nil || reactionErr != nil || emojiErr != nil ||
		!validReactionID(id, target) || expectedID != "" && id != expectedID || operatorType != "app" || operatorID != r.Config.AppID ||
		!stageEmoji(emoji) || expectedEmoji != "" && emoji != expectedEmoji {
		return "", Unknown, errors.New("Feishu reaction acknowledgement does not match its own-app mutation")
	}
	return id, Accepted, nil
}

func reactionRejected(raw json.RawMessage) bool {
	failure, err := object(raw)
	if err != nil {
		return false
	}
	kind, err := stringField(failure, "type")
	if err != nil {
		return false
	}
	switch kind {
	case "validation", "auth", "authorization", "permission":
		return true
	case "api":
		var code int
		if json.Unmarshal(failure["code"], &code) != nil {
			return false
		}
		// Documented validation/access failures confirm rejection. In particular,
		// 231015 (another request is processing) remains an unknown mutation.
		switch code {
		case 230110, 231001, 231002, 231003, 231004, 231005, 231006, 231007, 231008, 231010, 231011,
			231012, 231013, 231014, 231017, 231018, 231019, 231020, 231021, 231022, 232009:
			return true
		}
	}
	return false
}
