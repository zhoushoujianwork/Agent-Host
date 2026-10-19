package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

type MediaDownloader interface {
	Download(context.Context, dingtalk.Attachment) ([]byte, string, error)
}

func inputMaterial(m dingtalk.Message) (*state.ChannelInput, []string) {
	if len(m.Attachments) == 0 && m.Quote == nil {
		return nil, nil
	}
	input := &state.ChannelInput{}
	var codes []string
	for _, a := range m.Attachments {
		input.Attachments = append(input.Attachments, state.ChannelAttachment{Kind: a.Kind, Name: a.Name, MediaType: a.MediaType, Size: a.Size})
		codes = append(codes, a.DownloadCode)
	}
	if q := m.Quote; q != nil {
		input.Quote = &state.ChannelQuote{MessageID: q.MessageID, MessageType: q.MessageType, Body: q.Body, Title: q.Title, SummaryOnly: q.SummaryOnly, Truncated: q.Truncated}
	}
	return input, codes
}

// Download only after claiming the durable request. A failed or interrupted
// transfer never starts a native task, and quoted media cannot supply a code.
func (s *Service) prepareInput(ctx context.Context, m state.ChannelMessage) (state.ChannelMessage, error) {
	if m.Input == nil || len(m.Input.Attachments) == 0 {
		return m, nil
	}
	if s.Downloader == nil || !filepath.IsAbs(s.MediaRoot) {
		return m, errors.New("channel attachment download is not configured")
	}
	codes, err := s.Store.ChannelInputCapabilities(ctx, m.ID)
	if err != nil {
		return m, err
	}
	if len(codes) != len(m.Input.Attachments) {
		return m, errors.New("attachment receipt is incomplete")
	}
	if err := os.MkdirAll(s.MediaRoot, 0700); err != nil {
		return m, errors.New("cannot create private attachment directory")
	}
	info, err := os.Lstat(s.MediaRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return m, errors.New("attachment directory must be private and not a symlink")
	}
	dir, err := os.MkdirTemp(s.MediaRoot, "input-")
	if err != nil {
		return m, errors.New("cannot prepare attachment directory")
	}
	saved := false
	defer func() {
		if !saved {
			_ = os.RemoveAll(dir)
		}
	}()
	attachments := append([]state.ChannelAttachment(nil), m.Input.Attachments...)
	for i, a := range attachments {
		data, mime, err := s.Downloader.Download(ctx, dingtalk.Attachment{Kind: a.Kind, Name: a.Name, MediaType: a.MediaType, Size: a.Size, DownloadCode: codes[i]})
		if err != nil {
			return m, errors.New("attachment transfer failed")
		}
		limit := dingtalk.MaxFileBytes
		if a.Kind == "image" {
			limit = dingtalk.MaxPictureBytes
		}
		if len(data) == 0 || int64(len(data)) > int64(limit) || mime == "" {
			return m, errors.New("attachment content is empty or exceeds its limit")
		}
		ext := strings.ToLower(filepath.Ext(a.Name))
		if len(ext) < 2 || len(ext) > 12 || strings.IndexFunc(ext[1:], func(r rune) bool { return r < 'a' || r > 'z' }) >= 0 {
			ext = ".dat"
		}
		path := filepath.Join(dir, fmt.Sprintf("asset-%02d%s", i+1, ext))
		if err := os.WriteFile(path, data, 0600); err != nil {
			return m, errors.New("cannot preserve downloaded attachment")
		}
		digest := sha256.Sum256(data)
		attachments[i].Path, attachments[i].SHA256 = path, hex.EncodeToString(digest[:])
		attachments[i].ContentType, attachments[i].Bytes = mime, int64(len(data))
	}
	if err := s.Store.SaveChannelAttachments(ctx, m.ID, attachments); err != nil {
		return m, err
	}
	saved = true
	return s.Store.ChannelMessage(ctx, m.ID)
}

func supportedInput(m state.ChannelMessage) bool {
	switch m.MessageType {
	case "text":
		return strings.TrimSpace(m.Text) != ""
	case "richText":
		return strings.TrimSpace(m.Text) != "" || m.Input != nil && len(m.Input.Attachments) > 0
	case "picture", "file":
		return m.Input != nil && len(m.Input.Attachments) > 0
	case "markdown":
		return strings.TrimSpace(m.Text) != "" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "markdown" && strings.TrimSpace(m.Input.Quote.Body) != ""
	case "interactiveCard":
		return strings.TrimSpace(m.Text) != "" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "interactiveCard" &&
			(strings.TrimSpace(m.Input.Quote.Body) != "" || strings.TrimSpace(m.Input.Quote.Title) != "")
	default:
		return false
	}
}
