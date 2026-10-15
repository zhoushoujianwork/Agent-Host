package state

import "testing"

func TestPrivateUserAudienceRecognition(t *testing.T) {
	for _, audience := range []string{"group-123", "", "user:", "user:owner", "user:../../owner", "user:alice\n", "user:alice/bob"} {
		if PrivateUserID(audience) != "" {
			t.Fatal("non-private audience admitted", audience)
		}
	}
	for audience, id := range map[string]string{"owner": "owner", "user:alice": "alice", "user:u-123": "u-123"} {
		if got := PrivateUserID(audience); got != id {
			t.Fatal(audience, got)
		}
	}
}
