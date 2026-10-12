package assembly

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"agent-host/internal/state"
)

func TestAssembleProjectsSelectsPrimaryAndFingerprintsCompleteSet(t *testing.T) {
	canonical := func() string {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	neutral, a, b := canonical(), canonical(), canonical()
	projects := []state.ProjectRef{{ID: "b", Name: "B", Workspace: b, Scope: "project:b"}, {ID: "a", Name: "A", Workspace: a, Scope: "project:a"}}
	c := Configuration{NativeProfile: filepath.Join(neutral, "native"), Model: "sonnet"}
	joint, err := AssembleProjects(projects, "", "default", neutral, c)
	if err != nil || joint.Workspace != neutral || joint.Scope != "default" || joint.ProjectID != "" || len(joint.Projects) != 2 || joint.Projects[0].ID != "a" || len(joint.AdditionalDirectories) != 2 {
		t.Fatalf("neutral multi-project runtime: %+v %v", joint, err)
	}
	reordered, err := AssembleProjects([]state.ProjectRef{projects[1], projects[0], projects[0]}, "", "default", neutral, c)
	if err != nil || reordered.Fingerprint != joint.Fingerprint {
		t.Fatalf("set order changed identity: %+v %v", reordered, err)
	}
	primary, err := AssembleProjects(projects, "a", "default", neutral, c)
	if err != nil || primary.Workspace != a || primary.Scope != "project:a" || primary.ProjectID != "a" || !slices.Equal(primary.AdditionalDirectories, []string{b}) || primary.Fingerprint == joint.Fingerprint {
		t.Fatalf("primary selection: %+v %v", primary, err)
	}
	single, err := AssembleProjects(projects[1:], "", "default", neutral, c)
	if err != nil || single.ProjectID != "a" || single.Workspace != a || len(single.AdditionalDirectories) != 0 || single.Fingerprint == primary.Fingerprint {
		t.Fatalf("single selection: %+v %v", single, err)
	}
	changed := append([]state.ProjectRef(nil), projects...)
	changed[0].Scope = "project:b-v2"
	other, err := AssembleProjects(changed, "a", "default", neutral, c)
	if err != nil || other.Fingerprint == primary.Fingerprint {
		t.Fatalf("scope absent from fingerprint: %+v %v", other, err)
	}
	changed[0] = projects[0]
	changed[0].Workspace = canonical()
	other, err = AssembleProjects(changed, "a", "default", neutral, c)
	if err != nil || other.Fingerprint == primary.Fingerprint {
		t.Fatalf("additional cwd absent from fingerprint: %+v %v", other, err)
	}
	changed[0] = projects[0]
	changed[0].Name = "Renamed B"
	other, err = AssembleProjects(changed, "a", "default", neutral, c)
	if err != nil || other.Fingerprint != primary.Fingerprint {
		t.Fatalf("display name rotated session: %+v %v", other, err)
	}
	if _, err := AssembleProjects(projects, "missing", "default", neutral, c); err == nil {
		t.Fatal("unknown primary accepted")
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleProjects(projects, "a", "default", neutral, c); err == nil {
		t.Fatal("missing secondary directory accepted")
	}
}

func TestAssembleProjectsDeduplicatesAccessWithoutMergingScopes(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := AssembleProjects([]state.ProjectRef{{ID: "a", Scope: "scope:a", Workspace: dir}, {ID: "b", Scope: "scope:b", Workspace: dir}}, "a", "default", dir, Configuration{NativeProfile: filepath.Join(dir, "profile")})
	if err != nil || len(r.Projects) != 2 || len(r.AdditionalDirectories) != 0 {
		t.Fatalf("scope identity conflated with path: %+v %v", r, err)
	}
}
