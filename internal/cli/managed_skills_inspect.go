package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"agent-host/internal/managedskills"
)

type inspectedSkillFile struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type inspectedSkillRevision struct {
	managedskills.Revision
	Files []inspectedSkillFile `json:"file_inventory"`
}

type inspectedSkillRecord struct {
	ID        string                   `json:"id"`
	Enabled   bool                     `json:"enabled"`
	Selected  int                      `json:"selected"`
	Revisions []inspectedSkillRevision `json:"revisions"`
}

// Inspection uses the same integrity-checked publication source as execution.
// File reads bind to immutable revisions and never follow a caller-supplied path.
func inspectManagedSkills(out io.Writer, c managedskills.Catalog, id string, revision int, digest, file string) error {
	if file != "" {
		if id == "" || revision < 1 || digest == "" || !managedskills.SafePath(file) || (file != "SKILL.md" && !strings.HasPrefix(file, "references/")) {
			return errors.New("file inspection requires id, revision, digest and an instruction/reference path")
		}
		for _, record := range c.Records {
			if record.ID != id || revision > len(record.Revisions) {
				continue
			}
			e := record.Revisions[revision-1].Entry
			if e.Digest != digest {
				return errors.New("skill revision digest changed; inspect the catalog again")
			}
			body, ok := e.Files[file]
			if !ok {
				return errors.New("pinned skill file not found")
			}
			return json.NewEncoder(out).Encode(struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
				Digest   string `json:"digest"`
				Content  string `json:"content"`
				inspectedSkillFile
			}{id, revision, digest, body, inspectSkillFile(file, body)})
		}
		return errors.New("skill revision not found")
	}
	if id != "" || revision != 0 || digest != "" {
		return errors.New("revision selectors require --file")
	}
	records := []inspectedSkillRecord{}
	for _, record := range c.Records {
		r := inspectedSkillRecord{ID: record.ID, Enabled: record.Enabled, Selected: record.Selected, Revisions: []inspectedSkillRevision{}}
		for _, revision := range record.Revisions {
			v := inspectedSkillRevision{Revision: revision, Files: []inspectedSkillFile{}}
			for path, body := range revision.Entry.Files {
				v.Files = append(v.Files, inspectSkillFile(path, body))
			}
			sort.Slice(v.Files, func(i, j int) bool { return v.Files[i].Path < v.Files[j].Path })
			v.Entry.Files = nil
			r.Revisions = append(r.Revisions, v)
		}
		records = append(records, r)
	}
	return json.NewEncoder(out).Encode(struct {
		Version string                 `json:"version"`
		Digest  string                 `json:"catalog_digest"`
		Records []inspectedSkillRecord `json:"records"`
		Audit   []managedskills.Audit  `json:"audit"`
	}{c.Version, managedskills.Hash(c), records, c.Audit})
}

func inspectSkillFile(path, body string) inspectedSkillFile {
	h := sha256.Sum256([]byte(body))
	return inspectedSkillFile{Path: path, Bytes: len(body), SHA256: hex.EncodeToString(h[:])}
}
