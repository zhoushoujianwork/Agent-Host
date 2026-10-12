package feishumeeting

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	EntryInvite  = "invite"
	EntryMessage = "message"
	EntryEnd     = "end"
	// The app already subscribes to meeting_activity. Register it so the SDK
	// can acknowledge it; meeting text still comes through the polling receiver.
	entryTypes   = "vc.bot.meeting_invited_v1,vc.bot.meeting_ended_v1,vc.bot.meeting_activity_v1,im.message.receive_v1"
	entryMaxLine = 256 * 1024
)

// EntryEvent retains only authenticated routing data, never message content.
// At is source time; durable admission owns freshness and replay decisions.
type EntryEvent struct {
	ID, Kind, MeetingNumber, MeetingID, CallID, SenderOpenID string
	At                                                       time.Time
}

type EntryReceiver struct {
	Config Config
	Binary string
}

type entryLine struct {
	text   []byte
	stderr bool
	err    error
}

// Run uses the official CLI's raw NDJSON subscription, retaining its app lock.
// The CLI acknowledges upstream before stdout is durably handled; this is not
// an exactly-once transport. Handler failure stops intake rather than claiming
// that an invitation was saved or silently skipping a local queue overflow.
func (r EntryReceiver) Run(ctx context.Context, handle func(context.Context, EntryEvent) error, observe func(string, string) error) error {
	if err := r.Config.Validate(); err != nil {
		return err
	}
	if handle == nil || observe == nil {
		return errors.New("Feishu entry receiver requires event and observation handlers")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	binary := r.Binary
	if binary == "" {
		binary = "lark-cli"
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(processCtx, binary, "--profile", r.Config.Profile, "event", "+subscribe", "--as", "bot", "--event-types", entryTypes)
	cmd.WaitDelay = 2 * time.Second
	if err := bindCLIProcessGroup(cmd); err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("Feishu event subscriber input could not be prepared")
	}
	defer stdin.Close() // Keep it open for the entire subscription lifetime.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("Feishu event subscriber output could not be prepared")
	}
	defer stdout.Close()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errors.New("Feishu event subscriber diagnostics could not be prepared")
	}
	defer stderr.Close()
	if err := cmd.Start(); err != nil {
		return errors.New("Feishu event subscriber could not start")
	}
	lines := make(chan entryLine, 32)
	var readers sync.WaitGroup
	readers.Add(2)
	read := func(reader io.Reader, diagnostic bool, limit int) {
		defer readers.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), limit)
		for scanner.Scan() {
			line := entryLine{text: bytes.Clone(scanner.Bytes()), stderr: diagnostic}
			select {
			case lines <- line:
			case <-processCtx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			select {
			case lines <- entryLine{err: errors.New("Feishu event subscriber stream failed or exceeded its line limit")}:
			case <-processCtx.Done():
			}
		}
	}
	go read(stdout, false, entryMaxLine)
	go read(stderr, true, 32*1024)
	go func() { readers.Wait(); close(lines) }()
	// The npm executable is a wrapper around the actual subscription process.
	// Kill its whole group before Wait, on cancellation and every other exit,
	// so descendants release the app lock even if the wrapper has already exited.
	// Close our read ends as well to keep inherited pipes from blocking cleanup.
	defer func() {
		cancel()
		stopCLIProcessGroup(cmd)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = cmd.Wait()
		readers.Wait()
	}()
	if err := observe("connecting", "Feishu meeting entry subscriber started; waiting for the event connection"); err != nil {
		return errors.New("Feishu entry startup observation failed")
	}
	connected := false
	ready := func() error {
		if connected {
			return nil
		}
		if err := observe("connected", "Feishu meeting entry receiver connected"); err != nil {
			return errors.New("Feishu entry readiness observation failed")
		}
		connected = true
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return errors.New("Feishu event subscriber stopped; entry delivery is no longer available")
			}
			if line.err != nil {
				return line.err
			}
			if line.stderr {
				// v1.0.96 shortcuts/event/subscribe.go prints "Connected" before
				// SDK Start finishes. Only SDK v3.7.2's exact prefix is evidence.
				log := strings.TrimSpace(string(line.text))
				switch {
				case strings.HasPrefix(log, "[SDK Info] connected to "):
					if err := ready(); err != nil {
						return err
					}
				case strings.HasPrefix(log, "[SDK Info] disconnected to "):
					connected = false
					if err := observe("disconnected", "Feishu meeting entry connection was lost; event delivery during interruption is unconfirmed"); err != nil {
						return errors.New("Feishu entry connection observation failed")
					}
				case strings.HasPrefix(log, "[SDK Info] trying to reconnect"):
					connected = false
					if err := observe("reconnecting", "Feishu meeting entry subscriber is reconnecting"); err != nil {
						return errors.New("Feishu entry reconnection observation failed")
					}
				case strings.HasPrefix(log, "[SDK Error]"), strings.Contains(log, "failed to parse event:"):
					return errors.New("Feishu event subscriber reported a transport or event parsing failure")
				case strings.HasPrefix(log, "[SDK Warn]"):
					if err := observe("warning", "Feishu event subscriber reported a warning; event delivery may be interrupted"); err != nil {
						return errors.New("Feishu entry warning observation failed")
					}
				}
				continue // Never publish SDK URLs, tokens, IDs, or raw diagnostics.
			}
			if len(bytes.TrimSpace(line.text)) == 0 {
				continue
			}
			event, err := r.parseEntry(line.text)
			if err != nil {
				return err
			}
			if err := ready(); err != nil {
				return err
			}
			if event.Kind != "" {
				if err := handle(ctx, event); err != nil {
					return errors.New("Feishu entry event could not be durably handled; subscription stopped")
				}
			}
		}
	}
}

