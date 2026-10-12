package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

func sameProjectRefs(a, b []ProjectRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Scope != b[i].Scope || a[i].Workspace != b[i].Workspace {
			return false
		}
	}
	return true
}

func sameDirectories(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateChannelProjects(ctx context.Context, tx *sql.Tx, projects []ProjectRef, workspace string, additional []string) error {
	seenIDs, expected := map[string]bool{}, map[string]bool{}
	for _, ref := range projects {
		if ref.ID == "" || seenIDs[ref.ID] {
			return errors.New("resolved project references must have distinct registered IDs")
		}
		project, err := readProject(ctx, tx, ref.ID)
		if err != nil {
			return err
		}
		if ref.Scope != project.Scope || ref.Workspace != project.Workspace {
			return errors.New("resolved project reference does not match registration")
		}
		actual, err := CanonicalWorkspace(ref.Workspace)
		if err != nil {
			return err
		}
		if actual != ref.Workspace {
			return errors.New("resolved project directory changed its canonical path")
		}
		seenIDs[ref.ID] = true
		if ref.Workspace != workspace {
			expected[ref.Workspace] = true
		}
	}
	seenDirs := map[string]bool{}
	for _, directory := range additional {
		if !expected[directory] || seenDirs[directory] {
			return errors.New("additional directories must match the selected project roots")
		}
		seenDirs[directory] = true
	}
	if len(seenDirs) != len(expected) {
		return errors.New("selected project directory is missing from execution environment")
	}
	return nil
}

// ChannelRouteProjects isolates a project set while recognizing cwd/config
// changes within that set. Metadata renames do not rotate native conversations.
func (s *Store) ChannelRouteProjects(ctx context.Context, binding string, ids []string) (ChannelMessage, error) {
	wanted := append([]string(nil), ids...)
	sort.Strings(wanted)
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM channel_messages WHERE json_extract(payload,'$.binding')=? AND COALESCE(json_extract(payload,'$.route_binding'),'')<>'' ORDER BY route_order DESC,received_at DESC,rowid DESC", binding)
	if err != nil {
		return ChannelMessage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return ChannelMessage{}, err
		}
		var message ChannelMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return ChannelMessage{}, err
		}
		var actual []string
		for _, project := range message.Projects {
			actual = append(actual, project.ID)
		}
		if len(actual) == 0 && message.ProjectID != "" {
			actual = append(actual, message.ProjectID)
		}
		sort.Strings(actual)
		if sameDirectories(actual, wanted) {
			return message, nil
		}
	}
	if err := rows.Err(); err != nil {
		return ChannelMessage{}, err
	}
	return ChannelMessage{}, sql.ErrNoRows
}
