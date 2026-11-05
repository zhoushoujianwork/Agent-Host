package dream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

func structuredRefs(data map[string][]byte) (sourceRefs, error) {
	runs, sessions := map[string]bool{}, map[string]bool{}
	if raw := data["run.json"]; raw != nil {
		var run map[string]any
		if err := decodeOne(raw, &run); err != nil {
			return sourceRefs{}, fmt.Errorf("invalid run.json: %w", err)
		}
		addString(runs, run["id"])
		addString(runs, run["run_id"])
		addString(sessions, run["session_id"])
	}
	if raw := data["result.json"]; raw != nil {
		var result map[string]any
		if err := decodeOne(raw, &result); err != nil {
			return sourceRefs{}, fmt.Errorf("invalid result.json: %w", err)
		}
		if ref, ok := result["ref"].(map[string]any); ok {
			addString(runs, ref["run_id"])
			addString(sessions, ref["session_id"])
		}
	}
	if raw := data["events.jsonl"]; raw != nil {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 64<<10), maxFileBytes)
		for scanner.Scan() {
			if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
				continue
			}
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) != nil || event == nil {
				return sourceRefs{}, errors.New("invalid events.jsonl")
			}
			addString(runs, event["run_id"])
			addString(sessions, event["session_id"])
		}
		if err := scanner.Err(); err != nil {
			return sourceRefs{}, fmt.Errorf("read events.jsonl: %w", err)
		}
	}
	if len(runs) > 1 || len(sessions) > 1 {
		return sourceRefs{}, errors.New("structured artifacts contain conflicting run or session references")
	}
	return sourceRefs{run: onlyKey(runs), session: onlyKey(sessions)}, nil
}

func addString(values map[string]bool, value any) {
	if text, ok := value.(string); ok && text != "" {
		values[text] = true
	}
}

func onlyKey(values map[string]bool) string {
	for value := range values {
		return value
	}
	return ""
}