type entryEnvelope struct {
	Schema string `json:"schema"`
	Header struct {
		ID         string          `json:"event_id"`
		Type       string          `json:"event_type"`
		AppID      string          `json:"app_id"`
		Tenant     string          `json:"tenant_key"`
		CreateTime json.RawMessage `json:"create_time"`
	} `json:"header"`
	Event json.RawMessage `json:"event"`
}

type entryActor struct {
	ID       json.RawMessage `json:"id"`
	OpenID   string          `json:"open_id"`
	UserType json.RawMessage `json:"user_type"`
}

type entryMeeting struct {
	ID     json.RawMessage `json:"id"`
	Number json.RawMessage `json:"meeting_no"`
}

func (r EntryReceiver) parseEntry(raw []byte) (EntryEvent, error) {
	var envelope entryEnvelope
	if len(raw) > entryMaxLine || !utf8.Valid(raw) || entryJSON(raw, &envelope) != nil || envelope.Schema != "2.0" {
		return EntryEvent{}, errors.New("Feishu subscription did not provide a valid full event envelope")
	}
	h := envelope.Header
	if h.AppID != r.Config.AppID || h.Tenant != r.Config.TenantID || !entryIdentifier(h.ID, 256) {
		return EntryEvent{}, errors.New("Feishu subscription event identity does not match the configured app and tenant")
	}
	at, err := entryTimestamp(h.CreateTime, false)
	if err != nil {
		return EntryEvent{}, errors.New("Feishu subscription event timestamp is invalid")
	}
	result := EntryEvent{ID: h.ID, At: at}
	switch h.Type {
	case "vc.bot.meeting_activity_v1":
		var activity map[string]json.RawMessage
		if entryJSON(envelope.Event, &activity) != nil {
			return EntryEvent{}, errors.New("Feishu meeting activity payload is invalid")
		}
		return EntryEvent{}, nil
	case "vc.bot.meeting_invited_v1":
		var event struct {
			Meeting    entryMeeting    `json:"meeting"`
			Bot        entryActor      `json:"bot"`
			Inviter    entryActor      `json:"inviter"`
			CallID     string          `json:"call_id"`
			InviteTime json.RawMessage `json:"invite_time"`
		}
		if entryJSON(envelope.Event, &event) != nil {
			return EntryEvent{}, errors.New("Feishu invitation payload is invalid")
		}
		sender, bot := event.Inviter.openID(), event.Bot.openID()
		if !r.allowedEntrySender(sender) || !scalar(event.Inviter.UserType, "1") || bot != r.Config.BotOpenID || !scalar(event.Bot.UserType, "10") {
			return EntryEvent{}, nil
		}
		result.MeetingNumber, result.MeetingID = entryNumber(event.Meeting.Number), entryLongID(event.Meeting.ID)
		if !entryMeetingNumber.MatchString(result.MeetingNumber) || result.MeetingID == "" || !entryIdentifier(event.CallID, 256) {
			return EntryEvent{}, errors.New("Feishu invitation has invalid meeting or call identifiers")
		}
		if len(event.InviteTime) != 0 {
			invited, err := entryTimestamp(event.InviteTime, true)
			if err != nil {
				return EntryEvent{}, errors.New("Feishu invitation timestamp is invalid")
			}
			if invited.Before(result.At) {
				result.At = invited
			}
		}
		result.Kind, result.SenderOpenID, result.CallID = EntryInvite, sender, event.CallID
	case "vc.bot.meeting_ended_v1":
		var event struct {
			Meeting entryMeeting `json:"meeting"`
		}
		if entryJSON(envelope.Event, &event) != nil || entryLongID(event.Meeting.ID) == "" {
			return EntryEvent{}, errors.New("Feishu meeting-ended payload has no valid meeting ID")
		}
		result.Kind, result.MeetingID = EntryEnd, entryLongID(event.Meeting.ID)
	case "im.message.receive_v1":
		var event struct {
			Sender struct {
				ID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
				Type   string `json:"sender_type"`
				Tenant string `json:"tenant_key"`
			} `json:"sender"`
			Message struct {
				ID       string `json:"message_id"`
				ChatType string `json:"chat_type"`
				Type     string `json:"message_type"`
				Content  string `json:"content"`
			} `json:"message"`
		}
		if entryJSON(envelope.Event, &event) != nil {
			return EntryEvent{}, errors.New("Feishu IM entry payload is invalid")
		}
		if event.Sender.Type != "user" || !r.allowedEntrySender(event.Sender.ID.OpenID) || event.Message.ChatType != "p2p" || event.Sender.Tenant != "" && event.Sender.Tenant != r.Config.TenantID {
			return EntryEvent{}, nil
		}
		if !entryIdentifier(event.Message.ID, 256) {
			return EntryEvent{}, errors.New("Feishu IM entry has no valid message ID")
		}
		number, err := entryMessageNumber(event.Message.Type, event.Message.Content)
		if err != nil {
			return EntryEvent{}, err
		}
		if number == "" {
			return EntryEvent{}, nil
		}
		result.Kind, result.MeetingNumber, result.SenderOpenID = EntryMessage, number, event.Sender.ID.OpenID
	default:
		return EntryEvent{}, nil
	}
	return result, nil
}

