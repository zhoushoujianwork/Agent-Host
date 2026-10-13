package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func skillFixture() (state.Memory, SkillIdentity) {
	return state.Memory{ID: "experience-1", Scope: "project:ops", Kind: "procedure", Text: "Diagnose the observed connection failure.", Status: "active", Revision: 2, SourceType: "reviewed_experience", SourceRunID: "run-1", Conditions: "When the stream connection reports an expired token.", Steps: []string{"Inspect the observed connection status.", "Renew the token and verify a new connection."}, Avoid: []string{"Do not replay old tasks."}, EvidenceIDs: []string{"tool-1"}}, SkillIdentity{SourceIdentity: "channel:dingtalk", AgentID: state.DefaultAgentID, AudienceID: state.OwnerAudienceID, Scopes: []string{"project:ops"}}
}

func canonicalSkillTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeSkillSnapshotIsDeterministicScopedAndImmutable(t *testing.T) {
	memory, identity := skillFixture()
	root := canonicalSkillTemp(t)
	now := time.Now()
	package1, err := BuildSkillPackage(root, "run-one", []state.Memory{memory}, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	package2, err := BuildSkillPackage(root, "run-two", []state.Memory{memory}, identity, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SkillSetDigest([]state.Memory{memory}, identity, now)
	if err != nil || package1.Digest != package2.Digest || digest != package1.Digest {
		t.Fatalf("digest=%s package1=%+v package2=%+v err=%v", digest, package1, package2, err)
	}
	if strings.Contains(package1.Path, "config") || !strings.Contains(package1.Path, "workspaces/run-one/native-skills") {
		t.Fatal(package1.Path)
	}
	name := package1.Manifest.Sources[0].Name
	path := filepath.Join(package1.Path, "skills", name, "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"name: procedure-", "host-memory-revision: \"2\"", memory.Conditions, memory.Steps[1], memory.Avoid[0], memory.EvidenceIDs[0], "not independently verified truth"} {
		if !strings.Contains(string(body), expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
	if strings.Contains(string(body), "allowed-tools:") {
		t.Fatal("generated Skill grants tools")
	}
	if _, err := BuildSkillPackage(root, "run-one", []state.Memory{memory}, identity, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSkillPackage(root, "run-one", []state.Memory{memory}, identity, now); err == nil {
		t.Fatal("mutated cache accepted")
	}
	memory.Revision++
	changed, err := SkillSetDigest([]state.Memory{memory}, identity, now)
	if err != nil || changed == digest {
		t.Fatalf("revision did not change digest: %s %v", changed, err)
	}
}

func TestNativeSkillEligibilityAndPathBoundaries(t *testing.T) {
	memory, identity := skillFixture()
	now := time.Now()
	for _, status := range []string{"invalidated", "conflicted", "quarantined"} {
		copy := memory
		copy.Status = status
		digest, err := SkillSetDigest([]state.Memory{copy}, identity, now)
		if err != nil || digest != "" {
			t.Fatalf("status=%s digest=%q err=%v", status, digest, err)
		}
	}
	copy := memory
	copy.SourceType = "user_statement"
	digest, err := SkillSetDigest([]state.Memory{copy}, identity, now)
	if err != nil || digest != "" {
		t.Fatalf("fact qualified: %q %v", digest, err)
	}
	copy = memory
	copy.ExpiresAt = &now
	digest, err = SkillSetDigest([]state.Memory{copy}, identity, now)
	if err != nil || digest != "" {
		t.Fatalf("expired qualified: %q %v", digest, err)
	}
	for _, change := range []func(*state.Memory){func(m *state.Memory) { m.Scope = "project:other" }, func(m *state.Memory) { m.AgentID = "ops" }, func(m *state.Memory) { m.AudienceID = "group:other" }, func(m *state.Memory) { m.Conditions = "" }, func(m *state.Memory) { m.EvidenceIDs = nil }, func(m *state.Memory) { m.Steps = []string{"!`echo secret`"} }} {
		copy := memory
		change(&copy)
		if _, err := SkillSetDigest([]state.Memory{copy}, identity, now); err == nil {
			t.Fatalf("invalid memory qualified: %+v", copy)
		}
	}
	root := canonicalSkillTemp(t)
	if _, err := BuildSkillPackage(root, "../escape", []state.Memory{memory}, identity, now); err == nil {
		t.Fatal("unsafe context ID accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSkillPackage(link, "run-one", []state.Memory{memory}, identity, now); err == nil {
		t.Fatal("symlink cache accepted")
	}
}
