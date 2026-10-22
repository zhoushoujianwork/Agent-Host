package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func ticketService(t *testing.T, body *string) Service {
	t.Helper()
	path, c, _ := configuration(t)
	c.Services = append(c.Services, "oa")
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	contract, err := Process(executable)(t.Context(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", "oa approval detail", "--compact", "--format", "json"}})
	if err != nil {
		t.Fatal(err)
	}
	return Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: func(_ context.Context, r Request) (json.RawMessage, error) {
		if r.Args[0] == "schema" {
			return contract, nil
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		}
		command := strings.Join(r.Args, " ")
		if !strings.HasPrefix(command, "oa approval detail ") || !strings.Contains(command, "--instance-id=selected") || strings.Contains(command, "--yes") || !strings.Contains(command, "--profile corp:owner") {
			t.Fatal("ticket read changed target or wrote", command)
		}
		return json.RawMessage(*body), nil
	}}
}

func readTicketCall(t *testing.T, s Service, a Args) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(a)
	v, err := s.Call(t.Context(), "read_ticket", raw)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func TestReadTicketPreservesFormCommentsAndCurrentOwnerTask(t *testing.T) {
	body := `{"success":true,"result":{"processInstanceId":"selected","status":"RUNNING","formValueVOS":[{"name":"whitelist","value":"old /25"}],"operationRecords":[{"type":"ADD_REMARK","remark":"Use two /32 addresses","date":1792648679255,"userId":"submitter"}],"tasks":[{"taskId":12,"taskStatus":"COMPLETED","userId":"owner"},{"taskId":13,"taskStatus":"RUNNING","userId":"owner"},{"taskId":14,"taskStatus":"RUNNING","userId":"someone-else"}]}}`
	s := ticketService(t, &body)
	v, err := readTicketCall(t, s, Args{InstanceID: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(v)
	if !strings.Contains(string(encoded), "old /25") || !strings.Contains(string(encoded), "Use two /32 addresses") || len(v["comments"].([]map[string]json.RawMessage)) != 1 || len(v["current_tasks"].([]map[string]json.RawMessage)) != 1 {
		t.Fatal(v)
	}
	if string(v["current_tasks"].([]map[string]json.RawMessage)[0]["taskId"]) != "13" {
		t.Fatal("stale task selected")
	}
	for _, invalid := range []string{`{"success":true,"result":{"processInstanceId":"other"}}`, `{"success":false,"result":{"processInstanceId":"selected"}}`} {
		body = invalid
		if _, err := readTicketCall(t, s, Args{InstanceID: "selected"}); err == nil {
			t.Fatal("accepted wrong or failed detail")
		}
	}
	body = `{"success":true,"result":{"processInstanceId":"selected"}}`
	v, err = readTicketCall(t, s, Args{InstanceID: "selected"})
	if err != nil || v["comments"] != nil || v["current_tasks"] != nil {
		t.Fatal("missing arrays claimed empty", v, err)
	}
}

func TestReadTicketLargeDetailPagesAndRejectsChangedContinuation(t *testing.T) {
	body := `{"success":true,"result":{"processInstanceId":"selected","formValueVOS":[{"value":"` + strings.Repeat("保留", 10000) + `"}],"operationRecords":[],"tasks":[]}}`
	s := ticketService(t, &body)
	args := Args{InstanceID: "selected", Limit: 2000}
	v, err := readTicketCall(t, s, args)
	if err != nil || v["partial"] != true {
		t.Fatal(v, err)
	}
	digest := v["content_sha256"].(string)
	var content strings.Builder
	for {
		content.WriteString(v["content"].(string))
		next, ok := v["next_offset"].(int)
		if !ok {
			break
		}
		args.Offset, args.ExpectedDigest = next, digest
		v, err = readTicketCall(t, s, args)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !json.Valid([]byte(content.String())) || strings.Count(content.String(), "保留") != 10000 {
		t.Fatal("large detail was truncated or corrupted")
	}
	body = strings.Replace(body, "保留", "更新", 1)
	if _, err := readTicketCall(t, s, args); err == nil {
		t.Fatal("changed detail accepted stale offset")
	}
}
