package dingtalk

import (
	"encoding/json"
	"net/url"
	"strings"
)

// MessageQuote is provider-supplied reference content, not a statement by the
// current sender. Unknown or unavailable bodies stay empty; no history lookup
// or attachment download is inferred from a quote.
type MessageQuote struct {
	MessageID   string `json:"message_id,omitempty"`
	MessageType string `json:"message_type,omitempty"`
	Body        string `json:"body,omitempty"`
	Title       string `json:"title,omitempty"`
	SummaryOnly bool   `json:"summary_only,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type quotedCallback struct {
	MsgID       string          `json:"msgId"`
	MsgType     string          `json:"msgType"`
	Content     json.RawMessage `json:"content"`
	Title       json.RawMessage `json:"title"`
	Text        json.RawMessage `json:"text"`
	CardContent json.RawMessage `json:"cardContent"`
	ActionURL   json.RawMessage `json:"biz_custom_action_url"`
}

const maxQuoteRunes = 4000
const maxQuoteTitleRunes = 256

// Markdown forwards use text.content in current callbacks, with content.text
// in older shapes. Retain the supplied body as reference material; linked
// images are not download capabilities and are never fetched here.
func parseForwardedMarkdown(text string, raw json.RawMessage) (string, *MessageQuote, bool) {
	var title string
	if strings.TrimSpace(text) == "" {
		content, ok := contentObject(raw)
		if !ok {
			return "", nil, false
		}
		for key := range content {
			if key != "text" && key != "title" {
				return "", nil, false
			}
		}
		text, ok = contentString(content["text"])
		if !ok {
			return "", nil, false
		}
		if value, exists := content["title"]; exists {
			title, ok = contentString(value)
			if !ok {
				return "", nil, false
			}
		}
	}
	body, _ := json.Marshal(text)
	var cleaned string
	_ = json.Unmarshal(redactQuotedCapabilities(body), &cleaned)
	quote := &MessageQuote{MessageType: "markdown"}
	quote.Body, quote.Truncated = boundedQuote(cleaned, maxQuoteRunes)
	titleJSON, _ := json.Marshal(title)
	_ = json.Unmarshal(redactQuotedCapabilities(titleJSON), &title)
	var truncated bool
	quote.Title, truncated = boundedQuote(title, maxQuoteTitleRunes)
	quote.Truncated = quote.Truncated || truncated
	return text, quote, true
}

// This adapts the older DingTalk application channel's callback quote shapes.
func parseQuote(originalID string, isReply bool, quoted *quotedCallback) *MessageQuote {
	if !isReply && originalID == "" {
		return nil
	}
	out := &MessageQuote{}
	out.MessageID, out.Truncated = boundedQuote(originalID, maxIDBytes)
	if quoted == nil {
		return out
	}
	if quoted.MsgID != "" {
		out.MessageID, out.Truncated = boundedQuote(quoted.MsgID, maxIDBytes)
	}
	var truncated bool
	out.MessageType, truncated = boundedQuote(quoted.MsgType, 128)
	out.Truncated = out.Truncated || truncated
	content := quoted.Content
	var encoded string
	if json.Unmarshal(content, &encoded) == nil && strings.HasPrefix(strings.TrimSpace(encoded), "{") && json.Valid([]byte(encoded)) {
		var object map[string]json.RawMessage
		_ = json.Unmarshal([]byte(encoded), &object)
		// Literal JSON in a text message stays literal unless it has a known
		// provider shape. Redaction below also covers JSON within that literal.
		if strings.EqualFold(quoted.MsgType, "text") && (object["text"] != nil || object["content"] != nil) ||
			strings.EqualFold(quoted.MsgType, "chatRecord") && (object["title"] != nil || object["summary"] != nil) ||
			strings.EqualFold(quoted.MsgType, "interactiveCard") || strings.EqualFold(quoted.MsgType, "markdown") || strings.EqualFold(quoted.MsgType, "richText") {
			content = json.RawMessage(encoded)
		}
	}
	content = redactQuotedCapabilities(content)
	switch strings.ToLower(quoted.MsgType) {
	case "text":
		var object struct {
			Text    string `json:"text"`
			Content string `json:"content"`
		}
		if json.Unmarshal(content, &object) == nil {
			out.Body = object.Text
			if out.Body == "" {
				out.Body = object.Content
			}
		} else {
			_ = json.Unmarshal(content, &out.Body)
		}
	case "markdown":
		var object struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		if json.Unmarshal(content, &object) == nil {
			out.Title, out.Body = object.Title, object.Text
		}
	case "richtext":
		var object struct {
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"richText"`
		}
		if json.Unmarshal(content, &object) == nil {
			var parts []string
			for _, part := range object.Parts {
				if part.Type == "" || strings.EqualFold(part.Type, "text") {
					parts = append(parts, part.Text)
				}
			}
			out.Body = strings.Join(parts, "")
		}
	case "interactivecard":
		out.SummaryOnly = true
		out.Body, out.Title, truncated = parseCardQuote(content, quoted)
		out.Truncated = out.Truncated || truncated
	case "chatrecord":
		out.SummaryOnly = true
		var object struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal(content, &object) == nil {
			out.Title, out.Body = object.Title, object.Summary
		}
	}
	out.Body, truncated = boundedQuote(out.Body, maxQuoteRunes)
	out.Truncated = out.Truncated || truncated
	out.Title, truncated = boundedQuote(out.Title, maxQuoteTitleRunes)
	out.Truncated = out.Truncated || truncated
	return out
}

