package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const maxFrameBytes = 256 * 1024

// object checks framing and duplicate aliases before exact-key field access.
// It permits new platform fields without Go's case-insensitive struct aliases.
func object(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > maxFrameBytes || !utf8.Valid(raw) || len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("invalid Feishu JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("Feishu JSON nesting limit exceeded")
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
						return errors.New("duplicate or invalid Feishu JSON key")
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
	if err := walk(0); err != nil {
		return nil, errors.New("invalid Feishu JSON structure")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("Feishu JSON contains trailing data")
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, errors.New("invalid Feishu JSON object")
	}
	return value, nil
}

func stringField(fields map[string]json.RawMessage, key string) (string, error) {
	raw, present := fields[key]
	if !present {
		return "", nil
	}
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", errors.New("invalid Feishu string field")
	}
	return s, nil
}
