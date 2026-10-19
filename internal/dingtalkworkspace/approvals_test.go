package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This opt-in checks the selected real instance with the configured owner; it
// never writes a comment or submits an approval.
func TestApprovalDetailLive(t *testing.T) {
	path := os.Getenv("AGENT_HOST_APPROVAL_LIVE_CONFIG")
	if path == "" {
		t.Skip("requires explicit owner approval-read opt-in")
	}
	c, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("AGENT_HOST_APPROVAL_LIVE_INSTANCE")
	if id == "" {
		t.Fatal("explicit instance ID required")
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: Process(os.Getenv("AGENT_HOST_APPROVAL_LIVE_BINARY"))}
	raw, _ := json.Marshal(Args{Command: "oa approval detail", Parameters: map[string]string{"instance-id": id}})
	value, err := s.Call(t.Context(), "execute_command", raw)
	if err != nil {
		t.Fatal("real approval read failed", err)
	}
	body, _ := json.Marshal(value)
	var result struct {
		Success bool `json:"success"`
		Result  struct {
			ID     string `json:"processInstanceId"`
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &result) != nil || !result.Success || result.Result.ID != id || result.Result.Status == "" {
		t.Fatal("real approval read did not verify the selected instance")
	}
	t.Logf("Selected approval read through embedded owner MCP: status=%s; no writes", result.Result.Status)
}

// Exercise the existing MCP rather than introducing another approval API.
// The official pinned schemas are real; identity and business responses are fixtures.
func TestApprovalCommentAndPassUseOfficialMCPContracts(t *testing.T) {
	path, c, _ := configuration(t)
	c.Services = append(c.Services, "oa")
	data, _ := json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	official := Process(executable)
	contracts := map[string]json.RawMessage{}
	for _, command := range []string{"oa approval detail", "oa approval records", "oa approval oa-comments", "oa approval approve"} {
		raw, err := official(t.Context(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", command, "--compact", "--format", "json"}})
		if err != nil {
			t.Fatal(command, err)
		}
		contracts[command] = raw
	}
	writes := []string{}
	verificationFails := false
	run := func(_ context.Context, r Request) (json.RawMessage, error) {
		if r.Args[0] == "schema" {
			return contracts[r.Args[2]], nil
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		}
		command := strings.Join(r.Args[:3], " ")
		args := strings.Join(r.Args, " ")
		if !strings.Contains(args, "--instance-id=instance-1") || !strings.Contains(args, "--profile corp:owner") {
			t.Fatal("approval source/identity lost", args)
		}
		switch command {
		case "oa approval detail":
			if len(writes) == 2 {
				return json.RawMessage(`{"success":true,"result":{"processInstanceId":"instance-1","status":"COMPLETED","processInstanceResult":"agree"}}`), nil
			}
			// Earlier approval results do not establish closure of a running instance.
			return json.RawMessage(`{"success":true,"result":{"processInstanceId":"instance-1","status":"RUNNING","processInstanceResult":"agree","tasks":[{"taskId":731,"taskStatus":"RUNNING","taskResult":"NONE","userId":"owner"}]}}`), nil
		case "oa approval records":
			if verificationFails {
				return json.RawMessage(`{"success":false,"errorCode":"fixture-read-failure"}`), nil
			}
			return json.RawMessage(`{"success":true,"result":{"operationRecords":[{"operationType":"COMMENT","remark":"Verified VM upgrade","userId":"owner"}]}}`), nil
		case "oa approval oa-comments":
			if len(writes) != 0 || !strings.Contains(args, "--content=Verified VM upgrade") || !strings.Contains(args, "--yes") {
				t.Fatal("comment contract or close-out order lost", args)
			}
		case "oa approval approve":
			if len(writes) != 1 || !strings.Contains(args, "--task-id=731") || !strings.Contains(args, "--remark=Verified VM upgrade") || !strings.Contains(args, "--yes") {
				t.Fatal("approval contract or close-out order lost", args)
			}
		default:
			t.Fatal("unexpected business command", args)
		}
		writes = append(writes, command)
		return json.RawMessage(`{"success":true,"errcode":0}`), nil
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}
	call := func(command string, params map[string]string, operation string) (any, error) {
		raw, _ := json.Marshal(Args{Command: command, Parameters: params, OperationID: operation, Confirmed: true})
		return s.Call(t.Context(), "execute_command", raw)
	}
	before, err := call("oa approval detail", map[string]string{"instance-id": "instance-1"}, "")
	if err != nil || !strings.Contains(string(before.(json.RawMessage)), `"status":"RUNNING"`) || len(writes) != 0 {
		t.Fatal("interpretation read mutated approval", before, err)
	}
	comment := map[string]string{"instance-id": "instance-1", "content": "Verified VM upgrade"}
	value, err := call("oa approval oa-comments", comment, "completion-comment")
	if err != nil || value.(map[string]any)["business_acceptance_verified"] != false || len(writes) != 1 {
		t.Fatal("comment receipt claimed business verification", value, err)
	}
	verificationFails = true
	if _, err = call("oa approval records", map[string]string{"instance-id": "instance-1"}, ""); err == nil {
		t.Fatal("verification failure hidden")
	}
	if _, err = call("oa approval oa-comments", comment, "completion-comment"); err != nil || len(writes) != 1 {
		t.Fatal("verification failure caused duplicate comment", err)
	}
	verificationFails = false
	if _, err = call("oa approval records", map[string]string{"instance-id": "instance-1"}, ""); err != nil {
		t.Fatal(err)
	}
	approval := map[string]string{"instance-id": "instance-1", "task-id": "731", "remark": "Verified VM upgrade"}
	if _, err = call("oa approval approve", approval, "submit-approval"); err != nil || len(writes) != 2 {
		t.Fatal("approval submission failed", err)
	}
	if _, err = call("oa approval approve", approval, "submit-approval"); err != nil || len(writes) != 2 {
		t.Fatal("approval replayed", err)
	}
	after, err := call("oa approval detail", map[string]string{"instance-id": "instance-1"}, "")
	if err != nil || !strings.Contains(string(after.(json.RawMessage)), `"status":"COMPLETED"`) {
		t.Fatal("final workflow state not readable", after, err)
	}
	c.Services = []string{"chat"}
	data, _ = json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, s.Fingerprint, _ = Load(path)
	if _, err = call("oa approval approve", approval, "disabled-service"); err == nil || len(writes) != 2 {
		t.Fatal("disabled OA service allowed approval", err)
	}
}
