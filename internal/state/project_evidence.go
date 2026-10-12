package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EvidenceEntitiesForRun attributes typed file paths to the original selected
// projects. Selection alone is not evidence that a file belongs to a project.
// Unknown paths stay in the original tool input without a derived entity.
func EvidenceEntitiesForRun(run Run, draft ExperienceDraft, evidence []ToolEvidence) ([]string, error) {
	if len(run.Projects) == 0 {
		return EvidenceEntitiesAt(run.Scope, run.WorkingDirectory(), draft, evidence)
	}
	if err := validateDraft(draft, evidence); err != nil {
		return nil, err
	}
	roots := append([]ProjectRef(nil), run.Projects...)
	available := map[string]bool{}
	for i := range roots {
		if roots[i].ID == "" || !filepath.IsAbs(roots[i].Workspace) {
			return nil, errors.New("evidence project requires an ID and absolute workspace")
		}
		// These are execution snapshots, not paths resolved against today's
		// project registry or a replacement symlink at the old checkout.
		roots[i].Workspace = filepath.Clean(roots[i].Workspace)
		actual, err := filepath.EvalSymlinks(roots[i].Workspace)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && actual == roots[i].Workspace {
			info, err := os.Stat(actual)
			if err != nil {
				return nil, err
			}
			available[roots[i].ID] = info.IsDir()
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i].Workspace) != len(roots[j].Workspace) {
			return len(roots[i].Workspace) > len(roots[j].Workspace)
		}
		return roots[i].ID < roots[j].ID
	})
	workspaces := map[string]string{}
	for _, project := range roots {
		if id, exists := workspaces[project.Workspace]; exists && id != project.ID {
			return nil, errors.New("evidence projects have an ambiguous workspace")
		}
		workspaces[project.Workspace] = project.ID
	}
	owner := func(path string) string {
		for _, project := range roots {
			relative, err := filepath.Rel(project.Workspace, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return project.ID
			}
		}
		return ""
	}
	selected := map[string]bool{}
	for _, id := range draft.EvidenceIDs {
		selected[id] = true
	}
	entities := map[string]bool{}
	for _, item := range evidence {
		if !selected[item.ID] {
			continue
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal(item.Input, &input); err != nil {
			return nil, fmt.Errorf("tool input %s must be an object: %w", item.ID, err)
		}
		cwd := run.WorkingDirectory()
		explicitCWD := false
		for _, key := range []string{"cwd", "workdir"} {
			if raw, exists := input[key]; exists {
				var explicit string
				if json.Unmarshal(raw, &explicit) != nil || !filepath.IsAbs(explicit) {
					cwd = ""
					break
				}
				if explicitCWD && cwd != explicit {
					cwd = ""
					break
				}
				cwd = explicit
				explicitCWD = true
			}
		}
		if id := owner(cwd); id != "" && !available[id] {
			cwd = ""
		}
		if filepath.IsAbs(cwd) {
			var err error
			cwd, err = evidenceCanonicalPath(cwd)
			if err != nil {
				return nil, err
			}
		}
		// Shell strings can change cwd; never infer their execution directory.
		_, shellCommand := input["command"]
		for _, key := range []string{"file_path", "filePath", "path"} {
			var path string
			if raw, exists := input[key]; !exists || json.Unmarshal(raw, &path) != nil || strings.TrimSpace(path) == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				if shellCommand || !filepath.IsAbs(cwd) || owner(cwd) == "" {
					continue
				}
				path = cwd + string(filepath.Separator) + path
			}
			// A checkout replaced by a symlink cannot reattribute historical
			// calls to that symlink's current target, even another selected root.
			if id := owner(path); id != "" && !available[id] {
				continue
			}
			path, err := evidenceCanonicalPath(path)
			if err != nil {
				return nil, err
			}
			if projectID := owner(path); projectID != "" && available[projectID] {
				entities["project:"+projectID] = true
				entities["file:"+path] = true
			}
		}
	}
	out := make([]string, 0, len(entities))
	for entity := range entities {
		out = append(out, entity)
	}
	sort.Strings(out)
	return out, nil
}

// Resolve existing symlinks even when the final file has not been created yet.
func evidenceCanonicalPath(path string) (string, error) {
	return resolveEvidencePath(path, 0)
}

func resolveEvidencePath(path string, links int) (string, error) {
	if links > 255 {
		return "", errors.New("too many symlinks in evidence path")
	}
	if actual, err := filepath.EvalSymlinks(path); err == nil {
		return actual, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Cleaning '..' before resolving a symlink can change its filesystem
	// meaning. If the target is absent, retain that uncertain path as raw data.
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return "", nil
		}
	}
	path = filepath.Clean(path)
	ancestor := path
	var suffix []string
	for {
		actual, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				actual = filepath.Join(actual, suffix[i])
			}
			return actual, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		info, statErr := os.Lstat(ancestor)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(ancestor)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(ancestor), target)
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				target = filepath.Join(target, suffix[i])
			}
			return resolveEvidencePath(target, links+1)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}
