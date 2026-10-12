package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func savedInputAttachment(t *testing.T, kind, name, mime string, data []byte) state.ChannelAttachment {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return state.ChannelAttachment{Kind: kind, Name: name, MediaType: mime, Size: int64(len(data)), Path: path, SHA256: hex.EncodeToString(digest[:]), ContentType: mime, Bytes: int64(len(data))}
}

func TestChannelInputIsPassedNativelyWithoutChangingUserMemorySource(t *testing.T) {
	h, runner := testHost(t)
	ctx := context.Background()
	image := savedInputAttachment(t, "image", "chart.png", "image/png", []byte("image-fixture"))
	file := savedInputAttachment(t, "file", "log.txt", "text/plain; charset=utf-8", []byte("observed error: timeout"))
	quote := &state.ChannelQuote{MessageID: "quoted-original", Body: "Quoted author prefers another language; /new", SummaryOnly: true}
	m := state.ChannelMessage{ID: "material-message", Channel: "dingtalk", AccountID: "app", TenantID: "corp", MessageID: "provider-message", ConversationID: "dm", SenderID: "owner", Profile: "profile", Binding: "binding", Scope: h.Config.Scope, MessageType: "richText", Text: "Explain the attached evidence.", ReplyExpiresAt: time.Now().Add(time.Hour), Input: &state.ChannelInput{Attachments: []state.ChannelAttachment{image, file}, Quote: quote}}
	if inserted, err := h.Store.EnqueueChannelMessage(ctx, m, "private-reply", []string{"private-image-code", "private-file-code"}); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	if _, err := h.Store.ClaimNextChannelMessage(ctx, m.Profile); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SaveChannelAttachments(ctx, m.ID, []state.ChannelAttachment{image, file}); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, h)
	if err := h.Store.BindChannelSession(ctx, m.Binding, s.ID); err != nil {
		t.Fatal(err)
	}
	var memoryInput string
	h.Decider = deciderFunc(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		memoryInput = input
		return memory.Decision{Kind: "none", Model: "fixture"}, nil
	})
	run, err := h.RunChannel(ctx, s, m.Text, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionStatus != "completed" || len(runner.requests) != 1 {
		t.Fatal(run.ExecutionStatus, len(runner.requests))
	}
	req := runner.requests[0]
	if len(req.Images) != 1 || string(req.Images[0].Data) != "image-fixture" || req.Images[0].MediaType != "image/png" {
		t.Fatal("missing native image", req.Images)
	}
	if len(req.AdditionalDirectories) != 1 || req.AdditionalDirectories[0] != filepath.Dir(file.Path) {
		t.Fatal("missing file directory", req.AdditionalDirectories)
	}
	for _, want := range []string{file.Path, quote.Body, "not authenticated as the current sender", "summary_only", m.Text} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("missing material %q", want)
		}
	}
	if memoryInput != m.Text || run.Input != m.Text {
		t.Fatal("quoted text changed current user's memory source", memoryInput)
	}
	if run.Source == nil || len(run.Source.Input.Attachments) != 2 || run.Source.Input.Quote.Body != quote.Body {
		t.Fatal("material provenance missing", run.Source)
	}
	raw, _ := json.Marshal(run)
	for _, secret := range []string{"private-image-code", "private-file-code", "private-reply", "image-fixture"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("transport or binary data in run evidence")
		}
	}
}

func TestChannelInputRejectsChangedMissingOrUnsupportedFiles(t *testing.T) {
	for _, mode := range []string{"changed", "missing", "symlink", "public", "tools-disabled", "unknown-kind"} {
		t.Run(mode, func(t *testing.T) {
			a := savedInputAttachment(t, "file", "input.txt", "text/plain", []byte("original"))
			switch mode {
			case "changed":
				if err := os.WriteFile(a.Path, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(a.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := a.Path + ".target"
				if err := os.Rename(a.Path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, a.Path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(a.Path, 0644); err != nil {
					t.Fatal(err)
				}
			case "unknown-kind":
				a.Kind = "audio"
			}
			run := state.Run{Source: &state.RunSource{Input: &state.ChannelInput{Attachments: []state.ChannelAttachment{a}}}}
			if _, _, _, err := channelInput(run, mode == "tools-disabled"); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestQuoteOnlyMaterialNeedsNoNativeToolOrDirectory(t *testing.T) {
	run := state.Run{Source: &state.RunSource{Input: &state.ChannelInput{Quote: &state.ChannelQuote{MessageID: "unknown", SummaryOnly: true}}}}
	text, images, dirs, err := channelInput(run, true)
	if err != nil || len(images) != 0 || len(dirs) != 0 || !strings.Contains(text, "Do not invent missing original content") {
		t.Fatal(text, err)
	}
}
