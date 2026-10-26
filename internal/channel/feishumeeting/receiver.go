package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agent-host/internal/channel"
	"agent-host/internal/state"
	feishulib "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"
)

type Receiver struct {
	Config       Config
	ProgressPath string
	Client       Client
	// wait is replaced only by offline tests; configured production polls are >=5s.
	wait func(context.Context, time.Duration) error
}

type progress struct {
	Version int             `json:"version"`
	Scope   string          `json:"scope"`
	Cursor  string          `json:"cursor"`
	Seen    map[string]bool `json:"seen"`
}

type chatItem = feishulib.ChatItem

// Run polls only. handle must finish durable ingestion, never native execution.
// Each process start baselines currently available history, including downtime.
func (r Receiver) Run(ctx context.Context, handle func(context.Context, channel.Message) error, observe func(string, string) error) error {
	if err := r.Config.ValidateMeeting(); err != nil {
		return err
	}
	if !filepath.IsAbs(r.ProgressPath) || handle == nil || observe == nil {
		return errors.New("Feishu receiver requires an absolute progress path and handlers")
	}
	p, err := r.loadProgress()
	if err != nil {
		return err
	}
	wait := r.wait
	if wait == nil {
		wait = waitForPoll
	}
	client, err := meetingClient(r.Config, r.Client)
	if err != nil {
		return err
	}
	baseline := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := client.PollEvents(ctx, r.Config.MeetingID, p.Cursor)
		if err != nil {
			return err
		}
		for _, event := range current.Events {
			if event.Type != "chat_received" {
				continue
			}
			var payload struct {
				Items []json.RawMessage `json:"chat_received_items"`
			}
			if len(event.Payload) > 0 && json.Unmarshal(event.Payload, &payload) != nil {
				return errors.New("Feishu chat payload is malformed")
			}
			for index, raw := range payload.Items {
				key := r.Config.MeetingID + ":" + event.ID + ":" + strconv.Itoa(index)
				if p.Seen[key] {
					continue
				}
				if len(p.Seen) >= 100000 {
					return errors.New("Feishu meeting receipt limit reached; start a new bounded meeting runtime")
				}
				var item chatItem
				if json.Unmarshal(raw, &item) != nil {
					return errors.New("Feishu chat item is malformed")
				}
				if text, ok := r.question(item); ok && !baseline {
					message := channel.Message{ID: key, ConversationID: r.Config.MeetingID, ConversationType: "2", SenderID: item.Operator.ID, TenantID: r.Config.TenantID, MessageType: "text", Text: text, CreatedAt: sourceTime(item.SendTime), ReplyTarget: state.ChannelReplyTarget{Kind: "feishu-meeting", ID: r.Config.MeetingID}}
					ingestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					err := handle(ingestCtx, message)
					cancel()
					if err != nil {
						return errors.New("Feishu message could not be durably received; cursor was not advanced")
					}
				}
				p.Seen[key] = true
				if err := r.saveProgress(p); err != nil {
					return err
				}
			}
		}
		if current.Cursor != "" {
			p.Cursor = current.Cursor
		}
		if err := r.saveProgress(p); err != nil {
			return err
		}
		if baseline && !current.HasMore {
			baseline = false
			if err := observe("connected", "Feishu meeting text receiver ready; existing history was skipped"); err != nil {
				return errors.New("Feishu readiness observation failed")
			}
		}
		if err := wait(ctx, r.Config.PollInterval); err != nil {
			return err
		}
	}
}

func (r Receiver) question(item chatItem) (string, bool) {
	op := item.Operator
	if !scalar(item.MessageType, "1") || op.ID == r.Config.BotOpenID || scalar(op.UserType, "10") || op.ParticipantType == "bot" || op.ParticipantType == "app" || op.ParticipantType == "robot" {
		return "", false
	}
	if !scalar(op.UserType, "1") && op.ParticipantType != "human" && op.ParticipantType != "user" {
		return "", false
	}
	allowed := false
	for _, id := range r.Config.AllowedSenderOpenIDs {
		if op.ID == id {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	text := strings.TrimSpace(item.Content)
	if strings.HasPrefix(text, "AI:") {
		text = strings.TrimPrefix(text, "AI:")
	} else if strings.HasPrefix(text, "AI：") {
		text = strings.TrimPrefix(text, "AI：")
	} else {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != "" && len(text) <= 16*1024 && utf8.ValidString(text)
}

func scalar(raw json.RawMessage, value string) bool {
	return string(raw) == value || string(raw) == strconv.Quote(value)
}
func sourceTime(raw json.RawMessage) time.Time { return feishulib.SourceTime(raw) }

func waitForPoll(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r Receiver) loadProgress() (progress, error) {
	p := progress{Version: 1, Scope: r.Config.scope(), Seen: map[string]bool{}}
	info, err := os.Lstat(r.ProgressPath)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 32<<20 {
		return p, errors.New("Feishu progress file is not a bounded private regular file")
	}
	raw, err := os.ReadFile(r.ProgressPath)
	if err != nil {
		return p, errors.New("Feishu progress cannot be read")
	}
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 || p.Scope != r.Config.scope() || p.Seen == nil || len(p.Seen) > 100000 || len(p.Cursor) > 8192 {
		return p, errors.New("Feishu progress is invalid or belongs to another configured meeting identity")
	}
	return p, nil
}

func (r Receiver) saveProgress(p progress) error {
	directory := filepath.Dir(r.ProgressPath)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("Feishu progress directory cannot be created")
	}
	f, err := os.CreateTemp(directory, ".meeting-progress-*")
	if err != nil {
		return errors.New("Feishu progress cannot be prepared")
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(p)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), r.ProgressPath)
	}
	if err != nil {
		return errors.New("Feishu progress could not be committed")
	}
	dir, err := os.Open(directory)
	if err == nil {
		err = errors.Join(dir.Sync(), dir.Close())
	}
	if err != nil {
		return errors.New("Feishu progress directory commit failed")
	}
	return nil
}
