package managedskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func authored(t *testing.T, id string) Entry {
	t.Helper()
	root := canonicalTemp(t)
	if err := os.Mkdir(filepath.Join(root, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	for p, b := range map[string]string{"SKILL.md": "---\nname: sftp-delivery\ndescription: Deliver SFTP in China or the US\n---\nRead references/china.md or references/us.md using the current ticket region. Reuse instance_id.\n", "references/china.md": "China: profile tencent-cn; data root cn-only; never reuse US targets.\n", "references/us.md": "US: profile tencent-us; data root us-only; never reuse China targets.\n"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := Import(root, Entry{ID: id, Owner: "operator", Applicability: "customer SFTP delivery", Agents: []string{"default"}, Audiences: []string{"owner"}, Scopes: []string{"project-a"}, Provenance: []string{"operator authored regional runbook"}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPublicationPinsReferencesAndPreservesAudit(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "managed")
	e := authored(t, "delivery")
	c, err := Update(root, "import", e.ID, "operator", "", "", 0, &e)
	if err != nil {
		t.Fatal(err)
	}
	v := c.Records[0].Revisions[0]
	if _, err = Update(root, "publish", e.ID, "operator", v.Entry.Digest, "", 1, nil); err == nil {
		t.Fatal("unreviewed publication admitted")
	}
	for _, action := range []string{"review", "publish", "enable"} {
		c, err = Update(root, action, e.ID, "reviewer", v.Entry.Digest, "source reviewed including both regions", 1, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	identity := Identity{Agent: "default", Audience: "owner", Scopes: []string{"project-a"}}
	s := Resolve(c, identity, nil, time.Now())
	if len(s.Entries) != 1 {
		t.Fatalf("snapshot: %+v", s)
	}
	if err = os.WriteFile(filepath.Join(e.CanonicalRef, "references/us.md"), []byte("changed unreviewed"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := Resolve(loaded, identity, nil, time.Now()); got.Digest != s.Digest || strings.Contains(got.Entries[0].Files["references/us.md"], "unreviewed") {
		t.Fatal("source editing changed publication")
	}
	newEntry, err := Import(e.CanonicalRef, e)
	if err != nil {
		t.Fatal(err)
	}
	c, err = Update(root, "import", e.ID, "operator", "", "", 0, &newEntry)
	if err != nil {
		t.Fatal(err)
	}
	second := c.Records[0].Revisions[1].Entry
	for _, action := range []string{"review", "publish", "enable"} {
		c, err = Update(root, action, e.ID, "reviewer", second.Digest, "review correction", 2, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err = Update(root, "rollback", e.ID, "operator", v.Entry.Digest, "", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Records[0].Selected != 1 || len(c.Records[0].Revisions) != 2 || len(c.Audit) != 9 {
		t.Fatalf("lost history: %+v", c)
	}
	c, err = Update(root, "withdraw", e.ID, "operator", v.Entry.Digest, "", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(Resolve(c, identity, nil, time.Now()).Entries) != 0 {
		t.Fatal("withdrawn skill active")
	}
}

func TestReadinessRestrictionsExpiryAndDuplicates(t *testing.T) {
	e := authored(t, "delivery")
	e.Revision = 1
	e.RequiredPlugins = []string{"dingtalk"}
	e.RequiredTools = []string{"mcp__dingtalk__read_ticket"}
	e.Digest = e.ContentDigest()
	c := Catalog{Version: ContractVersion, Records: []Record{{ID: e.ID, Enabled: true, Selected: 1, Revisions: []Revision{{Entry: e, Published: true, Reviewer: "reviewer", ReviewNote: "review"}}}}}
	id := Identity{Agent: "default", Audience: "owner", Scopes: []string{"project-a"}}
	unready := Resolve(c, id, nil, time.Now())
	if len(unready.Entries) != 0 || len(unready.Unready) != 1 {
		t.Fatal("dependency silently ready")
	}
	plugins := map[string]Plugin{"dingtalk": {Tools: []string{"mcp__dingtalk__read_ticket"}, Contract: "current-contract"}}
	e.RequiredContracts = map[string]string{"dingtalk": "current-contract"}
	c.Records[0].Revisions[0].Entry = e
	ready := Resolve(c, id, plugins, time.Now())
	if len(ready.Entries) != 1 || ready.Digest == unready.Digest {
		t.Fatal("dependency fingerprint missing")
	}
	incompatible := map[string]Plugin{"dingtalk": {Tools: plugins["dingtalk"].Tools, Contract: "changed-contract"}}
	if s := Resolve(c, id, incompatible, time.Now()); len(s.Entries) != 0 || len(s.Unready) != 1 {
		t.Fatal("incompatible contract admitted")
	}
	for _, foreign := range []Identity{{Agent: "other", Audience: "owner", Scopes: id.Scopes}, {Agent: "default", Audience: "other", Scopes: id.Scopes}, {Agent: "default", Audience: "owner", Scopes: []string{"other"}}} {
		if len(Resolve(c, foreign, plugins, time.Now()).Entries) != 0 {
			t.Fatal("foreign skill admitted")
		}
	}
	expired := time.Now().Add(-time.Second)
	c.Records[0].Revisions[0].Entry.ExpiresAt = &expired
	if len(Resolve(c, id, plugins, time.Now()).Entries) != 0 {
		t.Fatal("expired skill admitted")
	}
}

func TestImportRejectsLinksHooksAndUnadoptedHelpers(t *testing.T) {
	for _, mode := range []string{"symlink", "hook", "helper", "traversal"} {
		t.Run(mode, func(t *testing.T) {
			e := authored(t, "delivery")
			p := filepath.Join(e.CanonicalRef, "references", "bad")
			switch mode {
			case "symlink":
				if err := os.Symlink("/etc/hosts", p); err != nil {
					t.Fatal(err)
				}
			case "hook":
				if err := os.WriteFile(filepath.Join(e.CanonicalRef, "SKILL.md"), []byte("---\nname: bad\ndescription: test\nhooks: {}\n---\nbody"), 0600); err != nil {
					t.Fatal(err)
				}
			case "helper":
				if err := os.WriteFile(filepath.Join(e.CanonicalRef, "run.sh"), []byte("echo forbidden"), 0600); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				if SafePath("references/../../outside") {
					t.Fatal("traversal allowed")
				}
				return
			}
			if _, err := Import(e.CanonicalRef, e); err == nil {
				t.Fatal("unsafe source adopted")
			}
		})
	}
}

func TestCatalogTamperingFailsIntegrity(t *testing.T) {
	e := authored(t, "delivery")
	root := canonicalTemp(t)
	if _, err := Update(root, "import", e.ID, "operator", "", "", 0, &e); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "catalog.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "us-only", "cn-only", 1))
	if err = os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(root); err == nil {
		t.Fatal("changed pinned reference accepted")
	}
}

func TestDuplicateNamesRequireExplicitDisablement(t *testing.T) {
	root := canonicalTemp(t)
	first := authored(t, "first")
	second := authored(t, "second")
	for _, e := range []Entry{first, second} {
		c, err := Update(root, "import", e.ID, "operator", "", "", 0, &e)
		if err != nil {
			t.Fatal(err)
		}
		v := c.Records[len(c.Records)-1].Revisions[0].Entry
		for _, action := range []string{"review", "publish"} {
			if _, err = Update(root, action, e.ID, "reviewer", v.Digest, "reviewed source", 1, nil); err != nil {
				t.Fatal(err)
			}
		}
		_, err = Update(root, "enable", e.ID, "operator", v.Digest, "", 1, nil)
		if e.ID == "first" && err != nil {
			t.Fatal(err)
		}
		if e.ID == "second" && (err == nil || !strings.Contains(err.Error(), "duplicate")) {
			t.Fatal("duplicate selected without explicit resolution", err)
		}
	}
}

func TestConditionalHostToolsResolveFromActualUsageContract(t *testing.T) {
	contract := []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}{{"read_context", "Read exact scoped context"}}
	usage := PluginUsage("host_context", contract, "Read source evidence before claims.", "binding")
	e := Entry{ID: "dependent", RequiredPlugins: []string{"host_context"}, RequiredTools: []string{"mcp__host_context__read_context"}, RequiredContracts: map[string]string{"host_context": ContractDigest(contract)}}
	if ready, unready, err := ReadyEntries([]Entry{e}); err != nil || len(ready) != 0 || len(unready) != 1 {
		t.Fatal("conditional dependency admitted before mount", ready, unready, err)
	}
	if ready, unready, err := ReadyEntries([]Entry{e, usage}); err != nil || len(ready) != 2 || len(unready) != 0 {
		t.Fatal("actual contract not ready", ready, unready, err)
	}
	usage.Files["references/contract.json"] = `[{"name":"different_tool"}]`
	if _, _, err := ReadyEntries([]Entry{e, usage}); err == nil {
		t.Fatal("inconsistent guidance contract admitted")
	}
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
