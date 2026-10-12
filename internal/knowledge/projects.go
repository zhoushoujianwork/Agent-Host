package knowledge

import (
	"slices"
	"sort"
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
	c, err := Load(path)
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
	for _, source := range c.Sources {
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
