package assembly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"agent-host/internal/state"
)

// Runtime separates durable knowledge identity from a concrete native cwd.
type Runtime struct {
	AgentID               string             `json:"agent_id"`
	AudienceID            string             `json:"audience_id"`
	NativeProfile         string             `json:"native_profile"`
	PermissionMode        string             `json:"permission_mode,omitempty"`
	HostInstructions      string             `json:"-"`
	Projects              []state.ProjectRef `json:"projects,omitempty"`
	AdditionalDirectories []string           `json:"additional_directories,omitempty"`
	ProjectID             string             `json:"project_id,omitempty"`
	ProjectName           string             `json:"project_name"`
	Workspace             string             `json:"workspace"`
	Scope                 string             `json:"scope"`
	Fingerprint           string             `json:"runtime_fingerprint"`
}

type Configuration struct {
	AgentID, AudienceID          string
	PermissionMode               string
	HostInstructions             string
	KnowledgeFingerprint         string
	ConversationHistoryPolicy    string `json:",omitempty"`
	Binary, Model, NativeProfile string
	ToolsDisabled                bool
	AllowedTools                 []string
}

// Assemble verifies the selected directory before execution. The fingerprint
// describes host-controlled native configuration, not the contents of the repo.
func Assemble(projectID, projectName, scope, workspace string, c Configuration) (Runtime, error) {
	if projectID == "" {
		r, err := AssembleProjects(nil, "", scope, workspace, c)
		if projectName != "" {
			r.ProjectName = projectName
		}
		return r, err
	}
	return AssembleProjects([]state.ProjectRef{{ID: projectID, Name: projectName, Scope: scope, Workspace: workspace}}, projectID, scope, workspace, c)
}

// AssembleProjects selects one primary cwd while retaining every project's
// separate memory identity. A symmetric multi-project task uses the default cwd.
func AssembleProjects(projects []state.ProjectRef, primaryID, defaultScope, defaultWorkspace string, c Configuration) (Runtime, error) {
	if c.AgentID == "" {
		c.AgentID = state.DefaultAgentID
	}
	if c.AudienceID == "" {
		c.AudienceID = state.OwnerAudienceID
	}
	r := Runtime{AgentID: c.AgentID, AudienceID: c.AudienceID, PermissionMode: c.PermissionMode, HostInstructions: c.HostInstructions, Scope: defaultScope, Workspace: defaultWorkspace, ProjectName: "Default workspace"}
	if c.PermissionMode != "" && c.PermissionMode != "default" && c.PermissionMode != "bypassPermissions" {
		return r, errors.New("runtime permission mode must be default or bypassPermissions")
	}
	byID := make(map[string]state.ProjectRef, len(projects))
	for _, project := range projects {
		if project.ID == "" || project.Scope == "" {
			return r, errors.New("runtime project requires an ID and memory scope")
		}
		if err := validateWorkspace(project.Workspace); err != nil {
			return r, fmt.Errorf("project %s: %w", project.ID, err)
		}
		if previous, exists := byID[project.ID]; exists && previous != project {
			return r, fmt.Errorf("project %s has conflicting runtime definitions", project.ID)
		}
		byID[project.ID] = project
	}
	for _, project := range byID {
		r.Projects = append(r.Projects, project)
	}
	sort.Slice(r.Projects, func(i, j int) bool { return r.Projects[i].ID < r.Projects[j].ID })
	if primaryID == "" && len(r.Projects) == 1 {
		primaryID = r.Projects[0].ID
	}
	if primaryID != "" {
		primary, exists := byID[primaryID]
		if !exists {
			return r, errors.New("primary project is not in the selected project set")
		}
		r.ProjectID, r.ProjectName, r.Workspace, r.Scope = primary.ID, primary.Name, primary.Workspace, primary.Scope
	} else if len(r.Projects) > 1 {
		r.ProjectName = "Multiple projects"
	}
	if r.Scope == "" {
		return r, errors.New("runtime requires a memory scope")
	}
	if err := validateWorkspace(r.Workspace); err != nil {
		return r, err
	}
	if r.ProjectName == "" {
		r.ProjectName = r.ProjectID
	}
	directories := map[string]bool{r.Workspace: true}
	for _, project := range r.Projects {
		if !directories[project.Workspace] {
			r.AdditionalDirectories = append(r.AdditionalDirectories, project.Workspace)
			directories[project.Workspace] = true
		}
	}
	sort.Strings(r.AdditionalDirectories)
	nativeSettings, err := fingerprintNativeSettings(r.Workspace, c.NativeProfile)
	if err != nil {
		return r, err
	}
	c.NativeProfile = nativeSettings.Profile
	r.NativeProfile = nativeSettings.Profile
	// Project names are presentation data. Identity, scope, cwd and the complete
	// access set must all agree before a native conversation can be resumed.
	identities := append([]state.ProjectRef(nil), r.Projects...)
	for i := range identities {
		identities[i].Name = ""
	}
	payload, err := json.Marshal(struct {
		Policy string
		Configuration
		NativeSettings              nativeSettingsFingerprint
		Projects                    []state.ProjectRef
		PrimaryID, Workspace, Scope string
		AdditionalDirectories       []string
	}{"runtime-assembly-v6:agent-audience:host-instructions:explicit-native-policy", c, nativeSettings, identities, r.ProjectID, r.Workspace, r.Scope, r.AdditionalDirectories})
	if err != nil {
		return r, err
	}
	digest := sha256.Sum256(payload)
	r.Fingerprint = hex.EncodeToString(digest[:])
	return r, nil
}

