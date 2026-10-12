package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectUnionDeduplicatesAndExcludesNeutralSources(t *testing.T) {
	path, config := knowledgeConfigFixture(t)
	config.Sources[0].Projects = []string{"alpha", "beta"}
	other := config.Sources[0]
	other.Name, other.Projects = "neutral", []string{"default"}
	other.CredentialFile = filepath.Join(filepath.Dir(path), "missing-neutral.json")
	config.Sources = append(config.Sources, other)
	writeKnowledgeConfig(t, path, config)
	selected, err := SelectProjects(path, []string{"beta", "alpha", "alpha"})
	if err != nil || len(selected) != 1 || selected[0].Source.Name != "wiki" {
		t.Fatalf("%+v %v", selected, err)
	}
	reordered, err := SelectProjects(path, []string{"alpha", "beta"})
	if err != nil || Fingerprint(selected) != Fingerprint(reordered) {
		t.Fatalf("order changed mounts: %v", err)
	}
	if _, err := SelectProjects(path, nil); err == nil {
		t.Fatal("default credential failure hidden")
	}
	if err := os.Remove(config.Sources[0].CredentialFile); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectProjects(path, []string{"beta"}); err == nil {
		t.Fatal("member credential failure hidden")
	}
}
