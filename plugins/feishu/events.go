package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Event preserves the platform's event ID, type and payload, including event
// kinds the caller does not yet understand. Payload is untrusted source content.
type Event struct {
	ID      string          `json:"event_id"`
	Type    string          `json:"event_type"`
	Payload json.RawMessage `json:"payload"`
}

// EventPage contains a single CLI poll result. PollEvents neither persists nor
// advances a cursor. The caller commits it only after accepting the whole page.
type EventPage struct {
	Events  []Event
	Cursor  string
	HasMore bool
}

// PollEvents requests the CLI's bounded page-all operation. HasMore may still be
// true; the caller must follow the returned cursor without replaying mutations.
// Poll no more frequently than every five seconds, including startup baselines.
func (c *MeetingClient) PollEvents(ctx context.Context, meetingID, cursor string) (EventPage, error) {
	var page EventPage
	if !meetingPattern.MatchString(meetingID) || !boundedIdentifier(cursor, 8192, true) {
		return page, errors.New("invalid Feishu meeting or cursor")
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	args := append(c.arguments("+meeting-events", meetingID), "--page-all", "--format", "json")
	if cursor != "" {
		args = append(args, "--page-token", cursor)
	}
	out, runErr := c.transport.Run(ctx, args...)
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if runErr != nil {
		return page, errors.New("Feishu event polling failed; progress was not advanced")
	}
	e, err := decodeEnvelope(out)
	if err != nil || !*e.OK {
		return page, errors.New("Feishu event polling was not confirmed; progress was not advanced")
	}
	var response struct {
		Meeting struct {
			ID string `json:"id"`
		} `json:"meeting"`
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
		Events  json.RawMessage `json:"events"`
		Cursor  string          `json:"page_token"`
		HasMore bool            `json:"has_more"`
	}
	if json.Unmarshal(e.Data, &response) != nil || response.Meeting.ID != "" && response.Meeting.ID != meetingID || response.Identity.ID != c.config.BotOpenID || len(response.Events) == 0 {
		return page, errors.New("Feishu event response has invalid meeting, bot identity, or events")
	}
	if !boundedIdentifier(response.Cursor, 8192, true) || response.HasMore && (response.Cursor == "" || response.Cursor == cursor) {
		return page, errors.New("Feishu event pagination did not provide a valid advancing cursor")
	}
	if json.Unmarshal(response.Events, &page.Events) != nil {
		return EventPage{}, errors.New("Feishu events are malformed")
	}
	for _, event := range page.Events {
		if !boundedIdentifier(event.ID, 256, false) {
			return EventPage{}, errors.New("Feishu event ID is missing or invalid")
		}
		var payload struct {
			Meeting struct {
				ID string `json:"id"`
			} `json:"meeting"`
		}
		if len(event.Payload) > 0 && json.Unmarshal(event.Payload, &payload) != nil {
			return EventPage{}, errors.New("Feishu event payload is malformed")
		}
		if payload.Meeting.ID != "" && payload.Meeting.ID != meetingID {
			return EventPage{}, errors.New("Feishu event belongs to a different meeting")
		}
	}
	page.Cursor, page.HasMore = response.Cursor, response.HasMore
	return page, nil
}

// ChatItem is the platform's meeting-text item. It retains source identity;
// applications must apply their own sender authorization and echo filtering.
type ChatItem struct {
	Content     string          `json:"content"`
	MessageType json.RawMessage `json:"message_type"`
	SendTime    json.RawMessage `json:"send_time"`
	Operator    struct {
		ID              string          `json:"id"`
		UserType        json.RawMessage `json:"user_type"`
		ParticipantType string          `json:"participant_type"`
	} `json:"operator"`
}

// ChatItems decodes a chat_received event without interpreting its text as a
// command. Other event kinds return no items; their raw payload remains intact.
func (e Event) ChatItems() ([]ChatItem, error) {
	if e.Type != "chat_received" {
		return nil, nil
	}
	var payload struct {
		Items []ChatItem `json:"chat_received_items"`
	}
	if json.Unmarshal(e.Payload, &payload) != nil {
		return nil, errors.New("Feishu chat items are malformed")
	}
	return payload.Items, nil
}

// SourceTime accepts the raw milliseconds or the CLI's RFC3339 timestamp. An
// unparseable timestamp remains unknown rather than becoming the local time.
func SourceTime(raw json.RawMessage) time.Time {
	value := strings.Trim(string(raw), "\"")
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func scalar(raw json.RawMessage, value string) bool {
	return string(raw) == value || string(raw) == strconv.Quote(value)
}

func boundedIdentifier(value string, limit int, empty bool) bool {
	return (empty || value != "") && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
