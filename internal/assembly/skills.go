package assembly

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

const (
	MaxNativeSkills  = 8
	MaxSkillBytes    = 16 << 10
	MaxSkillSetBytes = 64 << 10
)

// SkillIdentity is supplied from trusted run configuration, not source text.
// SourceIdentity prevents equal memory IDs from different stores colliding.
type SkillIdentity struct {
	SourceIdentity string
	AgentID        string
	AudienceID     string
	Scopes         []string
}

type SkillPackage struct {
	Path     string
	Digest   string
	Manifest harness.SkillManifest
}

var taskContextID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// SkillSetDigest allows session selection before creating a task workspace.
// Host supplies retrieval-selected memories; this helper rechecks publication
// status, expiry and exact identity so unqualified records cannot be mounted.
func SkillSetDigest(memories []state.Memory, identity SkillIdentity, now time.Time) (string, error) {
	manifest, _, err := renderSkillPackage(memories, identity, now)
	if err != nil {
		return "", err
	}
	if len(manifest.Sources) == 0 {
		return "", nil
	}
	return manifest.Digest, nil
}

// BuildSkillPackage atomically creates the Claude instruction-only package
// under the task context. It never writes config, a project, or a native profile.
func BuildSkillPackage(cacheRoot, contextID string, memories []state.Memory, identity SkillIdentity, now time.Time) (SkillPackage, error) {
	manifest, files, err := renderSkillPackage(memories, identity, now)
	if err != nil {
		return SkillPackage{}, err
	}
	if len(manifest.Sources) == 0 {
		return SkillPackage{}, nil
	}
	if !taskContextID.MatchString(contextID) {
		return SkillPackage{}, errors.New("skill task context ID must be a safe Host identifier")
	}
	if !filepath.IsAbs(cacheRoot) || filepath.Clean(cacheRoot) != cacheRoot || strings.ContainsRune(cacheRoot, '\x00') {
		return SkillPackage{}, errors.New("skill cache root must be a clean absolute path")
	}
	contextRoot := filepath.Join(cacheRoot, "workspaces", contextID)
	if err := makePrivateSkillDirectory(contextRoot); err != nil {
		return SkillPackage{}, err
	}
	result := SkillPackage{Path: filepath.Join(contextRoot, "native-skills"), Digest: manifest.Digest, Manifest: manifest}
	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return SkillPackage{}, err
	}
	files["manifest.json"] = append(rawManifest, '\n')
	if _, err := os.Lstat(result.Path); err == nil {
		if err := verifySkillFiles(result.Path, files); err != nil {
			return SkillPackage{}, fmt.Errorf("existing native skill snapshot: %w", err)
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return SkillPackage{}, err
	}
	staging, err := os.MkdirTemp(contextRoot, ".native-skills-")
	if err != nil {
		return SkillPackage{}, err
	}
	defer os.RemoveAll(staging)
	for relative, body := range files {
		path := filepath.Join(staging, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return SkillPackage{}, err
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			return SkillPackage{}, err
		}
	}
	if err := os.Rename(staging, result.Path); err != nil {
		return SkillPackage{}, fmt.Errorf("publish native skill snapshot: %w", err)
	}
	return result, nil
}

