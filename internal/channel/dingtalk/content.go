package dingtalk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

const MaxAttachments = 4
const maxRichTextParts = 128

type richTextMessage struct {
	Text        string
	Attachments []Attachment
	IsReply     bool
	Replied     *quotedCallback
}

// DWS quote replies put reply metadata beside the richText array. Separate
// that wrapper before strict part parsing; quoted bytes are never sender text
// or media download capabilities. Unknown/malformed fields reject the whole
// message rather than admitting an executable text prefix.
func parseRichTextMessage(raw json.RawMessage) (richTextMessage, bool) {
	var out richTextMessage
	content, ok := contentObject(raw)
	if !ok {
		return out, false
	}
	for key := range content {
		if key != "richText" && key != "isReplyMsg" && key != "repliedMsg" {
			return out, false
		}
	}
	if flag, exists := content["isReplyMsg"]; exists {
		switch string(bytes.TrimSpace(flag)) {
		case "true":
			out.IsReply = true
		case "false":
		default:
			return out, false
		}
	}
	if quoted, exists := content["repliedMsg"]; exists {
		if !out.IsReply {
			return richTextMessage{}, false
		}
		if _, ok := contentObject(quoted); !ok || json.Unmarshal(quoted, &out.Replied) != nil || out.Replied == nil {
			return richTextMessage{}, false
		}
	}
	parts, err := json.Marshal(map[string]json.RawMessage{"richText": content["richText"]})
	if err != nil {
		return richTextMessage{}, false
	}
	var supported bool
	out.Text, out.Attachments, supported = parseRichText(parts)
	if !supported {
		return richTextMessage{}, false
	}
	return out, true
}

// Attachment describes inbound media. DownloadCode is an ephemeral capability:
// persist it only in protected capability storage and never send it to the model
// or logs. Resolve it after claiming the durable receipt.
type Attachment struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	MediaType    string `json:"media_type"`
	Size         int64  `json:"size,omitempty"`
	DownloadCode string `json:"-"`
}

func (a Attachment) String() string {
	return fmt.Sprintf("Attachment{Kind:%q Name:%q MediaType:%q Size:%d}", a.Kind, a.Name, a.MediaType, a.Size)
}

func (a Attachment) GoString() string { return a.String() }

// Parse all parts or none. Unsupported parts must not leave an executable
// command prefix behind. Duplicate keys are rejected rather than overwritten.
func parseRichText(raw json.RawMessage) (string, []Attachment, bool) {
	content, ok := contentObject(raw)
	if !ok || len(content) != 1 {
		return "", nil, false
	}
	var parts []json.RawMessage
	if json.Unmarshal(content["richText"], &parts) != nil || len(parts) == 0 || len(parts) > maxRichTextParts {
		return "", nil, false
	}
	var text strings.Builder
	var attachments []Attachment
	for _, rawPart := range parts {
		part, ok := contentObject(rawPart)
		if !ok {
			return "", nil, false
		}
		if _, typed := part["type"]; typed {
			kind, ok := contentString(part["type"])
			if !ok || kind != "picture" && kind != "file" {
				return "", nil, false
			}
			delete(part, "type")
			piece, attachment, ok := mediaPart(kind, part, len(attachments)+1)
			if !ok || len(attachments) == MaxAttachments {
				return "", nil, false
			}
			text.WriteString(piece)
			attachments = append(attachments, attachment)
			continue
		}
		piece, ok := contentString(part["text"])
		if !ok || len(part) != 1 {
			return "", nil, false
		}
		text.WriteString(piece)
	}
	return text.String(), attachments, true
}

func parseStandaloneMedia(kind string, raw json.RawMessage) (string, []Attachment, bool) {
	part, ok := contentObject(raw)
	if !ok {
		return "", nil, false
	}
	piece, attachment, ok := mediaPart(kind, part, 1)
	if !ok {
		return "", nil, false
	}
	return piece, []Attachment{attachment}, true
}

func mediaPart(kind string, part map[string]json.RawMessage, ordinal int) (string, Attachment, bool) {
	var a Attachment
	for key := range part {
		switch key {
		case "downloadCode", "pictureDownloadCode", "fileName", "fileSize", "fileType":
		default:
			return "", a, false
		}
	}
	for _, key := range []string{"downloadCode", "pictureDownloadCode"} {
		if value, exists := part[key]; exists {
			code, ok := contentString(value)
			if !ok || !validDownloadCode(code) {
				return "", Attachment{}, false
			}
			// DingTalk supplies distinct codes for the same picture. The
			// download API uses downloadCode; older callbacks may supply only
			// pictureDownloadCode. These are alternatives, not duplicate IDs.
			if a.DownloadCode == "" {
				a.DownloadCode = code
			}
		}
	}
	if a.DownloadCode == "" {
		return "", Attachment{}, false
	}
	if rawSize, exists := part["fileSize"]; exists {
		if json.Unmarshal(rawSize, &a.Size) != nil || a.Size < 0 || a.Size > MaxFileBytes {
			return "", Attachment{}, false
		}
	}
	if rawType, exists := part["fileType"]; exists {
		if _, ok := contentString(rawType); !ok {
			return "", Attachment{}, false
		}
	}
	if kind == "picture" {
		if a.Size > MaxPictureBytes {
			return "", Attachment{}, false
		}
		a.Kind, a.Name, a.MediaType = "image", "图片 "+strconv.Itoa(ordinal), "image/*"
	} else {
		name, ok := contentString(part["fileName"])
		if !ok || !validAttachmentName(name) {
			return "", Attachment{}, false
		}
		a.Kind, a.Name, a.MediaType = "file", name, "application/octet-stream"
	}
	return "[" + a.Name + "]", a, true
}

func validAttachmentName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 255 && !strings.ContainsAny(name, "/\\") &&
		name != "." && name != ".." && strings.IndexFunc(name, unicode.IsControl) < 0
}

func validDownloadCode(code string) bool {
	return strings.TrimSpace(code) != "" && len(code) <= 4096 && strings.IndexFunc(code, unicode.IsControl) < 0
}

func contentString(raw json.RawMessage) (string, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func contentObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}