func (r EntryReceiver) allowedEntrySender(id string) bool {
	if !openID.MatchString(id) || id == r.Config.BotOpenID {
		return false
	}
	for _, allowed := range r.Config.AllowedSenderOpenIDs {
		if id == allowed {
			return true
		}
	}
	return false
}

func (a entryActor) openID() string {
	id := a.OpenID
	if len(a.ID) != 0 && string(a.ID) != "null" {
		var value string
		if json.Unmarshal(a.ID, &value) != nil {
			var object struct {
				OpenID string `json:"open_id"`
			}
			if json.Unmarshal(a.ID, &object) != nil {
				return ""
			}
			value = object.OpenID
		}
		if id != "" && value != "" && id != value {
			return ""
		}
		if value != "" {
			id = value
		}
	}
	if !openID.MatchString(id) {
		return ""
	}
	return id
}

func entryIdentifier(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return r <= ' ' || r == 127 }) < 0
}

func entryNumber(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Decimal integers stay text throughout; float64 would truncate long IDs.
	s = string(raw)
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return ""
	}
	return s
}

func entryLongID(raw json.RawMessage) string {
	value := entryNumber(raw)
	if !meetingID.MatchString(value) {
		return ""
	}
	return value
}

func entryTimestamp(raw json.RawMessage, seconds bool) (time.Time, error) {
	value := entryNumber(raw)
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, errors.New("invalid source time")
	}
	if seconds {
		if n > 253402300799 {
			return time.Time{}, errors.New("invalid source time")
		}
		return time.Unix(n, 0).UTC(), nil
	}
	if n > 253402300799999 {
		return time.Time{}, errors.New("invalid source time")
	}
	return time.UnixMilli(n).UTC(), nil
}

