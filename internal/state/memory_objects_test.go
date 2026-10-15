package state

import (
	"os"
	"testing"
	"time"
)

func objectFixture(name string) MemoryObject {
	now := time.Now().UTC()
	return MemoryObject{Name: name, Kind: "system", Source: ExternalSource{Entry: "test", ObservedAt: now}, Evidence: ExternalEvidence{ID: "context", Role: "user", Author: "owner", Text: "These deployment facts concern " + name + ".", ObservedAt: now}}
}

func TestExplicitObjectAssociationRetrievalAndCacheRebuild(t *testing.T) {
	s, home, _, cache := fileStore(t)
	r := addRun(t, s, "one", "scope", "The deployment namespace is ops.", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "one")}, decision); err != nil {
		t.Fatal(err)
	}
	r2 := addRun(t, s, "two", "scope", "The deployment namespace is other.", true)
	if err := s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{updateFor(r2, "two")}, decision); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"one": "Bastion", "two": "Billing"} {
		if _, err := s.AssociateMemoryObjects(testContext, "scope", id, 1, []MemoryObject{objectFixture(name)}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(s *Store) {
		t.Helper()
		result, err := s.Retrieve(testContext, "scope", "Bastion 运维方式", 10)
		if err != nil || len(result.Matches) != 1 || result.Matches[0].ID != "one" || len(result.Query.ResolvedIDs) != 1 {
			t.Fatalf("object retrieval: %+v %v", result, err)
		}
		m := result.Matches[0]
		if m.Text != r.Input || m.Revision != 2 || m.Objects[0].Evidence.Role != "user" {
			t.Fatalf("source changed: %+v", m)
		}
		other, err := s.Search(testContext, "foreign", "Bastion", 10)
		if err != nil || len(other) != 0 {
			t.Fatalf("scope leaked: %+v %v", other, err)
		}
		if _, err := s.AssociateMemoryObjects(testContext, "scope", "one", 1, []MemoryObject{objectFixture("Other")}); err == nil {
			t.Fatal("stale association accepted")
		}
		if _, err := s.WithPartition("another", "owner").AssociateMemoryObjects(testContext, "scope", "one", 2, []MemoryObject{objectFixture("Other")}); err == nil {
			t.Fatal("foreign partition accepted")
		}
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened)
	if err := reopened.InvalidateMemory(testContext, "one", "obsolete"); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Search(testContext, "scope", "Bastion", 10)
	if err != nil || len(result) != 0 {
		t.Fatalf("disabled object recalled: %+v %v", result, err)
	}
	if _, err := reopened.AssociateMemoryObjects(testContext, "scope", "one", 3, []MemoryObject{objectFixture("Other")}); err == nil {
		t.Fatal("association enabled disabled record")
	}
}

func TestObjectContextValidation(t *testing.T) {
	for _, mutate := range []func(*MemoryObject){
		func(o *MemoryObject) { o.Evidence.Role = "assistant" },
		func(o *MemoryObject) { o.Evidence.Text = "An unrelated system." },
		func(o *MemoryObject) { o.Source.ObservedAt = time.Time{} },
		func(o *MemoryObject) { o.Kind = "permission" },
	} {
		o := objectFixture("Bastion")
		mutate(&o)
		if validateMemoryObjects([]MemoryObject{o}) == nil {
			t.Fatalf("invalid context accepted: %+v", o)
		}
	}
}
