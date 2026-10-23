package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"

	"agent-host/internal/managedskills"
)

type skillExportManifest struct {
	Format        string                 `json:"format"`
	CatalogDigest string                 `json:"catalog_digest"`
	SkillPath     string                 `json:"skill_path"`
	Enabled       bool                   `json:"enabled"`
	Selected      int                    `json:"selected"`
	Revision      inspectedSkillRevision `json:"revision"`
	Audit         []managedskills.Audit  `json:"audit"`
}

// Export only the immutable catalog snapshot, never the mutable source tree or
// task workspace. Lifecycle state is evidence in the manifest, not permission
// for another Host to publish or execute this copy.
func exportManagedSkill(out io.Writer, catalog managedskills.Catalog, id string, revision int, digest string) error {
	if id == "" || revision < 1 || digest == "" {
		return errors.New("export requires id, revision and digest from inspection")
	}
	for _, record := range catalog.Records {
		if record.ID != id || revision > len(record.Revisions) {
			continue
		}
		v := record.Revisions[revision-1]
		if v.Entry.Digest != digest {
			return errors.New("skill revision digest changed; inspect the catalog again")
		}
		if _, ok := v.Entry.Files["SKILL.md"]; !ok {
			return errors.New("pinned Skill has no SKILL.md")
		}
		manifest := skillExportManifest{
			Format: "agent-host-skill-export-v1", CatalogDigest: managedskills.Hash(catalog),
			SkillPath: "skill/", Enabled: record.Enabled, Selected: record.Selected,
			Revision: inspectedSkillRevision{Revision: v, Files: []inspectedSkillFile{}},
			Audit:    []managedskills.Audit{},
		}
		manifest.Revision.Entry.Files = nil
		paths := make([]string, 0, len(v.Entry.Files))
		total := 0
		for path, body := range v.Entry.Files {
			if !managedskills.SafePath(path) || (path != "SKILL.md" && path != "agents/openai.yaml" && !strings.HasPrefix(path, "references/") && !(v.Entry.Helpers && strings.HasPrefix(path, "scripts/"))) {
				return errors.New("pinned Skill contains an unsupported export path")
			}
			total += len(body)
			if len(body) > managedskills.MaxFileBytes || total > managedskills.MaxCatalogBytes {
				return errors.New("pinned Skill exceeds export budget")
			}
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			manifest.Revision.Files = append(manifest.Revision.Files, inspectSkillFile(path, v.Entry.Files[path]))
		}
		for _, event := range catalog.Audit {
			if event.ID == id && event.Revision == revision {
				manifest.Audit = append(manifest.Audit, event)
			}
		}
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		// Buffer before writing stdout so validation failures cannot produce a
		// seemingly usable partial archive. Sorted files make exports reproducible.
		var buffer bytes.Buffer
		archive := zip.NewWriter(&buffer)
		write := func(path string, body []byte, mode fs.FileMode) error {
			header := &zip.FileHeader{Name: path, Method: zip.Deflate}
			header.SetMode(mode)
			file, err := archive.CreateHeader(header)
			if err != nil {
				return err
			}
			_, err = file.Write(body)
			return err
		}
		if err := write("manifest.json", append(raw, '\n'), 0600); err != nil {
			return err
		}
		for _, path := range paths {
			mode := fs.FileMode(0600)
			if strings.HasPrefix(path, "scripts/") {
				for _, executable := range v.Entry.Executables {
					if executable == path {
						mode = 0700
					}
				}
			}
			if err := write("skill/"+path, []byte(v.Entry.Files[path]), mode); err != nil {
				return err
			}
		}
		if err := archive.Close(); err != nil {
			return err
		}
		_, err = out.Write(buffer.Bytes())
		return err
	}
	return errors.New("skill revision not found")
}
