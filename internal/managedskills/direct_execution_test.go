package managedskills

import "testing"

func TestReadyEntriesRejectsOffloadDelegationBeforeRendering(t *testing.T) {
	for _, entry := range []Entry{
		{Name: "offload", Kind: "authored", Files: map[string]string{"SKILL.md": "instructions"}},
		{Name: "renamed", Kind: "authored", Files: map[string]string{"SKILL.md": "---\nname: offload\n---\nDelegate"}},
	} {
		if _, _, err := ReadyEntries([]Entry{entry}); err == nil {
			t.Fatal("Offload Skill reached the generated task package")
		}
	}
}
