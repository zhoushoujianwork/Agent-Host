package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func projectEvidenceRun(t *testing.T) Run {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := Run{Scope: "neutral", Workspace: filepath.Join(root, "neutral")}
	for _, id := range []string{"alpha", "beta"} {
		run.Projects = append(run.Projects, ProjectRef{ID: id, Scope: "project:" + id, Workspace: filepath.Join(root, id)})
	}
	for _, path := range []string{run.Workspace, run.Projects[0].Workspace, run.Projects[1].Workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func projectEvidenceDraft(evidence []ToolEvidence) ExperienceDraft {
	draft := ExperienceDraft{Title: "Observed project operation", Summary: "Observed typed tool inputs.", Conditions: "Selected project files only.", Steps: []string{"Inspect the recorded file."}}
	for _, item := range evidence {
		draft.EvidenceIDs = append(draft.EvidenceIDs, item.ID)
	}
	return draft
}

func projectEvidenceInput(t *testing.T, id string, fields map[string]string) ToolEvidence {
	t.Helper()
	input, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return ToolEvidence{ID: id, Name: "Read", Input: input}
}

func TestEvidenceEntitiesForRunUsesLongestSelectedProject(t *testing.T) {
	run := projectEvidenceRun(t)
	nested := filepath.Join(run.Projects[0].Workspace, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	run.Projects = append(run.Projects, ProjectRef{ID: "nested", Scope: "historical-memory-scope", Workspace: nested})
	evidence := []ToolEvidence{
		projectEvidenceInput(t, "nested", map[string]string{"file_path": filepath.Join(nested, "new.go")}),
		projectEvidenceInput(t, "beta", map[string]string{"filePath": filepath.Join(run.Projects[1].Workspace, "test.go")}),
		projectEvidenceInput(t, "unselected", map[string]string{"path": filepath.Join(run.Projects[0].Workspace, "ignored.go")}),
	}
	draft := projectEvidenceDraft(evidence[:2])
	got, err := EvidenceEntitiesForRun(run, draft, evidence)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file:" + filepath.Join(nested, "new.go"), "file:" + filepath.Join(run.Projects[1].Workspace, "test.go"), "project:nested", "project:beta"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entities %v want %v", got, want)
	}
}

func TestEvidenceEntitiesForRunResolvesRelativePathsWithoutGuessing(t *testing.T) {
	run := projectEvidenceRun(t)
	alpha, beta := run.Projects[0].Workspace, run.Projects[1].Workspace
	for _, tc := range []struct {
		name, workspace string
		input           map[string]string
		file, project   string
	}{
		{"primary cwd", alpha, map[string]string{"file_path": "src/file.go"}, filepath.Join(alpha, "src/file.go"), "alpha"},
		{"neutral cwd", run.Workspace, map[string]string{"file_path": "file.go"}, "", ""},
		{"typed cwd", run.Workspace, map[string]string{"path": "file.go", "cwd": beta}, filepath.Join(beta, "file.go"), "beta"},
		{"typed workdir", alpha, map[string]string{"path": "file.go", "workdir": beta}, filepath.Join(beta, "file.go"), "beta"},
		{"external cwd", alpha, map[string]string{"path": "file.go", "cwd": filepath.Dir(alpha)}, "", ""},
		{"relative cwd", alpha, map[string]string{"path": "file.go", "cwd": "../beta"}, "", ""},
		{"conflicting cwd", alpha, map[string]string{"path": "file.go", "cwd": alpha, "workdir": beta}, "", ""},
		{"outside selected roots", alpha, map[string]string{"path": "../../outside.go"}, "", ""},
		{"uncertain shell cwd", alpha, map[string]string{"path": "file.go", "command": "cd ../beta; cat file.go"}, "", ""},
		{"shell text only", alpha, map[string]string{"command": "cat " + filepath.Join(beta, "file.go")}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := run
			current.Workspace = tc.workspace
			evidence := []ToolEvidence{projectEvidenceInput(t, "read", tc.input)}
			before := append([]byte(nil), evidence[0].Input...)
			got, err := EvidenceEntitiesForRun(current, projectEvidenceDraft(evidence), evidence)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{}
			if tc.file != "" {
				want = []string{"file:" + tc.file, "project:" + tc.project}
			}
			if !reflect.DeepEqual(got, want) || string(before) != string(evidence[0].Input) {
				t.Fatalf("entities %v want %v; raw evidence must remain unchanged", got, want)
			}
		})
	}
}

