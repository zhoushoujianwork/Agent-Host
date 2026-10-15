package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserIdentityReceiptsSurviveEncryptedRecovery(t *testing.T) {
	root, policy := backupFixture(t)
	dir := filepath.Join(root, "users", "alice", "accounts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"user_id":"alice","account":{"channel":"dingtalk","sender_id":"staff-alice"}}`)
	if err := os.WriteFile(filepath.Join(dir, "account.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".account-pending"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	point, err := Create(t.Context(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(root), "restored")
	if _, err := Restore(t.Context(), filepath.Join(policy.Destination, point.Archive), policy.IdentityFile, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "users", "alice", "accounts", "account.json"))
	if err != nil || string(got) != string(body) {
		t.Fatal(string(got), err)
	}
	if _, err := os.Stat(filepath.Join(target, "users", "alice", "accounts", ".account-pending")); !os.IsNotExist(err) {
		t.Fatal("unfinished receipt entered recovery", err)
	}
}
