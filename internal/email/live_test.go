package email

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// This opt-in probe reads one message; it never sends, changes flags or logs content.
func TestMailboxReadLive(t *testing.T) {
	path := os.Getenv("AGENT_HOST_EMAIL_LIVE_CONFIG")
	account := os.Getenv("AGENT_HOST_EMAIL_LIVE_ACCOUNT")
	if path == "" || account == "" {
		t.Skip("opt-in mailbox read acceptance")
	}
	_, fp, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	s := Service{Path: path, Fingerprint: fp}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	invoke := func(name string, args map[string]any) any {
		t.Helper()
		b, _ := json.Marshal(args)
		r, e := s.Call(ctx, name, b)
		if e != nil {
			t.Fatal(name, e)
		}
		return r
	}
	invoke("list_accounts", map[string]any{})
	invoke("check_account", map[string]any{"account_id": account})
	invoke("list_folders", map[string]any{"account_id": account})
	args := map[string]any{"account_id": account, "limit": 2}
	r := invoke("search_messages", args).(map[string]any)
	messages := r["messages"].([]map[string]any)
	if len(messages) == 0 {
		t.Fatal("no message in initial bounded window; read not verified")
	}
	first := messages[0]
	read := invoke("read_message", map[string]any{"account_id": account, "message_id": first["message_id"]}).(map[string]any)
	after := invoke("search_messages", args).(map[string]any)["messages"].([]map[string]any)
	matched := false
	for _, m := range after {
		if m["message_id"] == first["message_id"] {
			matched = true
			if !reflect.DeepEqual(first["flags"], m["flags"]) {
				t.Fatal("message flags changed during read")
			}
		}
	}
	if !matched {
		t.Fatal("message changed during read acceptance; flag preservation unverified")
	}
	t.Logf("authentication, folders, search and body read passed; body_bytes=%v; flags unchanged; sends=0", read["body_bytes"])
}
