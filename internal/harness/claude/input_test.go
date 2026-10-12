//go:build unix

package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestClaudeTextInputRemainsExactWithoutStreamInputFlag(t *testing.T) {
	_, req := fixtureAdapter(t, "success")
	req.Prompt = "  Explain this text.\nKeep these line breaks.\n"
	input, err := encodeClaudeInput(req)
	if err != nil || string(input) != req.Prompt {
		t.Fatal("text-only input changed", string(input), err)
	}
	args, err := arguments(req)
	if err != nil || slices.Contains(args, "--input-format") {
		t.Fatal("text-only invocation acquired a new wire format", args, err)
	}
}

func TestClaudeImageInputUsesOrderedNativeBlocks(t *testing.T) {
	_, req := fixtureAdapter(t, "success")
	req.Prompt = "Compare these diagrams without changing their labels."
	for i, mime := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		image := harness.Image{MediaType: mime, Data: []byte{byte(i), 1, 2, 3, 4}}
		if i == 1 {
			image.Name = "sample\nlabel \"quoted\""
		}
		req.Images = append(req.Images, image)
	}
	input, err := encodeClaudeInput(req)
	if err != nil || !bytes.HasSuffix(input, []byte("\n")) || bytes.Count(input, []byte("\n")) != 1 {
		t.Fatal("multimodal input was not one NDJSON user envelope", err)
	}
	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "user" || envelope.Message.Role != "user" || envelope.Message.Content[0].Type != "text" || envelope.Message.Content[0].Text != req.Prompt || len(envelope.Message.Content) != 6 {
		t.Fatal("native input lost its user role, prompt or image blocks", envelope)
	}
	imageIndex := 0
	for _, block := range envelope.Message.Content[1:] {
		if block.Type == "text" {
			quoted, _ := json.Marshal(req.Images[1].Name)
			if block.Text != "Image name: "+string(quoted) {
				t.Fatal("image name changed the surrounding content structure", block.Text)
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(block.Source.Data)
		if err != nil || block.Type != "image" || block.Source.Type != "base64" || block.Source.MediaType != req.Images[imageIndex].MediaType || !bytes.Equal(data, req.Images[imageIndex].Data) {
			t.Fatal("native image did not retain its exact bytes and declared type", imageIndex, err)
		}
		imageIndex++
	}
	if imageIndex != len(req.Images) {
		t.Fatal("image input lost an attachment", imageIndex)
	}
	args, err := arguments(req)
	i := slices.Index(args, "--input-format")
	if err != nil || i < 0 || i+1 == len(args) || args[i+1] != "stream-json" {
		t.Fatal("multimodal invocation omitted the native input format", args, err)
	}
	metadata, err := json.Marshal(req.Images)
	if err != nil || bytes.Contains(metadata, []byte(`"Data"`)) || bytes.Contains(metadata, []byte(`"data"`)) {
		t.Fatal("image bytes entered serializable request metadata", err)
	}
}

func TestClaudeRejectsInvalidImagesBeforeStartingNativeProcess(t *testing.T) {
	image := harness.Image{Name: "diagram.png", MediaType: "image/png", Data: []byte("synthetic image content")}
	for _, tc := range []struct {
		name string
		edit func(*harness.Request)
	}{
		{"empty", func(r *harness.Request) { r.Images[0].Data = nil }},
		{"too large", func(r *harness.Request) { r.Images[0].Data = make([]byte, harness.MaxImageBytes+1) }},
		{"too many", func(r *harness.Request) { r.Images = slices.Repeat(r.Images, harness.MaxImages+1) }},
		{"unsupported type", func(r *harness.Request) { r.Images[0].MediaType = "image/svg+xml" }},
		{"missing type", func(r *harness.Request) { r.Images[0].MediaType = "" }},
		{"long name", func(r *harness.Request) { r.Images[0].Name = strings.Repeat("n", 513) }},
		{"invalid name", func(r *harness.Request) { r.Images[0].Name = string([]byte{0xff}) }},
		{"extraction", func(r *harness.Request) { r.Extraction, r.ToolsDisabled = true, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, req := fixtureAdapter(t, "success")
			capture := filepath.Join(t.TempDir(), "started.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capture)
			req.Images = []harness.Image{image}
			tc.edit(&req)
			if _, err := encodeClaudeInput(req); err == nil {
				t.Fatal("encoder accepted invalid image input")
			}
			calls := 0
			result, err := adapter.Run(context.Background(), req, func(harness.Observation) error { calls++; return nil })
			if err == nil || result.Status != "failed" || calls != 0 {
				t.Fatal("invalid image reached native execution", result, calls, err)
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("invalid image started a native process", err)
			}
		})
	}
	_, req := fixtureAdapter(t, "success")
	image.Data = make([]byte, harness.MaxImageBytes)
	req.Images = slices.Repeat([]harness.Image{image}, harness.MaxImages)
	if _, err := arguments(req); err != nil {
		t.Fatal("exact image count and byte bounds were rejected", err)
	}
}

func TestClaudePassesImagesToNativeStdinWithoutJournalingEchoedPayload(t *testing.T) {
	adapter, req := fixtureAdapter(t, "image_echo")
	capture := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capture)
	req.Images = []harness.Image{{Name: "diagram.png", MediaType: "image/png", Data: []byte("synthetic image payload for native input")}}
	encodedImage := base64.StdEncoding.EncodeToString(req.Images[0].Data)
	var observations []harness.Observation
	result, err := adapter.Run(context.Background(), req, func(o harness.Observation) error { observations = append(observations, o); return nil })
	if err != nil || result.Status != "completed" || result.Text != "hello" {
		t.Fatal("native image invocation failed", result, err)
	}
	var received struct {
		Args   []string `json:"args"`
		Prompt string   `json:"prompt"`
	}
	raw, err := os.ReadFile(capture)
	if err != nil || json.Unmarshal(raw, &received) != nil || !strings.Contains(received.Prompt, encodedImage) || !slices.Contains(received.Args, "--input-format") {
		t.Fatal("native process did not receive image bytes through stdin", err)
	}
	var echo json.RawMessage
	for _, observation := range observations {
		encoded, err := json.Marshal(observation)
		if err != nil || bytes.Contains(encoded, []byte(encodedImage)) || bytes.Contains(encoded, req.Images[0].Data) {
			t.Fatal("native image bytes entered journal observations", observation.Type, err)
		}
		if observation.Type == "user" {
			echo = observation.Raw
		}
	}
	if !bytes.Contains(echo, []byte(`"data_omitted":true`)) || !bytes.Contains(echo, []byte(`"data_sha256"`)) || !bytes.Contains(echo, []byte(`"media_type":"image/png"`)) {
		t.Fatal("image acknowledgment lost its attributed metadata", string(echo))
	}
}

func TestImageObservationRedactionCoversNestedToolResults(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("synthetic nested image"))
	result := json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}},{"type":"text","text":"Caption retained."}]`)
	raw := json.RawMessage(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"capture","content":` + string(result) + `}]}}`)
	tool := &harness.ToolObservation{ID: "capture", Name: "Read", Stage: "result", Result: result}
	original := harness.Observation{Type: "tool_result", Raw: raw, Tool: tool, Message: "capture " + string(result)}
	var received harness.Observation
	err := imageSafeEmitter(func(o harness.Observation) error { received = o; return nil })(original)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(received)
	if err != nil || bytes.Contains(encoded, []byte(data)) || !strings.Contains(received.Message, "Caption retained.") || !bytes.Contains(received.Raw, []byte(`"tool_use_id":"capture"`)) || !bytes.Contains(received.Tool.Result, []byte(`"data_omitted":true`)) {
		t.Fatal("nested image redaction lost tool evidence or retained binary data", err)
	}
	if !bytes.Contains(original.Raw, []byte(data)) || !bytes.Contains(original.Tool.Result, []byte(data)) {
		t.Fatal("redaction mutated its caller's native evidence")
	}
}
