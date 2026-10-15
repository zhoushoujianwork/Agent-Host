package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func writeKnowledgePrivate(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeKnowledgeConfig(t *testing.T, path string, c Configuration) {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgePrivate(t, path, string(raw))
}

func knowledgeConfigFixture(t *testing.T) (string, Configuration) {
	t.Helper()
	dir := t.TempDir()
	credential := filepath.Join(dir, "wiki.json")
	writeKnowledgePrivate(t, credential, `{"pat":"private-fixture-token"}`)
	c := Configuration{Version: 1, Sources: []Source{{
		Name: "wiki", Provider: "confluence", Endpoint: "https://wiki.example.test",
		CredentialFile: credential, Principal: "owner", Description: "Company API documentation",
		Projects: []string{"alpha"},
	}}}
	path := filepath.Join(dir, "knowledge.json")
	writeKnowledgeConfig(t, path, c)
	return path, c
}

func TestKnowledgeSelectScopesCredentialsAndPerformsNoPrefetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "configuration must not fetch content", http.StatusInternalServerError)
	}))
	defer server.Close()
	path, c := knowledgeConfigFixture(t)
	c.Sources[0].Name = "z_wiki"
	c.Sources[0].Endpoint = server.URL
	c.Sources[0].Projects = []string{"alpha", "default"}
	dokki := c.Sources[0]
	dokki.Name, dokki.Provider = "a_docs", "dokki"
	dokki.CredentialFile = filepath.Join(filepath.Dir(path), "dokki.json")
	writeKnowledgePrivate(t, dokki.CredentialFile, `{"api_key":"separate-dokki-token"}`)
	unselected := c.Sources[0]
	unselected.Name, unselected.Projects = "private_beta", []string{"beta"}
	unselected.CredentialFile = filepath.Join(filepath.Dir(path), "missing-credential.json")
	c.Sources = append(c.Sources, unselected, dokki)
	writeKnowledgeConfig(t, path, c)
	if _, err := Load(path); err != nil {
		t.Fatalf("local validation unexpectedly loaded credentials: %v", err)
	}
	for _, project := range []string{"alpha", "", "default"} {
		selected, err := Select(path, project)
		if err != nil {
			t.Fatalf("select %q: %v", project, err)
		}
		if len(selected) != 2 || selected[0].Source.Name != "a_docs" || selected[1].Source.Name != "z_wiki" {
			t.Fatalf("unexpected selected source identities for %q", project)
		}
		if selected[0].Credential != "separate-dokki-token" || selected[1].Credential != "private-fixture-token" {
			t.Fatal("credentials crossed source instances")
		}
		if _, err := ResolveSource(path, "a_docs", selected[0].Fingerprint); err != nil {
			t.Fatalf("resolve unchanged source: %v", err)
		}
	}
	if _, err := Select(path, "beta"); err == nil {
		t.Fatal("selected unavailable credential must fail")
	}
	for _, input := range []struct{ path, project string }{{path, "unknown"}, {"", "alpha"}} {
		selected, err := Select(input.path, input.project)
		if err != nil || len(selected) != 0 {
			t.Fatalf("unselected source unexpectedly mounted: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("source selection or resolution prefetched remote content")
	}
}

func TestKnowledgeBindingRejectsAccessAndConfigurationChanges(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *Configuration)
	}{
		{"credential rotation", func(t *testing.T, c *Configuration) {
			writeKnowledgePrivate(t, c.Sources[0].CredentialFile, `{"pat":"rotated-token"}`)
		}},
		{"principal change", func(t *testing.T, c *Configuration) { c.Sources[0].Principal = "different-owner" }},
		{"project revocation", func(t *testing.T, c *Configuration) { c.Sources[0].Projects = []string{"beta"} }},
		{"project grant change", func(t *testing.T, c *Configuration) { c.Sources[0].Projects = []string{"alpha", "beta"} }},
		{"endpoint change", func(t *testing.T, c *Configuration) { c.Sources[0].Endpoint = "https://other.example.test" }},
		{"citation base change", func(t *testing.T, c *Configuration) { c.Sources[0].SourceURLBase = "https://references.example.test" }},
		{"source removed", func(t *testing.T, c *Configuration) { c.Sources = []Source{} }},
		{"credential removed", func(t *testing.T, c *Configuration) {
			if err := os.Remove(c.Sources[0].CredentialFile); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, c := knowledgeConfigFixture(t)
			before, err := Select(path, "alpha")
			if err != nil || len(before) != 1 {
				t.Fatalf("initial selection: %v", err)
			}
			if _, err := ResolveSource(path, "wiki", before[0].Fingerprint); err != nil {
				t.Fatalf("valid launch binding rejected: %v", err)
			}
			tc.change(t, &c)
			writeKnowledgeConfig(t, path, c)
			if _, err := ResolveSource(path, "wiki", before[0].Fingerprint); err == nil {
				t.Fatal("stale launch binding retained access")
			}
			if after, err := Select(path, "alpha"); err == nil && Fingerprint(after) == Fingerprint(before) {
				t.Fatal("runtime binding did not change after access/configuration changed")
			}
		})
	}
}

