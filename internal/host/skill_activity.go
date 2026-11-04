package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
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
	calls       map[string]harness.ToolObservation
	mountedRoot string
}

func (a *skillActivity) bindMount(p assembly.SkillPackage, mount *harness.SkillMount) error {
	if mount == nil {
		return nil
	}
	if p.Digest == "" || mount.Digest != p.Digest || !filepath.IsAbs(mount.Directory) || filepath.Clean(mount.Directory) != mount.Directory || strings.ContainsRune(mount.Directory, 0) {
		return errors.New("native Skill mount metadata does not match the assembled package")
	}
	canonical, err := filepath.EvalSymlinks(mount.Directory)
	if err != nil || canonical != mount.Directory {
		return errors.New("native Skill mount metadata is not canonical")
	}
	info, err := os.Lstat(mount.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("native Skill mount metadata is not a private directory")
	}
	manifestPath := filepath.Join(mount.Directory, "manifest.json")
	info, err = os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("native Skill mount metadata has no private regular manifest")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("native Skill mount manifest exceeds its size limit")
	}
	var manifest harness.SkillManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return errors.New("native Skill mount manifest is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("native Skill mount manifest has trailing content")
	}
	digest, err := manifest.ContentDigest()
	if err != nil || manifest.Digest != p.Digest || digest != p.Digest {
		return errors.New("native Skill mount manifest does not match the assembled package")
	}
	a.mountedRoot = mount.Directory
	return nil
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
	data := map[string]any{"snapshot_digest": p.Digest, "source_root": p.Path, "tool_id": t.ID, "stage": t.Stage, "is_error": t.IsError, "input": input}
	if a.mountedRoot != "" {
		data["mounted_root"] = a.mountedRoot
	}
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
				sourcePath := filepath.Join(p.Path, filepath.FromSlash(f.Path))
				mountedPath := filepath.Join(a.mountedRoot, filepath.FromSlash(f.Path))
				if (path == sourcePath || a.mountedRoot != "" && path == mountedPath) && strings.Contains(f.Path, "/references/") {
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
