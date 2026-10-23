package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/managedskills"
)

func TestManagedSkillInspectionPinsReferencesAndDoesNotPublish(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skillRoot := filepath.Join(root, "skills")
	e := adoptedCLIEntry(t, skillRoot)
	if err = os.Mkdir(filepath.Join(e.CanonicalRef, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("US region fixture; never substitute CN settings.\n", 4000)
	path := filepath.Join(e.CanonicalRef, "references", "us.md")
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = managedskills.Import(e.CanonicalRef, e)
	if err != nil {
		t.Fatal(err)
	}
	c, err := managedskills.Update(skillRoot, "import", e.ID, "operator", "", "", 0, &e)
	if err != nil {
		t.Fatal(err)
	}
	e = c.Records[0].Revisions[1].Entry
	before, _ := os.ReadFile(filepath.Join(skillRoot, "catalog.json"))
	var out bytes.Buffer
	run := func(extra ...string) error {
		out.Reset()
		return Run(t.Context(), append([]string{"skills", "inspect", "--root", skillRoot}, extra...), nil, &out, io.Discard)
	}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("never substitute")) || !bytes.Contains(out.Bytes(), []byte("references/us.md")) {
		t.Fatal("inspection must expose inventory without source bodies", out.String())
	}
	var view struct {
		Records []inspectedSkillRecord `json:"records"`
	}
	if err = json.Unmarshal(out.Bytes(), &view); err != nil || len(view.Records) != 1 || view.Records[0].Selected != 1 || view.Records[0].Revisions[1].Published {
		t.Fatal("pending revision changed active publication", err)
	}
	if err = os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--id", e.ID, "--revision", "2", "--digest", e.Digest, "--file", "references/us.md"}
	if err = run(args...); err != nil {
		t.Fatal(err)
	}
	var file struct {
		Content string `json:"content"`
		Bytes   int    `json:"bytes"`
	}
	if err = json.Unmarshal(out.Bytes(), &file); err != nil || file.Content != body || file.Bytes != len(body) {
		t.Fatal("reference was truncated or read from mutable source", err)
	}
	for _, path := range []string{"../outside", "/etc/passwd", "references/missing.md", "scripts/run.sh"} {
		args[len(args)-1] = path
		if err = run(args...); err == nil {
			t.Fatal("unsafe or missing reference admitted", path)
		}
	}
	args[len(args)-1] = "SKILL.md"
	args[5] = "stale"
	if err = run(args...); err == nil {
		t.Fatal("stale revision digest admitted")
	}
	after, _ := os.ReadFile(filepath.Join(skillRoot, "catalog.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("inspection changed catalog")
	}
	if err = os.WriteFile(filepath.Join(skillRoot, "catalog.json"), bytes.Replace(before, []byte("Reuse the known"), []byte("Tampered"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run(); err == nil {
		t.Fatal("corrupted catalog admitted")
	}
}
