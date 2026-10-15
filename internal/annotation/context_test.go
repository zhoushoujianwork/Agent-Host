package annotation

import "testing"

func TestContextObjectsRemainScopedAndSourceLinked(t *testing.T) {
	object := ContextObject("scope", "system", "Bastion", "subject")
	a := WithContextObjects(Extract("scope", "The database uses PostgreSQL."), []Object{object})
	q := Resolve("scope", "Bastion database", []Annotation{a})
	if len(q.ResolvedIDs) != 1 || q.ResolvedIDs[0] != object.ID {
		t.Fatalf("unresolved subject: %+v", q)
	}
	if ok, why := Compatible(q, a); !ok {
		t.Fatal(why)
	}
	if object.Span.Start != -1 || object.Span.Source != "subject" || len(a.Topics[0].ObjectIDs) != 1 {
		t.Fatalf("invented source span or lost binding: %+v", a)
	}
	if ok, _ := Compatible(Resolve("scope", "Bastion port", []Annotation{a}), a); ok {
		t.Fatal("wrong property admitted")
	}
	if q := Resolve("foreign", "Bastion database", []Annotation{a}); len(q.ResolvedIDs) != 0 {
		t.Fatal("foreign object resolved", q)
	}
	if q := Resolve("scope", "BastionBackup database", []Annotation{a}); len(q.ResolvedIDs) != 0 {
		t.Fatal("name substring resolved", q)
	}
	other := WithContextObjects(Extract("scope", "The port is 22."), []Object{ContextObject("scope", "service", "Bastion", "other")})
	if q := Resolve("scope", "Bastion database", []Annotation{a, other}); len(q.Ambiguous) != 1 || len(q.ResolvedIDs) != 0 {
		t.Fatal("collision guessed", q)
	}
	multiple := WithContextObjects(Extract("scope", "The database uses PostgreSQL."), []Object{object, ContextObject("scope", "system", "Billing", "other")})
	if len(multiple.Topics[0].ObjectIDs) != 0 {
		t.Fatal("guessed multi-object property", multiple)
	}
}
