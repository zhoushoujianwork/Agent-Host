package knowledge

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"agent-host/internal/componentconfig"
)

// SelectProjects resolves the union of explicitly authorized sources once.
// A neutral cwd in a joint run does not add the default project's permissions.
func SelectProjects(path string, ids []string) ([]ResolvedSource, error) {
	return SelectAgentProjects(path, ids, nil)
}

// SelectAgentProjects narrows project-authorized sources before opening their
// credentials. An excluded source cannot block an unrelated instance.
func SelectAgentProjects(path string, ids, names []string) ([]ResolvedSource, error) {
	return SelectAgentAudienceProjects(path, ids, names, "owner")
}

// SelectAgentAudienceProjects applies all access selectors before opening a
// credential or inspecting a local root. An omitted audience means local owner.
func SelectAgentAudienceProjects(path string, ids, names []string, audience string) ([]ResolvedSource, error) {
	if path == "" {
		return nil, nil
	}
	return SelectMany([]string{path}, ids, names, audience, "")
}

func Catalog(paths []string, channel string) ([]Source, error) {
	var sources []Source
	seen := map[string]bool{}
	for _, path := range paths {
		if componentconfig.DataSourceID(path) != "" && channel != "" {
			raw, err := readPrivate(path, 64<<10)
			if err != nil {
				return nil, err
			}
			e, err := componentconfig.Decode(raw)
			if err != nil {
				return nil, err
			}
			if !e.Allows(channel) {
				continue
			}
		}
		c, err := Load(path)
		if err != nil {
			return nil, err
		}
		for _, source := range c.Sources {
			if seen[source.Name] {
				return nil, errors.New("duplicate datasource source identity")
			}
			seen[source.Name] = true
			sources = append(sources, source)
		}
	}
	return sources, nil
}

// ResolveNames allows a legacy short reference only when its meaning is unique.
func ResolveNames(sources []Source, names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		var matches []string
		for _, source := range sources {
			if source.Name == name || !strings.Contains(name, ":") && strings.HasSuffix(source.Name, ":"+name) {
				matches = append(matches, source.Name)
			}
		}
		if len(matches) != 1 {
			return nil, errors.New("knowledge source reference is unavailable or ambiguous; use plugin:source")
		}
		resolved = append(resolved, matches[0])
	}
	return resolved, nil
}

func SelectMany(paths, ids, names []string, audience, channel string) ([]ResolvedSource, error) {
	sources, err := Catalog(paths, channel)
	if err != nil {
		return nil, err
	}
	names, err = ResolveNames(sources, names)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		ids = []string{"default"}
	}
	if audience == "" {
		audience = "owner"
	}
	selectedIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		selectedIDs[id] = true
	}
	var selected []ResolvedSource
	for _, source := range sources {
		if names != nil && !slices.Contains(names, source.Name) {
			continue
		}
		if source.Audiences != nil && !slices.Contains(source.Audiences, audience) {
			continue
		}
		for _, id := range source.Projects {
			if selectedIDs[id] {
				r, err := resolve(source)
				if err != nil {
					return nil, err
				}
				selected = append(selected, r)
				break
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Source.Name < selected[j].Source.Name })
	return selected, nil
}