func validateWorkspace(workspace string) error {
	if !filepath.IsAbs(workspace) {
		return errors.New("runtime workspace must be an absolute path")
	}
	actual, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("selected workspace: %w", err)
	}
	info, err := os.Stat(actual)
	if err != nil || !info.IsDir() {
		return errors.New("selected workspace must be an existing directory")
	}
	if actual != workspace {
		return errors.New("selected workspace changed its canonical path; update the project locally")
	}
	return nil
}

// Shared and loaded project settings participate. Authentication files and
// native conversation history do not rotate an otherwise compatible run.
type nativeSettingsFingerprint struct {
	Profile string
	Files   []nativeSettingsFileFingerprint
}

type nativeSettingsFileFingerprint struct {
	Path    string
	Present bool
	SHA256  string
}

func fingerprintNativeSettings(workspace, profile string) (nativeSettingsFingerprint, error) {
	var out nativeSettingsFingerprint
	if profile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return out, fmt.Errorf("resolve native profile: %w", err)
		}
		profile = filepath.Join(home, ".claude")
	} else if !filepath.IsAbs(profile) {
		profile = filepath.Join(workspace, profile)
	}
	profile, err := filepath.Abs(profile)
	if err != nil {
		return out, fmt.Errorf("resolve native profile: %w", err)
	}
	// Resolve the existing parent of an absent profile too. Creating that profile
	// for authentication/session files must not change an otherwise identical path.
	for ancestor := profile; ; ancestor = filepath.Dir(ancestor) {
		actual, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			suffix, err := filepath.Rel(ancestor, profile)
			if err != nil {
				return out, fmt.Errorf("resolve native profile: %w", err)
			}
			profile = filepath.Join(actual, suffix)
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return out, fmt.Errorf("resolve native profile: %w", err)
		}
		if filepath.Dir(ancestor) == ancestor {
			return out, fmt.Errorf("resolve native profile: %w", err)
		}
	}
	if info, err := os.Stat(profile); err == nil {
		if !info.IsDir() {
			return out, errors.New("native profile must be a directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, fmt.Errorf("inspect native profile: %w", err)
	}
	out.Profile = profile
	localRoot, err := nativeLocalSettingsRoot(workspace)
	if err != nil {
		return out, fmt.Errorf("resolve native settings: %w", err)
	}
	paths := []string{filepath.Join(profile, "settings.json"), filepath.Join(workspace, ".claude", "settings.json"), filepath.Join(workspace, ".claude", "settings.local.json")}
	if localRoot != workspace {
		paths = append(paths, filepath.Join(localRoot, ".claude", "settings.local.json"))
	}
	sort.Strings(paths)
	for i, path := range paths {
		if i > 0 && path == paths[i-1] {
			continue
		}
		file, err := fingerprintNativeSettingsFile(path)
		if err != nil {
			return out, err
		}
		out.Files = append(out.Files, file)
	}
	return out, nil
}

func fingerprintNativeSettingsFile(path string) (nativeSettingsFileFingerprint, error) {
	out := nativeSettingsFileFingerprint{Path: path}
	// A missing directory is normal, but a broken directory link is not absent
	// configuration. Check each existing ancestor before looking up the file.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		entry, err := os.Lstat(parent)
		if err == nil {
			if entry.Mode()&os.ModeSymlink != 0 {
				entry, err = os.Stat(parent)
			}
			if err != nil {
				return out, fmt.Errorf("inspect native settings directory %s: %w", parent, err)
			}
			if !entry.IsDir() {
				return out, fmt.Errorf("native settings directory %s must be a directory", parent)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return out, fmt.Errorf("inspect native settings directory %s: %w", parent, err)
		}
	}
	// Lstat distinguishes absent configuration from a configured broken symlink.
	if _, err := os.Lstat(out.Path); errors.Is(err, os.ErrNotExist) {
		return out, nil
	} else if err != nil {
		return out, fmt.Errorf("inspect native settings %s: %w", path, err)
	}
	info, err := os.Stat(out.Path)
	if err != nil {
		return out, fmt.Errorf("inspect native settings %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return out, fmt.Errorf("native settings %s must be a regular file", path)
	}
	file, err := os.Open(out.Path)
	if err != nil {
		return out, fmt.Errorf("read native settings %s: %w", path, err)
	}
	info, err = file.Stat()
	if err != nil {
		return out, errors.Join(fmt.Errorf("inspect opened native settings: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return out, errors.Join(fmt.Errorf("native settings %s must be a regular file", path), file.Close())
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, file)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return out, fmt.Errorf("read native settings %s: %w", path, err)
	}
	out.Present, out.SHA256 = true, hex.EncodeToString(digest.Sum(nil))
	return out, nil
}
