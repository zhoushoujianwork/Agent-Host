package dingtalkworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// readTicket uses the pinned detail contract, whose operationRecords retain
// comment text (the separate records endpoint can omit remarks). No list or
// task mutation is used to resolve an already selected instance.
func (s Service) readTicket(ctx context.Context, c Config, a Args) (any, error) {
	if a.InstanceID == "" || len(a.InstanceID) > 256 || strings.ContainsAny(a.InstanceID, "\x00\r\n\t ") || a.Offset < 0 || a.Limit < 0 || a.Limit > 8000 {
		return nil, errors.New("read_ticket requires an exact instance_id, nonnegative offset and limit at most 8000")
	}
	parts, err := commandPath(c, "oa approval detail")
	if err != nil {
		return nil, err
	}
	r := Request{Config: s.Path, Fingerprint: s.Fingerprint}
	schema, err := describe(ctx, s.Run, r, "oa approval detail")
	if err != nil {
		return nil, err
	}
	if schema["effect"] != "read" {
		return nil, errors.New("ticket detail contract is not read-only")
	}
	args, err := commandArguments(parts, map[string]string{"instance-id": a.InstanceID}, schema)
	if err != nil {
		return nil, err
	}
	if err := verifyIdentity(ctx, s.Run, r, c); err != nil {
		return nil, err
	}
	r.Args = append(args, "--profile", c.Profile, "--format", "json")
	body, err := s.Run(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("read selected ticket: %w", err)
	}
	if len(body) > 4<<20 {
		return nil, errors.New("ticket detail exceeds the 4 MiB read limit")
	}
	var envelope struct {
		Success bool                       `json:"success"`
		Result  map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &envelope) != nil || !envelope.Success || envelope.Result == nil {
		return nil, errors.New("ticket detail did not return a successful result")
	}
	var id string
	if json.Unmarshal(envelope.Result["processInstanceId"], &id) != nil || id != a.InstanceID {
		return nil, errors.New("ticket detail returned a different or missing instance identity")
	}
	var records, tasks []map[string]json.RawMessage
	recordsKnown := json.Unmarshal(envelope.Result["operationRecords"], &records) == nil && records != nil
	tasksKnown := json.Unmarshal(envelope.Result["tasks"], &tasks) == nil && tasks != nil
	comments, pending := []map[string]json.RawMessage{}, []map[string]json.RawMessage{}
	for _, record := range records {
		var kind string
		_ = json.Unmarshal(record["type"], &kind)
		if kind == "ADD_REMARK" {
			comments = append(comments, record)
		}
	}
	_, owner, _ := strings.Cut(c.Profile, ":")
	for _, task := range tasks {
		var status, user string
		_ = json.Unmarshal(task["taskStatus"], &status)
		_ = json.Unmarshal(task["userId"], &user)
		if status == "RUNNING" && user == owner {
			pending = append(pending, task)
		}
	}
	result := map[string]any{"instance_id": id, "profile": c.Profile, "detail": envelope.Result,
		"comments": comments, "current_tasks": pending,
		"coverage": map[string]any{"comments_available": recordsKnown, "tasks_available": tasksKnown, "source": "oa approval detail", "attachments_downloaded": false}}
	if !recordsKnown {
		result["comments"] = nil
	}
	if !tasksKnown {
		result["current_tasks"] = nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if a.Offset > 0 && a.ExpectedDigest == "" || a.ExpectedDigest != "" && a.ExpectedDigest != digest {
		return nil, errors.New("ticket changed or continuation digest missing; read again from offset zero")
	}
	if len(encoded) <= 10000 && a.Offset == 0 {
		result["content_sha256"], result["partial"] = digest, false
		return result, nil
	}
	text := []rune(string(encoded))
	if a.Offset >= len(text) {
		return nil, errors.New("ticket offset is outside the returned document")
	}
	limit := a.Limit
	if limit == 0 {
		limit = 4000
	}
	end := min(len(text), a.Offset+limit)
	page := map[string]any{"instance_id": id, "format": "json", "content": string(text[a.Offset:end]), "offset": a.Offset, "total_characters": len(text), "content_sha256": digest, "partial": end < len(text)}
	if end < len(text) {
		page["next_offset"] = end
	}
	return page, nil
}