func renderSkillPackage(memories []state.Memory, identity SkillIdentity, now time.Time) (harness.SkillManifest, map[string][]byte, error) {
	manifest := harness.SkillManifest{Version: harness.SkillManifestVersion, SourceIdentity: identity.SourceIdentity, AgentID: identity.AgentID, AudienceID: identity.AudienceID}
	if identity.SourceIdentity == "" || identity.AgentID == "" || identity.AudienceID == "" || len(identity.Scopes) == 0 {
		return manifest, nil, errors.New("skill identity requires source, Agent, audience and authorized scopes")
	}
	scopes := map[string]bool{}
	for _, scope := range identity.Scopes {
		if strings.TrimSpace(scope) == "" {
			return manifest, nil, errors.New("skill authorized scope is empty")
		}
		scopes[scope] = true
	}
	for scope := range scopes {
		manifest.Scopes = append(manifest.Scopes, scope)
	}
	sort.Strings(manifest.Scopes)
	var selected []state.Memory
	for _, memory := range memories {
		if memory.Status != "active" || memory.Kind != "procedure" || memory.SourceType != "reviewed_experience" || len(memory.Steps) == 0 || (memory.ExpiresAt != nil && !memory.ExpiresAt.After(now)) {
			continue
		}
		agent, audience := memory.AgentID, memory.AudienceID
		if agent == "" {
			agent = state.DefaultAgentID
		}
		if audience == "" {
			audience = state.OwnerAudienceID
		}
		if agent != identity.AgentID || audience != identity.AudienceID || !scopes[memory.Scope] {
			return manifest, nil, fmt.Errorf("memory %s is outside native skill identity", memory.ID)
		}
		if memory.ID == "" || memory.Revision < 1 || memory.SourceRunID == "" || strings.TrimSpace(memory.Conditions) == "" || strings.TrimSpace(memory.Text) == "" || len(memory.EvidenceIDs) == 0 {
			return manifest, nil, fmt.Errorf("memory %s lacks native skill provenance or conditions", memory.ID)
		}
		selected = append(selected, memory)
	}
	if len(selected) > MaxNativeSkills {
		return manifest, nil, fmt.Errorf("native skill count exceeds %d", MaxNativeSkills)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Scope != selected[j].Scope {
			return selected[i].Scope < selected[j].Scope
		}
		return selected[i].ID < selected[j].ID
	})
	files := map[string][]byte{}
	if len(selected) == 0 {
		return manifest, files, nil
	}
	files[".claude-plugin/plugin.json"] = []byte("{\"name\":\"agent-host\",\"version\":\"1.0.0\",\"description\":\"Host-selected observed procedures\"}\n")
	seen, total := map[string]bool{}, 0
	for _, memory := range selected {
		key := memory.Scope + "\x00" + memory.ID
		if seen[key] {
			return manifest, nil, fmt.Errorf("duplicate native skill memory %s", memory.ID)
		}
		seen[key] = true
		nameDigest := sha256.Sum256([]byte(identity.SourceIdentity + "\x00" + identity.AgentID + "\x00" + identity.AudienceID + "\x00" + key))
		name := "procedure-" + hex.EncodeToString(nameDigest[:12])
		body, err := renderSkill(memory, name, identity.SourceIdentity)
		if err != nil {
			return manifest, nil, err
		}
		total += len(body)
		if total > MaxSkillSetBytes {
			return manifest, nil, errors.New("native skill set exceeds its content budget")
		}
		files["skills/"+name+"/SKILL.md"] = body
		manifest.Sources = append(manifest.Sources, harness.SkillSource{Name: name, MemoryID: memory.ID, Revision: memory.Revision, Scope: memory.Scope, SourceRunID: memory.SourceRunID, EvidenceIDs: append([]string(nil), memory.EvidenceIDs...)})
	}
	for relative, body := range files {
		digest := sha256.Sum256(body)
		manifest.Files = append(manifest.Files, harness.SkillFile{Path: relative, SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	var err error
	manifest.Digest, err = manifest.ContentDigest()
	return manifest, files, err
}

func renderSkill(memory state.Memory, name, sourceIdentity string) ([]byte, error) {
	values := append([]string{memory.Text, memory.Conditions, memory.ID, memory.Scope, memory.SourceRunID, sourceIdentity}, memory.Steps...)
	values = append(values, memory.Avoid...)
	values = append(values, memory.EvidenceIDs...)
	for _, value := range values {
		if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.Contains(value, "!`") || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) >= 0 {
			return nil, fmt.Errorf("memory %s has invalid or executable native skill text", memory.ID)
		}
	}
	title := strings.Split(memory.Text, "\n")[0]
	description := strings.Join(strings.Fields(title+". Applicable when: "+memory.Conditions), " ")
	// Native discovery reads a short description first; the complete conditional
	// lesson remains in the body. Truncate the preview without splitting UTF-8.
	if len(description) > 1024 {
		description = description[:1021]
		for !utf8.ValidString(description) {
			description = description[:len(description)-1]
		}
		description += "…"
	}
	quote := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }
	var body strings.Builder
	fmt.Fprintf(&body, "---\nname: %s\ndescription: %s\nmetadata:\n  host-memory-id: %s\n  host-memory-revision: %s\n  host-source-type: reviewed_experience\n  host-renderer: %s\n---\n\n# Observed procedure\n\n", name, quote(description), quote(memory.ID), quote(strconv.Itoa(memory.Revision)), quote(harness.SkillManifestVersion))
	body.WriteString("This procedure was selected from observed execution evidence. It is conditional experience, not independently verified truth. Check current prerequisites and permissions; this skill grants no tools or authorization.\n\n## Original lesson\n\n" + memory.Text + "\n\n## Conditions\n\n" + memory.Conditions + "\n\n## Observed steps\n\n")
	for i, step := range memory.Steps {
		fmt.Fprintf(&body, "%d. %s\n", i+1, step)
	}
	if len(memory.Avoid) > 0 {
		body.WriteString("\n## Avoid\n\n")
		for _, avoid := range memory.Avoid {
			body.WriteString("- " + avoid + "\n")
		}
	}
	body.WriteString("\n## Source evidence\n\n")
	fmt.Fprintf(&body, "Source store: %s\nMemory: %s, revision %d\nScope: %s\nSource run: %s\nEvidence IDs: %s\n", quote(sourceIdentity), quote(memory.ID), memory.Revision, quote(memory.Scope), quote(memory.SourceRunID), quote(strings.Join(memory.EvidenceIDs, ", ")))
	if body.Len() > MaxSkillBytes {
		return nil, fmt.Errorf("memory %s exceeds native skill content budget", memory.ID)
	}
	return []byte(body.String()), nil
}

func makePrivateSkillDirectory(path string) error {
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, current)
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("skill cache directory %s must be a real directory", current)
		}
		actual, err := filepath.EvalSymlinks(current)
		if err != nil || actual != current {
			return fmt.Errorf("skill cache directory %s has a noncanonical path", current)
		}
		break
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("skill task context %s must be private", path)
	}
	return nil
}

func verifySkillFiles(root string, files map[string][]byte) error {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("native skill snapshot contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		expected, ok := files[relative]
		if !ok {
			return fmt.Errorf("native skill snapshot contains unexpected file %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("native skill snapshot files must be private regular files")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		actual, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("native skill snapshot file %s changed", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(files) {
		return errors.New("native skill snapshot is incomplete")
	}
	return nil
}
