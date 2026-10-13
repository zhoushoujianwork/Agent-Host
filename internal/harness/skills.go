package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SkillManifest binds an instruction-only native package to the exact reviewed
// memory snapshot selected by Host. It is derived data, never an authority.
type SkillManifest struct {
	Version        string        `json:"version"`
	SourceIdentity string        `json:"source_identity"`
	AgentID        string        `json:"agent_id"`
	AudienceID     string        `json:"audience_id"`
	Scopes         []string      `json:"scopes"`
	Sources        []SkillSource `json:"sources"`
	Files          []SkillFile   `json:"files"`
	Digest         string        `json:"digest"`
}

type SkillSource struct {
	Name        string   `json:"name"`
	MemoryID    string   `json:"memory_id"`
	Revision    int      `json:"revision"`
	Scope       string   `json:"scope"`
	SourceRunID string   `json:"source_run_id"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type SkillFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

const SkillManifestVersion = "host-native-skills-v1"

// ContentDigest deliberately omits task paths and creation times. Equal scoped
// publication snapshots stay compatible across task-specific cache directories.
func (m SkillManifest) ContentDigest() (string, error) {
	m.Digest = ""
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
