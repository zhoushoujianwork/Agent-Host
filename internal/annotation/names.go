package annotation

import "strings"

// IdentityKind treats different technical classifications as one named object.
// People, projects and organizations retain their own identity namespaces.
func IdentityKind(kind string) string {
	switch kind {
	case "system", "service", "tool":
		return "system"
	default:
		return kind
	}
}

// IdentityKey is independent of presentation case and technical classification.
// The caller still supplies its normal scope/partition boundary.
func IdentityKey(kind, name string) string {
	return IdentityKind(kind) + ":" + canonical(name)
}

// DisplayName is a projection; retained names and evidence are never rewritten.
func DisplayName(kind, name string) string {
	if kind == "person" {
		return name
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}, strings.Join(strings.Fields(name), " "))
}
