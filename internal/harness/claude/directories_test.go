//go:build unix

package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestAdditionalDirectoriesKeepNativeInstructionsAndBridgeAgents(t *testing.T) {
	for _, version := range []string{"2.1.223 (Claude Code)", "2.1.277 (Claude Code)"} {
		t.Run(version, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "success")
			t.Setenv("AGENT_HOST_FIXTURE_VERSION", version)
			t.Setenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "0")
			capturePath := filepath.Join(t.TempDir(), "request.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capturePath)
			agentsDir, _ := filepath.EvalSymlinks(t.TempDir())
			nativeDir, _ := filepath.EvalSymlinks(t.TempDir())
			for path, text := range map[string]string{
				filepath.Join(agentsDir, "AGENTS.md"): "Secondary marker: MAGNOLIA.",
				filepath.Join(nativeDir, "CLAUDE.md"): "Native rules.",
				filepath.Join(nativeDir, "AGENTS.md"): "Do not duplicate this entry.",
			} {
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			request.AdditionalDirectories = []string{agentsDir, nativeDir}
			request.ToolsDisabled = true
			var observations []harness.Observation
			result, err := adapter.Run(t.Context(), request, func(o harness.Observation) error { observations = append(observations, o); return nil })
			if err != nil || result.Status != "completed" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			raw, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Args         []string `json:"args"`
				Instructions string   `json:"additional_instructions"`
				Memory       string   `json:"auto_memory_disabled"`
			}
			if err := json.Unmarshal(raw, &capture); err != nil {
				t.Fatal(err)
			}
			if capture.Instructions != "1" || capture.Memory != "1" || os.Getenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD") != "0" {
				t.Fatalf("per-child policy changed parent env: %+v", capture)
			}
			var directories []string
			for i, arg := range capture.Args {
				if arg == "--add-dir" {
					directories = append(directories, capture.Args[i+1])
				}
			}
			if !slices.Equal(directories, request.AdditionalDirectories) {
				t.Fatalf("additional access set: %v", capture.Args)
			}
			index := slices.Index(capture.Args, "--append-system-prompt")
			if index < 0 || !strings.Contains(capture.Args[index+1], agentsDir) || !strings.Contains(capture.Args[index+1], "MAGNOLIA") || strings.Contains(capture.Args[index+1], "Do not duplicate") {
				t.Fatalf("instruction ownership: %v", capture.Args)
			}
			if len(observations) == 0 || observations[0].Type != "project_instructions" || !strings.Contains(string(observations[0].Raw), `"additional_directory":true`) {
				t.Fatalf("missing source attribution: %+v", observations)
			}
		})
	}
}

func TestRuntimeDirectoriesAreRevalidatedBeforeNativeLaunch(t *testing.T) {
	for _, kind := range []string{"missing", "file", "replaced-symlink"} {
		t.Run(kind, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "success")
			capturePath := filepath.Join(t.TempDir(), "request.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capturePath)
			parent, _ := filepath.EvalSymlinks(t.TempDir())
			directory := filepath.Join(parent, "attached")
			switch kind {
			case "file":
				if err := os.WriteFile(directory, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced-symlink":
				if err := os.Symlink(request.Workspace, directory); err != nil {
					t.Fatal(err)
				}
			}
			request.AdditionalDirectories = []string{directory}
			result, err := adapter.Run(t.Context(), request, nil)
			if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "directory") {
				t.Fatalf("invalid directory accepted: %+v %v", result, err)
			}
			if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
				t.Fatalf("native process launched: %v", err)
			}
		})
	}
}

func TestExtractionDoesNotInheritAdditionalInstructionEnvironment(t *testing.T) {
	adapter, request := fixtureAdapter(t, "success")
	t.Setenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "1")
	path := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
	request.Extraction, request.ToolsDisabled = true, true
	if _, err := adapter.Run(t.Context(), request, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Instructions string `json:"additional_instructions"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Instructions != "" {
		t.Fatal("isolated extraction inherited attached instruction policy")
	}
}
