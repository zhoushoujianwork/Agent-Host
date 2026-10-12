package routing

import (
	"net"
	"net/url"
	"regexp"
	"strings"

	"agent-host/internal/state"
)

// Resource references can name a second target without using "project" or a
// conjunction. Never let an exact match for one project hide that uncertainty.
// This guard only requests semantic evaluation; it does not select any project.
func hasUnmappedResourceReference(text string, projects []state.Project) bool {
	matched := map[string]bool{}
	for _, match := range explicitMatches(text, projects) {
		matched[match.id] = true
	}
	for _, location := range resourceReferences.FindAllStringIndex(text, -1) {
		reference := strings.TrimRight(text[location[0]:location[1]], ".,;:!?)}")
		if reference == "" || ignoredBareReference(text, location, reference) {
			continue
		}
		covered := false
		for _, project := range projects {
			// Catalog knowledge alone cannot add a target to the exact result.
			// A hostname inside a URL/path may fail the exact word boundaries;
			// semantic routing must then establish its project's participation.
			if !matched[project.ID] {
				continue
			}
			for _, alias := range append([]string{project.ID, project.Name}, project.Aliases...) {
				if resourceAliasCovers(reference, alias) {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			return true
		}
	}
	return false
}

var resourceReferences = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|git@)[^\s<>"',;，。；！？（）\x60]+|(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*(?::[0-9]+)?(?:/[^\s<>"',;，。；！？（）\x60]+)?|(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?::[0-9]+)?(?:/[^\s<>"',;，。；！？（）\x60]+)?|\[[0-9a-f:]+\](?::[0-9]+)?(?:/[^\s<>"',;，。；！？（）\x60]+)?|localhost:[0-9]+(?:/[^\s<>"',;，。；！？（）\x60]+)?`)

func ignoredBareReference(text string, location []int, reference string) bool {
	if strings.Contains(reference, "://") || strings.HasPrefix(strings.ToLower(reference), "git@") {
		return false
	}
	// An email address is a recipient, not a repository/service target. Paths
	// and dotted identifiers with common source/config suffixes are not hosts.
	if location[0] > 0 && text[location[0]-1] == '@' {
		return true
	}
	if strings.ContainsAny(reference, ":/") {
		return false
	}
	if ip := net.ParseIP(reference); ip != nil {
		return false
	}
	suffix := strings.ToLower(reference[strings.LastIndexByte(reference, '.')+1:])
	switch suffix {
	case "go", "rs", "py", "js", "jsx", "ts", "tsx", "java", "c", "h", "cpp", "hpp", "cs", "rb", "sh", "sql", "md", "txt", "json", "jsonl", "yaml", "yml", "toml", "xml", "html", "css", "scss", "lock", "sum", "mod", "ini", "cfg", "conf", "log", "png", "jpg", "jpeg", "svg", "pdf", "csv":
		return true
	}
	return false
}

func resourceAliasCovers(reference, alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return false
	}
	ref, ok := parseResourceReference(reference)
	if !ok {
		return false
	}
	registered, ok := parseResourceReference(alias)
	if !ok || !strings.EqualFold(ref.host, registered.host) {
		return false
	}
	// A registered hostname names that service at every path. A repository or
	// URL alias covers only its complete path, never sibling repositories or a
	// prefix such as billing-tools. Ports remain part of the service identity.
	return registered.path == "" || ref.path == registered.path
}

type resourceReference struct {
	host string
	path string
}

func parseResourceReference(raw string) (resourceReference, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".")
	explicitURL := strings.Contains(raw, "://")
	if strings.HasPrefix(strings.ToLower(raw), "git@") {
		remote := raw[4:]
		if colon := strings.IndexByte(remote, ':'); colon >= 0 {
			raw = "ssh://" + remote[:colon] + "/" + remote[colon+1:]
		} else {
			return resourceReference{}, false
		}
		explicitURL = true
	}
	if !explicitURL {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return resourceReference{}, false
	}
	if !explicitURL && !strings.Contains(u.Hostname(), ".") && net.ParseIP(u.Hostname()) == nil && !(strings.EqualFold(u.Hostname(), "localhost") && u.Port() != "") {
		return resourceReference{}, false
	}
	return resourceReference{host: strings.ToLower(u.Host), path: strings.TrimSuffix(strings.TrimRight(u.EscapedPath(), "/"), ".git")}, true
}
