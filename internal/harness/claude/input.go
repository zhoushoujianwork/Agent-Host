package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"agent-host/internal/harness"
)

func validateImages(req harness.Request) error {
	if len(req.Images) > harness.MaxImages {
		return fmt.Errorf("Claude input supports at most %d images", harness.MaxImages)
	}
	if req.Extraction && len(req.Images) > 0 {
		return errors.New("Claude extraction cannot contain images")
	}
	for i, image := range req.Images {
		if len(image.Data) == 0 || len(image.Data) > harness.MaxImageBytes {
			return fmt.Errorf("Claude image %d must contain between 1 byte and 5 MiB", i+1)
		}
		switch image.MediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return fmt.Errorf("Claude image %d has an unsupported media type", i+1)
		}
		if !utf8.ValidString(image.Name) || len(image.Name) > 512 {
			return fmt.Errorf("Claude image %d name must be valid UTF-8 and at most 512 bytes", i+1)
		}
	}
	return nil
}

// Claude's wire format stays inside the adapter. Text-only input is unchanged;
// images remain native image blocks instead of paths or prompt-embedded base64.
func encodeClaudeInput(req harness.Request) ([]byte, error) {
	if err := validateImages(req); err != nil {
		return nil, err
	}
	if len(req.Images) == 0 {
		return []byte(req.Prompt), nil
	}
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	content := []block{{Type: "text", Text: req.Prompt}}
	for _, image := range req.Images {
		if image.Name != "" {
			// A filename is user content, never an extra system instruction.
			name, _ := json.Marshal(image.Name)
			content = append(content, block{Type: "text", Text: "Image name: " + string(name)})
		}
		content = append(content, block{Type: "image", Source: &source{Type: "base64", MediaType: image.MediaType, Data: base64.StdEncoding.EncodeToString(image.Data)}})
	}
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// Native verbose output can echo image-bearing user envelopes or tool results.
// Keep their structure and digest as evidence without journaling the payload.
func imageSafeEmitter(emit func(harness.Observation) error) func(harness.Observation) error {
	if emit == nil {
		return nil
	}
	return func(o harness.Observation) error {
		o.Raw = omitImageData(o.Raw)
		if o.Tool != nil {
			tool := *o.Tool
			tool.Input, tool.Result = omitImageData(tool.Input), omitImageData(tool.Result)
			changed := !bytes.Equal(tool.Input, o.Tool.Input) || !bytes.Equal(tool.Result, o.Tool.Result)
			o.Tool = &tool
			if changed && tool.Stage == "result" {
				o.Message = tool.ID + " " + string(tool.Result)
			} else if changed {
				o.Message = tool.Name + " " + tool.ID + " " + string(tool.Input)
			}
		}
		return emit(o)
	}
}

func omitImageData(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !bytes.Contains(raw, []byte(`"base64"`)) {
		return raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return raw
	}
	changed := false
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			if value["type"] == "image" {
				if source, ok := value["source"].(map[string]any); ok && source["type"] == "base64" {
					if data, ok := source["data"].(string); ok {
						if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
							digest := sha256.Sum256(decoded)
							source["data_sha256"], source["data_bytes"] = hex.EncodeToString(digest[:]), len(decoded)
						}
						delete(source, "data")
						source["data_omitted"] = true
						changed = true
					}
				}
			}
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(value)
	if !changed {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}
