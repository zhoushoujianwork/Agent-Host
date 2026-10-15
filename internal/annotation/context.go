package annotation

// ContextObject has no offset in the memory statement. Its source excerpt is
// stored separately; negative offsets prevent accidental textual attribution.
func ContextObject(scope, kind, name, evidenceID string) Object {
	return Object{ID: objectID(scope, kind, name), Name: name, Kind: kind,
		Span: Span{Text: name, Start: -1, End: -1, Method: "explicit-object-context", Source: evidenceID}}
}

// WithContextObjects retains explicit metadata without pretending the name
// occurred in the statement. Only one unambiguous subject can bind properties.
func WithContextObjects(a Annotation, objects []Object) Annotation {
	if len(objects) == 0 {
		return a
	}
	for _, o := range objects {
		found := false
		for _, old := range a.Objects {
			if old.ID == o.ID {
				found = true
			}
		}
		if !found {
			a.Objects = append(a.Objects, o)
		}
	}
	ids := []string{}
	for _, o := range a.Objects {
		ids = append(ids, o.ID)
	}
	ids = sortedUnique(ids)
	if len(ids) == 1 {
		for i := range a.Topics {
			if len(a.Topics[i].ObjectIDs) == 0 {
				a.Topics[i].ObjectIDs = append([]string(nil), ids...)
			}
		}
	}
	return a
}
