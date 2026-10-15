package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/annotation"
)

func TestObjectGroundingAcceptsCaseVariantsWithoutChangingEvidence(t *testing.T) {
	o := objectFixture("tccli")
	o.Kind, o.Name = "tool", "TCCLI"
	if err := ValidateMemoryObjects([]MemoryObject{o}); err != nil {
		t.Fatal(err)
	}
	other := o
	other.Name, other.Kind = "tccli", "service"
	if err := ValidateMemoryObjects([]MemoryObject{o, other}); err == nil {
		t.Fatal("same technical object admitted twice under different kinds")
	}
	if !sameObjectIdentities([]MemoryObject{o}, []MemoryObject{other}) || !strings.Contains(o.Evidence.Text, "tccli") {
		t.Fatal("case/classification changed identity or source")
	}
	o.Name = "TCCLI_UNRELATED"
	if err := ValidateMemoryObjects([]MemoryObject{o}); err == nil {
		t.Fatal("ungrounded subject accepted")
	}
}

func TestV4ObjectCacheMigrationMergesTechnicalNamesAndPreservesMemory(t *testing.T) {
	s, home, _, _ := fileStore(t)
	retained := map[string]Memory{}
	for _, seed := range []struct{ id, name, kind string }{{"one", "cbs", "service"}, {"two", "CBS", "tool"}} {
		r := addRun(t, s, seed.id, "scope", "Saved observation "+seed.id+".", true)
		if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, seed.id)}, decision); err != nil {
			t.Fatal(err)
		}
		o := objectFixture(seed.name)
		o.Kind = seed.kind
		m, err := s.AssociateMemoryObjects(testContext, "scope", seed.id, 1, []MemoryObject{o})
		if err != nil {
			t.Fatal(err)
		}
		retained[seed.id] = m
	}
	before, err := s.VerifyFileMemory(testContext)
	if err != nil {
		t.Fatal(err)
	}
	// A historical v4 cache included classification in its object IDs. Only
	// derived rows are changed here; retained source and revisions stay intact.
	for id, m := range retained {
		o := m.Objects[0]
		hash := sha256.Sum256([]byte(m.Scope + "\x00" + o.Kind + "\x00" + strings.ToLower(o.Name)))
		legacy := annotation.Annotation{Version: "entity-topic-v4", Objects: []annotation.Object{{ID: o.Kind + "-" + hex.EncodeToString(hash[:16]), Name: o.Name, Kind: o.Kind, Span: annotation.Span{Text: o.Name, Start: -1, End: -1, Method: "explicit-object-context", Source: o.Evidence.ID}}}}
		raw, _ := json.Marshal(legacy)
		if _, err := s.db.Exec("UPDATE memory_annotations SET version=?,payload=? WHERE memory_id=?", legacy.Version, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.VerifyFileMemory(testContext)
	if err != nil || before.Digest != after.Digest || before.Generation != after.Generation {
		t.Fatal("cache migration rewrote memory history", before, after, err)
	}
	var identity string
	for _, spelling := range []string{"cbs", "CBS", "CbS"} {
		got, err := s.Retrieve(testContext, "scope", spelling, 10)
		if err != nil || len(got.Matches) != 2 || len(got.Query.ResolvedIDs) != 1 || len(got.Query.Ambiguous) != 0 {
			t.Fatal(spelling, got, err)
		}
		if identity != "" && identity != got.Query.ResolvedIDs[0] {
			t.Fatal("case-dependent identity", got)
		}
		identity = got.Query.ResolvedIDs[0]
		for _, m := range got.Matches {
			if !reflect.DeepEqual(retained[m.ID], m) {
				t.Fatal("original metadata/evidence changed", m.ID)
			}
		}
	}
	var labels int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_annotations WHERE json_extract(payload,'$.objects[0].name')='CBS' AND version=?", annotation.Version).Scan(&labels); err != nil || labels != 2 {
		t.Fatal("derived names did not converge", labels, err)
	}
	foreign, err := s.WithPartition("foreign", "owner").Retrieve(testContext, "scope", "CBS", 10)
	if err != nil || len(foreign.Matches) != 0 {
		t.Fatal("merged object escaped its partition", foreign, err)
	}
}
