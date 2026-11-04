//go:build unix

package nativepolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func privateMountEnvironment(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTaskSkillMountReusesDigestPathAcrossRunPackages(t *testing.T) {
	first, second := nativeSkillFixture(t), nativeSkillFixture(t)
	if first.Path == second.Path || first.Digest != second.Digest {
		t.Fatalf("fixture does not represent equal content at distinct run paths: %+v %+v", first, second)
	}
	profile := privateMountEnvironment(t)
	environment, _, err := executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "stable-conversation"})
	if err != nil {
		t.Fatal(err)
	}
	firstMount, err := mountTaskSkillPackage(environment.Root, first.Path, first.Digest)
	if err != nil {
		t.Fatal(err)
	}
	resumed, _, err := executionEnvironment(harness.Request{NativeProfile: profile, HostSessionID: "stable-conversation", Resume: true})
	if err != nil || resumed.Root != environment.Root {
		t.Fatalf("conversation environment did not resume: %+v %v", resumed, err)
	}
	secondMount, err := mountTaskSkillPackage(resumed.Root, second.Path, second.Digest)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(environment.Root, "native-skills", first.Digest)
	if firstMount != expected || secondMount != expected || firstMount == first.Path || secondMount == second.Path {
		t.Fatalf("task package path was not stable: first=%s second=%s expected=%s", firstMount, secondMount, expected)
	}
	firstRoots, err := approvedSkillRoots("", firstMount)
	if err != nil {
		t.Fatal(err)
	}
	secondRoots, err := approvedSkillRoots("", secondMount)
	if err != nil || strings.Join(firstRoots, "\x00") != strings.Join(secondRoots, "\x00") {
		t.Fatalf("equal package content produced different exact roots: %v %v %v", firstRoots, secondRoots, err)
	}
	if _, err := validateSkillPlugin(firstMount, first.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("source package was removed: %v", err)
	}
	changed := managedNativeFixture(t)
	changedMount, err := mountTaskSkillPackage(environment.Root, changed.Path, changed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == first.Digest || changedMount == firstMount || changedMount != filepath.Join(environment.Root, "native-skills", changed.Digest) {
		t.Fatalf("changed digest did not receive a distinct controlled path: %s %s", firstMount, changedMount)
	}
}

func TestTaskSkillMountRevalidatesSourceAndNeverRepairsControlledCopy(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		first, second := nativeSkillFixture(t), nativeSkillFixture(t)
		environment := privateMountEnvironment(t)
		if _, err := mountTaskSkillPackage(environment, first.Path, first.Digest); err != nil {
			t.Fatal(err)
		}
		skill := filepath.Join(second.Path, "skills", second.Manifest.Sources[0].Name, "SKILL.md")
		if err := os.WriteFile(skill, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := mountTaskSkillPackage(environment, second.Path, second.Digest); err == nil {
			t.Fatal("mutated original package reused an existing controlled copy")
		}
	})
	for _, mutation := range []string{"body", "extra", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			p := nativeSkillFixture(t)
			environment := privateMountEnvironment(t)
			mounted, err := mountTaskSkillPackage(environment, p.Path, p.Digest)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(mounted, "residue")
			switch mutation {
			case "body":
				marker = filepath.Join(mounted, "skills", p.Manifest.Sources[0].Name, "SKILL.md")
				err = os.WriteFile(marker, []byte("changed"), 0600)
			case "extra":
				err = os.WriteFile(marker, []byte("extra"), 0600)
			case "symlink":
				err = os.Symlink("manifest.json", marker)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mountTaskSkillPackage(environment, p.Path, p.Digest); err == nil {
				t.Fatalf("invalid controlled copy %s was accepted", mutation)
			}
			if _, err := os.Lstat(marker); err != nil {
				t.Fatalf("invalid controlled copy %s was repaired or removed: %v", mutation, err)
			}
		})
	}
}

func TestTaskSkillMountRequiresPrivateCanonicalEnvironment(t *testing.T) {
	p := nativeSkillFixture(t)
	environment := privateMountEnvironment(t)
	if err := os.Chmod(environment, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := mountTaskSkillPackage(environment, p.Path, p.Digest); err == nil {
		t.Fatal("nonprivate environment accepted a controlled Skill package")
	}
}
