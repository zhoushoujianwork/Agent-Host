package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agent-host/internal/managedskills"
)

func TestManagedSkillExportPreservesPinnedTreeAndLifecycle(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skillRoot := filepath.Join(root, "skills")
	e := adoptedCLIEntry(t, skillRoot)
	files := map[string]string{
		"references/us.md":    strings.Repeat("US region only.\n", 10000),
		"references/china.md": "China region only.\npassword=synthetic-export-fixture\n",
		"scripts/check.sh":    "#!/bin/sh\nexit 42\n",
		"agents/openai.yaml":  "interface:\n  display_name: Fixture\n",
	}
	for path, body := range files {
		p := filepath.Join(e.CanonicalRef, path)
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	e.Helpers = true
	e, err = managedskills.Import(e.CanonicalRef, e)
	if err != nil {
		t.Fatal(err)
	}
	c, err := managedskills.Update(skillRoot, "import", e.ID, "operator", "", "", 0, &e)
	if err != nil {
		t.Fatal(err)
	}
	e = c.Records[0].Revisions[1].Entry
	// The source may disappear; the exported revision still owns all its files.
	if err = os.RemoveAll(e.CanonicalRef); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(skillRoot, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run := func(rev int, digest string, extra ...string) error {
		out.Reset()
		args := []string{"skills", "export", "--root", skillRoot, "--id", e.ID, "--revision", strconv.Itoa(rev), "--digest", digest}
		return Run(t.Context(), append(args, extra...), nil, &out, io.Discard)
	}
	if err = run(2, e.Digest); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), out.Bytes()...)
	archive, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != len(e.Files)+1 {
		t.Fatal("unexpected archive contents")
	}
	var manifest skillExportManifest
	for _, file := range archive.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "manifest.json" {
			if err = json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		path := strings.TrimPrefix(file.Name, "skill/")
		if string(body) != e.Files[path] {
			t.Fatalf("export changed file %s", path)
		}
		if path == "scripts/check.sh" && file.Mode().Perm() != 0700 || path != "scripts/check.sh" && file.Mode().Perm() != 0600 {
			t.Fatalf("incorrect private file mode: %s %v", path, file.Mode())
		}
	}
	if manifest.Format != "agent-host-skill-export-v1" || manifest.Revision.Entry.Digest != e.Digest || manifest.Revision.Published || manifest.Selected != 1 || !manifest.Enabled || len(manifest.Revision.Files) != len(e.Files) || len(manifest.Audit) != 1 {
		t.Fatal("export confused draft and active state", manifest)
	}
	for _, file := range manifest.Revision.Files {
		if file != inspectSkillFile(file.Path, e.Files[file.Path]) {
			t.Fatal("incorrect file checksum", file.Path)
		}
	}
	if manifest.Revision.Entry.Owner != e.Owner || strings.Join(manifest.Revision.Entry.Provenance, ",") != strings.Join(e.Provenance, ",") {
		t.Fatal("export lost attribution")
	}
	if err = run(2, e.Digest); err != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("unchanged exports must be reproducible", err)
	}
	for _, args := range []struct {
		rev    int
		digest string
		extra  []string
	}{
		{0, e.Digest, nil}, {2, "stale", nil}, {3, e.Digest, nil}, {2, e.Digest, []string{"--file", "SKILL.md"}},
	} {
		if err = run(args.rev, args.digest, args.extra...); err == nil || out.Len() != 0 {
			t.Fatal("invalid selector produced an archive", args)
		}
	}
	after, _ := os.ReadFile(filepath.Join(skillRoot, "catalog.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("export mutated publication or audit history")
	}
	if err = os.WriteFile(filepath.Join(skillRoot, "catalog.json"), bytes.Replace(before, []byte(e.Digest), []byte("corrupt"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run(2, e.Digest); err == nil || out.Len() != 0 {
		t.Fatal("corrupt catalog produced an archive")
	}
}

func TestManagedSkillExportRejectsUnsafePinnedPaths(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", "references/../../escape", "scripts/not-adopted.sh", "credential.json"} {
		e := managedskills.Entry{ID: "fixture", Revision: 1, Files: map[string]string{"SKILL.md": "fixture", path: "bad"}}
		e.Digest = e.ContentDigest()
		c := managedskills.Catalog{Records: []managedskills.Record{{ID: e.ID, Revisions: []managedskills.Revision{{Entry: e}}}}}
		var out bytes.Buffer
		if err := exportManagedSkill(&out, c, e.ID, 1, e.Digest); err == nil || out.Len() != 0 {
			t.Fatal("unsafe export path", path)
		}
	}
}
