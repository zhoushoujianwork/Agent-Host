package host

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"agent-host/internal/assembly"
	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

// skillActivity records native calls and results, not inferred inventory loads.
// Reference coverage requires inspecting the recorded ranges and native results.
type skillActivity struct {
	calls map[string]harness.ToolObservation
}

func (a *skillActivity) observe(emit func(event.Event) error, p assembly.SkillPackage, t *harness.ToolObservation) error {
	if p.Digest == "" {
		return nil
	}
	if a.calls == nil {
		a.calls = map[string]harness.ToolObservation{}
	}
	if t.Stage == "call" {
		a.calls[t.ID] = *t
	}
	call := *t
	if previous, ok := a.calls[t.ID]; ok && len(call.Input) == 0 {
		call.Input = previous.Input
		call.Name = previous.Name
	}
	var input map[string]any
	_ = json.Unmarshal(call.Input, &input)
	data := map[string]any{"snapshot_digest": p.Digest, "tool_id": t.ID, "stage": t.Stage, "is_error": t.IsError, "input": input}
	typ := ""
	if call.Name == "Skill" {
		name, _ := input["skill"].(string)
		for _, s := range p.Manifest.Sources {
			if name == "agent-host:"+s.Name {
				data["source"] = s
				typ = "skills.load.observed"
			}
		}
	}
	if call.Name == "Read" {
		path, _ := input["file_path"].(string)
		if filepath.IsAbs(path) {
			for _, f := range p.Manifest.Files {
				if path == filepath.Join(p.Path, filepath.FromSlash(f.Path)) && strings.Contains(f.Path, "/references/") {
					data["file"] = f
					typ = "skills.reference.observed"
				}
			}
		}
	}
	if typ == "" {
		return nil
	}
	status := "requested"
	if t.Stage == "result" {
		status = "completed"
		if t.IsError {
			status = "failed"
		}
	}
	return emit(event.Event{Type: typ, Phase: "execute", Component: "native.skills", Status: status, Message: "native load evidence; reference coverage and business acceptance are separate", Data: data})
}

func legacySkillContext(managed bool, manifest harness.SkillManifest, memories []state.Memory) string {
	if managed {
		return ""
	}
	return assembly.NativeSkillContext(manifest, memories)
}
