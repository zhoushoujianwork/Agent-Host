package annotation

// ResolveRecord uses a record's explicit subject only for that record's own
// exact name. Ordinary queries and unrelated aliases retain global ambiguity.
func ResolveRecord(scope, text string, records []Annotation, objects []Object) Query {
	preferred := map[string]map[string]bool{}
	for _, o := range objects {
		if o.ID != objectID(scope, o.Kind, o.Name) {
			continue
		}
		name := canonical(o.Name)
		if preferred[name] == nil {
			preferred[name] = map[string]bool{}
		}
		preferred[name][o.ID] = true
	}
	if len(preferred) == 0 {
		return Resolve(scope, text, records)
	}
	filtered := make([]Annotation, 0, len(records))
	for _, record := range records {
		copy := record
		copy.Objects, copy.Aliases = nil, nil
		for _, o := range record.Objects {
			ids := preferred[canonical(o.Name)]
			if len(ids) != 1 || ids[o.ID] {
				copy.Objects = append(copy.Objects, o)
			}
		}
		for _, a := range record.Aliases {
			ids := preferred[canonical(a.Name)]
			if len(ids) != 1 || ids[a.TargetID] {
				copy.Aliases = append(copy.Aliases, a)
			}
		}
		filtered = append(filtered, copy)
	}
	return Resolve(scope, text, filtered)
}

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