// Card callbacks carry a visible preview, not a guaranteed full ticket body.
// Extract documented text nodes only; metadata and media capabilities are not text.
func parseCardQuote(content json.RawMessage, quoted *quotedCallback) (string, string, bool) {
	var object struct {
		Title       string          `json:"title"`
		Text        string          `json:"text"`
		CardContent json.RawMessage `json:"cardContent"`
		ActionURL   string          `json:"biz_custom_action_url"`
	}
	_ = json.Unmarshal(content, &object)
	if len(object.CardContent) == 0 {
		object.CardContent = redactQuotedCapabilities(quoted.CardContent)
	}
	if object.Title == "" {
		object.Title = cleanQuoteField(quoted.Title)
	}
	if object.Text == "" {
		object.Text = cleanQuoteField(quoted.Text)
	}
	if object.ActionURL == "" {
		object.ActionURL, _ = contentString(quoted.ActionURL)
	}
	var tree any
	_ = json.Unmarshal(object.CardContent, &tree)
	parts, characters, nodes := []string{}, 0, 0
	truncated := false
	appendText := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		remaining := maxQuoteRunes - characters
		if len(parts) > 0 {
			remaining--
		}
		if remaining <= 0 {
			truncated = true
			return
		}
		runes := []rune(text)
		if len(runes) > remaining {
			runes = runes[:remaining]
			truncated = true
		}
		if len(parts) > 0 {
			characters++
		}
		parts = append(parts, string(runes))
		characters += len(runes)
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > 16 || nodes >= 1024 {
			truncated = true
			return
		}
		nodes++
		switch item := value.(type) {
		case []any:
			for index, child := range item {
				walk(child, depth+1)
				if nodes >= 1024 {
					truncated = truncated || index+1 < len(item)
					break
				}
			}
		case string:
			truncated = truncated || item == "[nested content omitted]"
		case map[string]any:
			kind, _ := item["elementType"].(string)
			if strings.EqualFold(kind, "TEXT") {
				text, _ := item["value"].(string)
				if text == "[nested content omitted]" {
					truncated = true
				} else {
					appendText(text)
				}
			}
			if children, ok := item["children"]; ok {
				walk(children, depth+1)
			}
		}
	}
	walk(tree, 0)
	if len(parts) == 0 {
		appendText(object.Text)
	}
	if u, err := url.Parse(object.ActionURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil {
		appendText(object.ActionURL)
	}
	return strings.Join(parts, "\n"), object.Title, truncated
}

func cleanQuoteField(raw json.RawMessage) string {
	var cleaned string
	_ = json.Unmarshal(redactQuotedCapabilities(raw), &cleaned)
	return cleaned
}

func boundedQuote(text string, limit int) (string, bool) {
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]) + "…", true
}

// Only normalized text fields survive parsing. Walk embedded provider JSON as
// well so a stringified media object cannot carry a capability into evidence.
func redactQuotedCapabilities(raw json.RawMessage) json.RawMessage {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil
	}
	cleaned, _ := json.Marshal(redactQuoteValue(value, 0))
	return cleaned
}

func redactQuoteValue(value any, depth int) any {
	if depth > 16 {
		return "[nested content omitted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(key) {
			case "sessionwebhook", "sessionwebhookexpiredtime", "robotcode", "chatbotuserid", "downloadcode", "picturedownloadcode", "downloadurl", "mediaid", "fileid", "url":
				typed[key] = "[redacted]"
			default:
				typed[key] = redactQuoteValue(item, depth+1)
			}
		}
	case []any:
		for i, item := range typed {
			typed[i] = redactQuoteValue(item, depth+1)
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var nested any
			decoder := json.NewDecoder(strings.NewReader(trimmed))
			decoder.UseNumber()
			if json.Valid([]byte(trimmed)) && decoder.Decode(&nested) == nil {
				original, _ := json.Marshal(nested)
				encoded, _ := json.Marshal(redactQuoteValue(nested, depth+1))
				if string(original) == string(encoded) {
					return typed
				}
				return string(encoded)
			}
		}
	}
	return value
}
