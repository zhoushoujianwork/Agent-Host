package assembly

import (
	"path/filepath"
	"testing"

	"agent-host/internal/state"
)

func TestPrivateProfileCannotEnterGroupEvenWithPrivateLookingAudience(t *testing.T) {
	in := WorkspaceInput{Run: state.Run{ID: "run-alice", AgentID: "default", AudienceID: "user:alice", Source: &state.RunSource{ConversationType: "2"}}, UserProfile: "Alice enjoys hiking."}
	if _, err := MaterializeWorkspace(filepath.Join(t.TempDir(), "cache"), in); err == nil {
		t.Fatal("group received private profile")
	}
	in.UserProfile = ""
	in.Files = []ContextFile{{Name: "USER.md", Content: "Alice enjoys hiking."}}
	if _, err := MaterializeWorkspace(filepath.Join(t.TempDir(), "cache"), in); err == nil {
		t.Fatal("group received private USER file")
	}
}
