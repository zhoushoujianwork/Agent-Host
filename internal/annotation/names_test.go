package annotation

import "testing"

func TestTechnicalNamesResolveAsOneScopedObject(t *testing.T) {
	objects := []Object{ContextObject("scope", "tool", "CBS", "tool"), ContextObject("scope", "service", "cbs", "service"), ContextObject("scope", "system", "CbS", "system")}
	for _, o := range objects {
		if o.ID != objects[0].ID || o.Name != "CBS" || o.Span.Text == "" {
			t.Fatal("technical spelling/classification split an identity", objects)
		}
	}
	records := []Annotation{}
	for _, o := range objects {
		records = append(records, WithContextObjects(Extract("scope", "Historical observation."), []Object{o}))
	}
	for _, spelling := range []string{"cbs", "CBS", "CbS"} {
		q := Resolve("scope", spelling, records)
		if len(q.ResolvedIDs) != 1 || q.ResolvedIDs[0] != objects[0].ID || len(q.Ambiguous) != 0 {
			t.Fatal("case variant did not resolve", spelling, q)
		}
	}
	if other := ContextObject("elsewhere", "tool", "CBS", "other"); other.ID == objects[0].ID {
		t.Fatal("identity crossed its scope")
	}
	person := ContextObject("scope", "person", "cbs", "person")
	q := Resolve("scope", "cbs", append(records, WithContextObjects(Extract("scope", "Personal statement."), []Object{person})))
	if len(q.Ambiguous) != 1 || len(q.ResolvedIDs) != 0 {
		t.Fatal("a person was merged with a technical object", q)
	}
}

func TestObjectDisplayPreservesSourceAndPeople(t *testing.T) {
	tool := ContextObject("scope", "tool", "tccli", "source")
	if tool.Name != "TCCLI" || tool.Span.Text != "tccli" || tool.Span.Source != "source" {
		t.Fatal(tool)
	}
	if got := DisplayName("person", "Mikas Zhou"); got != "Mikas Zhou" {
		t.Fatal("person name was uppercased", got)
	}
	if got := DisplayName("project", "BuildHub 项目"); got != "BUILDHUB 项目" {
		t.Fatal("Latin label was not normalized", got)
	}
}
