package assembly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

func hashSkillBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ManagedSkillPackage combines approved authored revisions, every eligible
// experience and adapter-owned usage instructions without top-k truncation.
func ManagedSkillPackage(cacheRoot, contextID string, entries []managedskills.Entry, memories []state.Memory, identity SkillIdentity, now time.Time) (SkillPackage, error) {
	m, files, err := renderManagedSkills(entries, memories, identity, now)
	if err != nil || len(m.Sources) == 0 {
		return SkillPackage{}, err
	}
	return writeSkillPackage(cacheRoot, contextID, m, files)
}

func ManagedSkillDigest(entries []managedskills.Entry, memories []state.Memory, identity SkillIdentity, now time.Time) (string, error) {
	m, _, err := renderManagedSkills(entries, memories, identity, now)
	if err != nil || len(m.Sources) == 0 {
		return "", err
	}
	return m.Digest, nil
}

func renderManagedSkills(entries []managedskills.Entry, memories []state.Memory, identity SkillIdentity, now time.Time) (harness.SkillManifest, map[string][]byte, error) {
	m, files, err := renderSkillPackage(nil, identity, now)
	if err != nil {
		return m, nil, err
	}
	m.Version = harness.ManagedSkillManifestVersion
	executables := map[string]bool{}
	files[".claude-plugin/plugin.json"] = []byte("{\"name\":\"agent-host\",\"version\":\"1.0.0\",\"description\":\"Host-selected observed procedures\"}\n")
	for _, memory := range memories {
		one, body, err := renderSkillPackage([]state.Memory{memory}, identity, now)
		if err != nil {
			return m, nil, err
		}
		for _, source := range one.Sources {
			source.Kind = "reviewed_experience"
			source.SkillID = "experience:" + memory.Scope + ":" + memory.ID
			source.Title = strings.Split(memory.Text, "\n")[0]
			source.Applicability = memory.Conditions
			source.ContentDigest = managedskills.Hash(memory)
			m.Sources = append(m.Sources, source)
		}
		for p, b := range body {
			files[p] = b
		}
	}
	for _, entry := range entries {
		if entry.Digest != entry.ContentDigest() || (entry.Kind != "authored" && entry.Kind != "plugin_usage") {
			return m, nil, errors.New("invalid managed skill publication")
		}
		name := "procedure-" + managedskills.Hash(entry.Kind + ":" + entry.ID)[:24]
		for _, p := range entry.Executables {
			if !entry.Helpers || !managedskills.SafePath(p) || !strings.HasPrefix(p, "scripts/") {
				return m, nil, errors.New("invalid executable helper adoption")
			}
			if _, ok := entry.Files[p]; !ok {
				return m, nil, errors.New("adopted executable helper is missing")
			}
			executables["skills/"+name+"/"+p] = true
		}
		body := entry.Files["SKILL.md"]
		parts := strings.SplitN(body, "---", 3)
		if len(parts) != 3 || !strings.HasPrefix(body, "---\n") {
			return m, nil, errors.New("managed skill missing frontmatter")
		}
		description, _ := json.Marshal(entry.Description + " Applicable when: " + entry.Applicability)
		files["skills/"+name+"/SKILL.md"] = []byte(fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s", name, description, parts[2]))
		for p, b := range entry.Files {
			if p == "SKILL.md" {
				continue
			}
			if !managedskills.SafePath(p) || (!strings.HasPrefix(p, "references/") && !(entry.Helpers && strings.HasPrefix(p, "scripts/"))) {
				return m, nil, errors.New("invalid managed reference path")
			}
			files["skills/"+name+"/"+p] = []byte(b)
		}
		m.Sources = append(m.Sources, harness.SkillSource{Name: name, SkillID: entry.ID, Kind: entry.Kind, Title: entry.Name + ": " + entry.Description, Applicability: entry.Applicability, ContentDigest: entry.Digest, Revision: entry.Revision, Helpers: entry.Helpers, ToolContractDigest: entry.ToolContractDigest})
	}
	if len(m.Sources) > 512 {
		return m, nil, errors.New("effective Skill catalog exceeds 512 entries; no entries were silently omitted")
	}
	seen := map[string]bool{}
	total := 0
	for _, source := range m.Sources {
		if seen[source.Name] {
			return m, nil, errors.New("duplicate effective Skill identity")
		}
		seen[source.Name] = true
	}
	for p, b := range files {
		total += len(b)
		if len(b) > managedskills.MaxFileBytes || total > managedskills.MaxCatalogBytes || strings.Contains(string(b), "!`") {
			return m, nil, errors.New("managed Skill snapshot exceeds content budget or contains dynamic execution")
		}
		m.Files = append(m.Files, harness.SkillFile{Path: p, SHA256: hashSkillBytes(b), Executable: executables[p]})
	}
	sort.Slice(m.Sources, func(i, j int) bool { return m.Sources[i].Name < m.Sources[j].Name })
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if len(NativeSkillContext(m, memories)) > 128<<10 {
		return m, nil, errors.New("complete Skill descriptors exceed 128 KiB; no entries were silently omitted")
	}
	m.Digest, err = m.ContentDigest()
	return m, files, err
}
