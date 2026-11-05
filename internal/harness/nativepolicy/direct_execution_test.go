package nativepolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeSkillRootsRejectOffloadWithoutCLIAdmission(t *testing.T) {
	for _, directory := range []string{"offload", "renamed-worker"} {
		t.Run(directory, func(t *testing.T) {
			profile := t.TempDir()
			root := filepath.Join(profile, "skills", directory)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: offload\n---\nDelegate the task."), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := approvedSkillRoots(profile, ""); err == nil || !strings.Contains(err.Error(), "Offload delegation") {
				t.Fatal("direct harness request admitted recursive delegation", err)
			}
		})
	}
}
