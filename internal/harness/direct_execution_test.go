package harness

import "testing"

func TestDirectExecutionRejectsOffloadSkillIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		blocked    bool
	}{
		{"offload", "plain fixture", true},
		{"OFFLOAD", "plain fixture", true},
		{"renamed-worker", "---\nname: 'offload'\ndescription: delegate\n---\nbody", true},
		{"renamed-worker", "---\r\nname: offload\r\n---\r\nbody", true},
		{"jms-ssh", "---\nname: jms-ssh\n---\nDo not call offload.", false},
		{"procedure-fixture", "ordinary business workflow", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDirectExecutionSkill(tc.name, []byte(tc.body))
			if (err != nil) != tc.blocked {
				t.Fatal(tc.name, err)
			}
		})
	}
}