func TestKnowledgeFingerprintIsStableAcrossConfigReordering(t *testing.T) {
	path, c := knowledgeConfigFixture(t)
	second := c.Sources[0]
	second.Name = "second"
	c.Sources = append(c.Sources, second)
	writeKnowledgeConfig(t, path, c)
	before, err := Select(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	c.Sources[0], c.Sources[1] = c.Sources[1], c.Sources[0]
	writeKnowledgeConfig(t, path, c)
	after, err := Select(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(before) != Fingerprint(after) || Fingerprint(before) == Fingerprint(nil) {
		t.Fatal("config order changed the same selected source set")
	}
}

func TestKnowledgeSecretsExcludedFromHintsResolvedJSONAndErrors(t *testing.T) {
	path, c := knowledgeConfigFixture(t)
	selected, err := Select(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-fixture-token") || strings.Contains(string(raw), `"Credential"`) {
		t.Fatal("resolved source JSON exposed a credential")
	}
	hint, err := json.Marshal(selected[0].Hint())
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-fixture-token", c.Sources[0].CredentialFile, c.Sources[0].Endpoint, c.Sources[0].Principal} {
		if strings.Contains(string(hint), private) {
			t.Fatal("navigation hint exposed private source configuration")
		}
	}
	if !reflect.DeepEqual(selected[0].Hint().Tools, []string{"search_confluence", "read_confluence_page", "create_confluence_page", "update_confluence_page", "read_confluence_blogpost", "create_confluence_blogpost", "update_confluence_blogpost"}) {
		t.Fatal("Confluence hint does not expose the supported tools")
	}
	const secret = "secret-that-must-not-appear"
	for _, body := range []string{
		`{"pat":"` + secret + `","pat":"other"}`,
		`{"pat":"` + secret + `","PAT":"other"}`,
		`{"pat":{"nested":"` + secret + `"}}`,
		`{"pat":"` + secret + `","unknown":"` + secret + `"}`,
		`{"api_key":"` + secret + `"}`,
		`{"pat":"` + secret + `","api_key":"` + secret + `"}`,
		`{"pat":"` + secret + `\n"}`,
		`{"pat":"` + secret + `"} {}`,
		`{"pat":"` + secret,
	} {
		writeKnowledgePrivate(t, c.Sources[0].CredentialFile, body)
		_, err := Select(path, "alpha")
		if err == nil {
			t.Fatal("malformed or wrong-provider credential was accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("credential validation error disclosed a secret")
		}
	}
}

func TestKnowledgeRejectsMalformedOrAmbiguousConfiguration(t *testing.T) {
	path, c := knowledgeConfigFixture(t)
	valid, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "secret-config-value"
	cases := map[string]string{
		"malformed":         `{"version":1,"sources":["` + secret,
		"trailing":          string(valid) + `{}`,
		"unknown field":     strings.Replace(string(valid), `"version":1`, `"version":1,"`+secret+`":true`, 1),
		"duplicate version": strings.Replace(string(valid), `"version":1`, `"version":1,"version":1`, 1),
		"case duplicate":    strings.Replace(string(valid), `"version":1`, `"version":1,"VERSION":1`, 1),
		"nested duplicate":  strings.Replace(string(valid), `"name":"wiki"`, `"name":"wiki","name":"other"`, 1),
		"wrong type":        strings.Replace(string(valid), `"version":1`, `"version":"`+secret+`"`, 1),
		"null sources":      `{"version":1,"sources":null}`,
		"missing sources":   `{"version":1}`,
		"unknown version":   `{"version":2,"sources":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			writeKnowledgePrivate(t, path, body)
			_, err := Load(path)
			if err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("configuration error exposed rejected input")
			}
		})
	}
	writeKnowledgeConfig(t, path, Configuration{Version: 1, Sources: []Source{}})
	if _, err := Load(path); err != nil {
		t.Fatalf("explicit empty source set rejected: %v", err)
	}
}

func TestKnowledgeRejectsIgnoredConfluenceCitationOverride(t *testing.T) {
	path, c := knowledgeConfigFixture(t)
	c.Sources[0].SourceURLBase = "https://references.example.test"
	writeKnowledgeConfig(t, path, c)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "only for Dokki") {
		t.Fatalf("unsupported citation override must fail explicitly: %v", err)
	}
}

func TestKnowledgeRequiresPrivateRegularFiles(t *testing.T) {
	for _, target := range []string{"config", "credential"} {
		for _, mutation := range []string{"world readable", "symlink", "directory", "oversized"} {
			t.Run(target+"/"+mutation, func(t *testing.T) {
				path, c := knowledgeConfigFixture(t)
				file := path
				if target == "credential" {
					file = c.Sources[0].CredentialFile
				}
				switch mutation {
				case "world readable":
					if err := os.Chmod(file, 0644); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Rename(file, file+".real"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(file+".real", file); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(file, 0700); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					writeKnowledgePrivate(t, file, strings.Repeat(" ", 65<<10))
				}
				if _, err := Select(path, "alpha"); err == nil {
					t.Fatal("unsafe file was accepted")
				}
			})
		}
	}
	path, c := knowledgeConfigFixture(t)
	for _, file := range []string{path, c.Sources[0].CredentialFile} {
		if err := os.Chmod(file, 0400); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Select(path, "alpha"); err != nil {
		t.Fatalf("owner read-only credentials rejected: %v", err)
	}
	if _, err := Load("relative-config.json"); err == nil {
		t.Fatal("relative config path accepted")
	}
}

func TestKnowledgeResolveRequiresAnExactPinnedSource(t *testing.T) {
	path, _ := knowledgeConfigFixture(t)
	selected, err := Select(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, fingerprint string }{
		{"wiki", ""}, {"wiki", "short"}, {"wiki", strings.Repeat("0", 64)},
		{"removed", selected[0].Fingerprint},
	} {
		if _, err := ResolveSource(path, tc.name, tc.fingerprint); err == nil {
			t.Fatal("unpinned or mismatched source accepted")
		}
	}
	writeKnowledgePrivate(t, path, `{"version":1,"sources":[]}`)
	if _, err := ResolveSource(path, "wiki", selected[0].Fingerprint); err == nil {
		t.Fatal("removed source retained access")
	}
}