func TestEvidenceEntitiesForRunPreservesSymlinkBoundaries(t *testing.T) {
	run := projectEvidenceRun(t)
	alpha, beta := run.Projects[0].Workspace, run.Projects[1].Workspace
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside": outside, "broken": filepath.Join(outside, "absent"), "other-project": beta} {
		if err := os.Symlink(target, filepath.Join(alpha, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(outside, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "existing.go"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "nested"), filepath.Join(alpha, "traversal")); err != nil {
		t.Fatal(err)
	}
	run.Workspace = alpha
	evidence := []ToolEvidence{
		projectEvidenceInput(t, "outside", map[string]string{"path": filepath.Join(alpha, "outside/new.go")}),
		projectEvidenceInput(t, "broken", map[string]string{"path": filepath.Join(alpha, "broken/new.go")}),
		projectEvidenceInput(t, "beta", map[string]string{"path": filepath.Join(alpha, "other-project/new.go")}),
		projectEvidenceInput(t, "existing traversal", map[string]string{"path": "traversal/../existing.go"}),
		projectEvidenceInput(t, "missing traversal", map[string]string{"path": "traversal/../missing.go"}),
	}
	got, err := EvidenceEntitiesForRun(run, projectEvidenceDraft(evidence), evidence)
	want := []string{"file:" + filepath.Join(beta, "new.go"), "project:beta"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("entities %v want %v: %v", got, want, err)
	}
}

func TestEvidenceEntitiesForRunRetainsLegacyBehaviorAndDraftValidation(t *testing.T) {
	run := projectEvidenceRun(t)
	legacy := Run{Scope: "project:legacy", Workspace: run.Projects[0].Workspace}
	evidence := []ToolEvidence{projectEvidenceInput(t, "read", map[string]string{"path": "file.go"})}
	draft := projectEvidenceDraft(evidence)
	want, err := EvidenceEntitiesAt(legacy.Scope, legacy.Workspace, draft, evidence)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EvidenceEntitiesForRun(legacy, draft, evidence)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy entities %v want %v: %v", got, want, err)
	}
	draft.EvidenceIDs = []string{"invented"}
	if _, err := EvidenceEntitiesForRun(run, draft, evidence); err == nil {
		t.Fatal("unknown evidence accepted")
	}
}

func TestEvidenceEntitiesForRunDoesNotReattributeReplacedCheckout(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "replacement symlink"}[replacement], func(t *testing.T) {
			run := projectEvidenceRun(t)
			alpha, beta := run.Projects[0].Workspace, run.Projects[1].Workspace
			run.Workspace = alpha
			if err := os.Remove(alpha); err != nil {
				t.Fatal(err)
			}
			if replacement {
				if err := os.Symlink(beta, alpha); err != nil {
					t.Fatal(err)
				}
			}
			evidence := []ToolEvidence{
				projectEvidenceInput(t, "absolute", map[string]string{"path": filepath.Join(alpha, "file.go")}),
				projectEvidenceInput(t, "relative", map[string]string{"path": "file.go"}),
			}
			got, err := EvidenceEntitiesForRun(run, projectEvidenceDraft(evidence), evidence)
			if err != nil || len(got) != 0 {
				t.Fatalf("historical alpha paths reattributed: %v, %v", got, err)
			}
		})
	}
}