var (
	entryMeetingNumber = regexp.MustCompile(`^[0-9]{9}$`)
	entryNumberText    = regexp.MustCompile(`^(?:[0-9]{9}|[0-9]{3}[ \t]+[0-9]{3}[ \t]+[0-9]{3})$`)
	entryJoinPrefix    = regexp.MustCompile(`(?i)^(?:/join|join(?:[ \t]+meeting)?|入会|加入会议|参加会议|会议号|会议[ \t]*ID)[ \t:：]+`)
)

func entryMessageNumber(kind, content string) (string, error) {
	if kind != "text" && kind != "post" && kind != "video_chat" {
		return "", nil
	}
	if len(content) > 32*1024 || !utf8.ValidString(content) {
		return "", errors.New("Feishu entry message exceeds its content limit")
	}
	if kind == "video_chat" {
		// Verified from an im.message.receive_v1 platform receipt, 2026-10-12.
		// Topic and start_time are descriptive source fields, not routing input.
		var card struct {
			Topic     string          `json:"topic"`
			Number    string          `json:"meet_number"`
			StartTime string          `json:"start_time"`
			EndTime   json.RawMessage `json:"end_time"`
		}
		if entryJSON([]byte(content), &card) != nil {
			return "", errors.New("Feishu meeting card content is invalid")
		}
		decoder := json.NewDecoder(strings.NewReader(content))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&card) != nil || !entryMeetingNumber.MatchString(card.Number) || string(card.EndTime) != "null" && string(card.EndTime) != `""` {
			return "", nil
		}
		return card.Number, nil
	}
	var text string
	if kind == "text" {
		var body struct {
			Text string `json:"text"`
		}
		if entryJSON([]byte(content), &body) != nil {
			return "", errors.New("Feishu text entry content is invalid")
		}
		text = body.Text
	} else {
		var body struct {
			Title   string                               `json:"title"`
			Content [][]struct{ Tag, Text, Href string } `json:"content"`
		}
		if entryJSON([]byte(content), &body) != nil {
			return "", errors.New("Feishu post entry content is invalid")
		}
		var parts []string
		if body.Title != "" {
			parts = append(parts, body.Title)
		}
		for _, row := range body.Content {
			var line strings.Builder
			for _, node := range row {
				switch node.Tag {
				case "text":
					line.WriteString(node.Text)
				case "a":
					if entryURLNumber(node.Href) == "" {
						return "", nil
					}
					line.WriteString(node.Href)
				default:
					return "", nil
				}
			}
			parts = append(parts, line.String())
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	text = entryJoinPrefix.ReplaceAllString(text, "")
	fields := strings.Fields(text)
	compact := strings.Join(fields, "")
	// Meeting numbers copied from the client can contain non-breaking spaces.
	// Keep the entire-message and three-groups constraints when normalizing them.
	grouped := len(fields) == 3 && len(fields[0]) == 3 && len(fields[1]) == 3 && len(fields[2]) == 3 && entryMeetingNumber.MatchString(compact)
	if entryNumberText.MatchString(text) || grouped {
		return compact, nil
	}
	return entryURLNumber(text), nil
}

// Only this explicit URL shape is supported. Opaque join tokens, query values,
// redirectors, and embedded links in arbitrary prose are never guessed as IDs.
func entryURLNumber(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "vc.feishu.cn" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return ""
	}
	value := strings.TrimPrefix(u.Path, "/j/")
	if u.Path != "/j/"+value || !entryMeetingNumber.MatchString(value) {
		return ""
	}
	return value
}

// Reject duplicate keys, including differently cased aliases accepted by Go's
// struct decoder, before reading any authorization or routing field.
func entryJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("event nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			keys := map[string]bool{}
			for d.More() {
				if delimiter == '{' {
					key, err := d.Token()
					name, ok := key.(string)
					if err != nil || !ok || keys[strings.ToLower(name)] {
						return errors.New("duplicate or invalid event key")
					}
					keys[strings.ToLower(name)] = true
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("event must be an object")
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("event has trailing data")
	}
	return json.Unmarshal(raw, value)
}
