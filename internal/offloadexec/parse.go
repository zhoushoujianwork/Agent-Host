package offloadexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const maxJSONDepth = 64

// record holds the fields the client relies on. Unknown fields are ignored so
// Offload can add fields without breaking the client.
type record struct {
	Status   *string         `json:"status"`
	ExitCode *int            `json:"exit_code"`
	Profile  *string         `json:"profile"`
	Session  *string         `json:"session"`
	Model    string          `json:"model"`
	Backend  string          `json:"backend"`
	Result   string          `json:"result"`
	Usage    json.RawMessage `json:"usage"`
}

// parseRecord validates that data is exactly one JSON object without duplicate
// keys and decodes the fields the client uses.
func parseRecord(data []byte) (record, error) {
	var rec record
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return rec, errors.New("empty output")
	}
	if data[0] != '{' {
		return rec, errors.New("output is not a JSON object")
	}
	if !json.Valid(data) {
		return rec, errors.New("malformed JSON or multiple records")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicates(dec, 0); err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, errors.New("record has fields of the wrong type")
	}
	if string(rec.Usage) == "null" {
		rec.Usage = nil
	}
	return rec, nil
}

// rejectDuplicates walks one JSON value and fails on repeated object keys,
// compared case-insensitively because encoding/json matches keys that way.
func rejectDuplicates(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return errors.New("malformed JSON")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return errors.New("malformed JSON")
			}
			key, _ := kt.(string)
			folded := strings.ToLower(key)
			if seen[folded] {
				return errors.New("duplicate JSON key")
			}
			seen[folded] = true
			if err := rejectDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := rejectDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("malformed JSON")
	}
	return nil
}
