package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func messageTarget(a Args) error {
	if (a.ConversationID == "") == (a.PeerID == "") {
		return errors.New("provide exactly one conversation_id or peer_open_dingtalk_id from a verified source")
	}
	for _, id := range []string{a.ConversationID, a.PeerID} {
		if len(id) > 512 || id != strings.TrimSpace(id) || strings.ContainsAny(id, "\x00\r\n") || !utf8.ValidString(id) {
			return errors.New("invalid DingTalk message destination")
		}
	}
	return nil
}

// The generic command entry point must preserve the dedicated message tools'
// identity, deduplication and bounded-read contracts as well.
func enforceMessaging(c Config, a *Args) error {
	if a.Command != "chat message send" && a.Command != "chat +chat-messages" {
		return nil
	}
	p := a.Parameters
	if err := messageTarget(Args{ConversationID: p["conversation-id"], PeerID: p["open-dingtalk-id"]}); err != nil {
		return err
	}
	allowed := []string{"conversation-id", "open-dingtalk-id"}
	if a.Command == "chat message send" {
		allowed = append(allowed, "content", "title", "idempotency-key")
		if !operationID.MatchString(a.OperationID) || strings.TrimSpace(p["content"]) == "" || len(p["content"]) > 16000 || len(p["title"]) > 256 {
			return errors.New("personal sending requires a bounded message and operation_id")
		}
		p["idempotency-key"] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(c.Profile+":"+a.OperationID)).String()
	} else {
		allowed = append(allowed, "start", "end", "order", "page-all", "page-limit", "max-items", "no-reactions")
		start, e := time.Parse(time.RFC3339, p["start"])
		end, e2 := time.Parse(time.RFC3339, p["end"])
		limit, e3 := strconv.Atoi(p["max-items"])
		if e != nil || e2 != nil || !end.After(start) || end.Sub(start) > 24*time.Hour || e3 != nil || limit < 1 || limit > 20 {
			return errors.New("personal history requires a window at most 24 hours and max-items 1-20")
		}
		p["order"], p["page-all"], p["page-limit"], p["no-reactions"] = "asc", "true", "1", "true"
	}
	for key := range p {
		if !slices.Contains(allowed, key) {
			return errors.New("parameter is outside the personal messaging scope")
		}
	}
	return nil
}

func (s Service) messageCall(ctx context.Context, c Config, configured bool, name string, a Args) (any, error) {
	if !configured || !slices.Contains(c.Services, "chat") {
		return nil, errors.New("enable chat in the owner workspace services and complete owner login first")
	}
	command := "chat message query-send-status"
	params := map[string]string{}
	if name == "get_send_status" {
		if strings.TrimSpace(a.TaskID) == "" || len(a.TaskID) > 512 || strings.ContainsAny(a.TaskID, "\x00\r\n") {
			return nil, errors.New("provide an open_task_id from the official send receipt")
		}
		params["open-task-id"] = a.TaskID
	} else {
		if err := messageTarget(a); err != nil {
			return nil, err
		}
		if a.ConversationID != "" {
			params["conversation-id"] = a.ConversationID
		} else {
			params["open-dingtalk-id"] = a.PeerID
		}
		if name == "send_message" {
			if !operationID.MatchString(a.OperationID) {
				return nil, errors.New("sending requires a unique operation_id")
			}
			if strings.TrimSpace(a.Text) == "" || len(a.Text) > 16000 || !utf8.ValidString(a.Text) || len(a.Title) > 256 || !utf8.ValidString(a.Title) {
				return nil, errors.New("message is empty, invalid or too large")
			}
			command = "chat message send"
			params["content"] = a.Text
			if a.Title != "" {
				params["title"] = a.Title
			}
			// Preserve the official backend's UUID deduplication in addition to
			// the Host ledger. Retrying transport requests keeps the same UUID.
			params["idempotency-key"] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(c.Profile+":"+a.OperationID)).String()
		} else {
			command = "chat +chat-messages"
			after, err := time.Parse(time.RFC3339, a.After)
			if err != nil {
				return nil, errors.New("after must be RFC3339")
			}
			before, err := time.Parse(time.RFC3339, a.Before)
			if err != nil || !before.After(after) || before.Sub(after) > 24*time.Hour {
				return nil, errors.New("message read window must be positive and at most 24 hours")
			}
			if a.Limit == 0 {
				a.Limit = 10
			}
			if a.Limit < 1 || a.Limit > 20 {
				return nil, errors.New("message limit must be 1-20")
			}
			params["start"], params["end"] = after.UTC().Format(time.RFC3339), before.UTC().Format(time.RFC3339)
			params["order"], params["page-all"], params["page-limit"], params["max-items"], params["no-reactions"] = "asc", "true", "1", strconv.Itoa(a.Limit), "true"
		}
	}
	raw, _ := json.Marshal(Args{Command: command, Parameters: params, OperationID: a.OperationID, Confirmed: a.Confirmed})
	return s.Call(ctx, "execute_command", raw)
}
