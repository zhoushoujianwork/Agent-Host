package experience

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"agent-host/internal/state"
)

var credentialAssignment = regexp.MustCompile(`(?i)(\b(?:secret[_-]?(?:id|key)|api[_-]?key|access[_-]?(?:key|token)|password|authorization|token)\b["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)`)
var credentialFlag = regexp.MustCompile(`(?i)(--(?:secret-?(?:id|key)|api-key|access-token|password|token)\s+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s;|]+)`)
var bearerCredential = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`)

func redactText(text string) string {
	text = bearerCredential.ReplaceAllString(text, `${1}[redacted]`)
	text = credentialAssignment.ReplaceAllString(text, `${1}[redacted]`)
	return credentialFlag.ReplaceAllString(text, `${1}[redacted]`)
}

// LearningEvidence is a derived view; original journal observations stay intact.
// Known credential fields are removed before extraction and decision prompts.
func LearningEvidence(evidence []state.ToolEvidence) []state.ToolEvidence {
	out := append([]state.ToolEvidence(nil), evidence...)
	for i := range out {
		out[i].Input = redactJSON(out[i].Input)
		out[i].Result = redactJSON(out[i].Result)
	}
	return out
}

func redactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if !decodeLearningJSON(raw, &value) {
		return raw // Validation at the caller rejects malformed source JSON.
	}
	encoded, err := json.Marshal(redactValue(value, 0))
	if err != nil {
		return nil
	}
	return encoded
}

func decodeLearningJSON(raw []byte, value *any) bool {
	if !json.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value) == nil
}

func redactValue(value any, depth int) any {
	if depth > 16 {
		return "[redacted: nesting limit]"
	}
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			canonical := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			switch canonical {
			case "secretid", "secretkey", "apikey", "accesskey", "accesstoken", "password", "authorization", "token":
				v[key] = "[redacted]"
			default:
				v[key] = redactValue(item, depth+1)
			}
		}
	case []any:
		for i := range v {
			v[i] = redactValue(v[i], depth+1)
		}
	case string:
		var nested any
		if decodeLearningJSON([]byte(v), &nested) {
			if _, scalar := nested.(string); !scalar {
				encoded, err := json.Marshal(redactValue(nested, depth+1))
				if err == nil {
					return string(encoded)
				}
			}
		}
		return redactText(v)
	}
	return value
}
