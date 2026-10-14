// Package projectruntime retains shared local project identities and execution
// receipts. Discovery is metadata-only and does not enroll candidates.
package projectruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-host/internal/state"
)

const (
	MaxDiscoveryDirectories = 2048
	MaxDiscoveryDepth       = 3
)

type Catalog struct {
	Root        string
	LookupRoots []string
}

type Origin struct {
	Entry      string `json:"entry"`
	StateHome  string `json:"state_home,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	AudienceID string `json:"audience_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
}

// RootForState keeps the ordinary CLI and channel state homes in one registry.
// An arbitrary custom state home remains an independent runtime.
func RootForState(home string) string {
	home = filepath.Clean(home)
	if filepath.Base(home) == "cli-state" || filepath.Base(home) == "state" && filepath.Base(filepath.Dir(filepath.Dir(home))) != "channels" {
		return filepath.Dir(home)
	}
	if filepath.Base(home) == "state" && filepath.Base(filepath.Dir(filepath.Dir(home))) == "channels" {
		return filepath.Dir(filepath.Dir(filepath.Dir(home)))
	}
	return home
}

func New(root string) *Catalog {
	c := &Catalog{Root: root}
	if home, err := os.UserHomeDir(); err == nil {
		c.LookupRoots = []string{filepath.Join(home, "github"), filepath.Join(home, "gitlab")}
	}
	return c
}

type checkout struct{ workspace, identity string }

// inspectCheckout does not read repository contents or remote configuration.
func inspectCheckout(ctx context.Context, directory string) (checkout, error) {
	canonical, err := state.CanonicalWorkspace(directory)
	if err != nil {
		return checkout{}, err
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, "git", "-C", canonical, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir")
	cmd.Env = gitEnvironment()
	output, err := cmd.Output()
	if err != nil {
		return checkout{}, fmt.Errorf("inspect local Git checkout %q: %w", canonical, err)
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(parts) != 2 {
		return checkout{}, fmt.Errorf("unexpected Git identity for %q", canonical)
	}
	workspace, err := state.CanonicalWorkspace(parts[0])
	if err != nil {
		return checkout{}, err
	}
	identity, err := filepath.EvalSymlinks(parts[1])
	if err != nil {
		return checkout{}, err
	}
	return checkout{workspace, filepath.Clean(identity)}, nil
}

func gitEnvironment() []string {
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func localID(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return "local-" + hex.EncodeToString(sum[:12])
}

func hasGitMarker(directory string) (bool, error) {
	_, err := os.Lstat(filepath.Join(directory, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Candidates enumerates bounded checkout roots, never files within a checkout.
// Limit exhaustion is an error rather than an incomplete routing catalog.
func (c *Catalog) Candidates(ctx context.Context, workspace string, known []state.Project) ([]state.Project, error) {
	shared, err := c.Projects(ctx)
	if err != nil {
		return nil, err
	}
	known = append(append([]state.Project(nil), known...), shared...)
	byIdentity := make(map[string]state.Project)
	for _, p := range known {
		co, err := inspectCheckout(ctx, p.Workspace)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // A stale registered path is retained by its owning state store.
		}
		if old, exists := byIdentity[co.identity]; exists && (old.ID != p.ID || old.Scope != p.Scope) {
			return nil, fmt.Errorf("Git checkout has conflicting registered identities %q and %q", old.ID, p.ID)
		}
		byIdentity[co.identity] = p
	}
	found := make(map[string]state.Project)
	seen := make(map[string]int)
	count := 0
	var visit func(string, int, int) error
	visit = func(path string, depth, maxDepth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		canonical, err := state.CanonicalWorkspace(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		remaining := maxDepth - depth
		previous, exists := seen[canonical]
		if exists && previous >= remaining {
			return nil
		}
		seen[canonical] = remaining
		if !exists {
			count++
		}
		if count > MaxDiscoveryDirectories {
			return fmt.Errorf("local project discovery exceeds %d directories", MaxDiscoveryDirectories)
		}
		marker, err := hasGitMarker(canonical)
		if err != nil {
			return err
		}
		if marker {
			co, err := inspectCheckout(ctx, canonical)
			if err != nil {
				return err
			}
			if _, exists := found[co.identity]; exists {
				return nil
			}
			p, exists := byIdentity[co.identity]
			if !exists {
				id := localID(co.identity)
				p = state.Project{ID: id, Name: filepath.Base(co.workspace), Workspace: co.workspace, Scope: "project:" + id}
			}
			// Identity and memory scope belong to the repository. Execution cwd
			// belongs to the checkout selected for this request. The workspace
			// probe runs first, before sibling inventory can find the same repo.
			p.Workspace = co.workspace
			found[co.identity] = p
			return nil
		}
		entries, err := os.ReadDir(canonical)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || ignoredDirectory(entry.Name()) {
				continue
			}
			if depth == maxDepth {
				if maxDepth == MaxDiscoveryDepth {
					// A non-Git source export can contain arbitrarily deep source
					// directories. That is not a missing checkout. At the search
					// boundary probe only direct Git markers, without descending.
					child := filepath.Join(canonical, entry.Name())
					if _, seenChild := seen[child]; !seenChild {
						seen[child] = -1
						count++
						if count > MaxDiscoveryDirectories {
							return fmt.Errorf("local project discovery exceeds %d directories", MaxDiscoveryDirectories)
						}
					}
					marker, err := hasGitMarker(child)
					if err != nil {
						return err
					}
					if marker {
						return fmt.Errorf("local project discovery exceeds depth %d at checkout %q", maxDepth, child)
					}
				}
				continue
			}
			if err := visit(filepath.Join(canonical, entry.Name()), depth+1, maxDepth); err != nil {
				return err
			}
		}
		return nil
	}
	// A nested execution cwd can resolve its enclosing checkout without scanning
	// siblings, and a non-checkout workspace is inspected one directory deep.
	if workspace != "" {
		canonical, err := state.CanonicalWorkspace(workspace)
		if err != nil {
			return nil, err
		}
		for ancestor := canonical; ; ancestor = filepath.Dir(ancestor) {
			marker, err := hasGitMarker(ancestor)
			if err != nil {
				return nil, err
			}
			if marker {
				canonical = ancestor
				break
			}
			if filepath.Dir(ancestor) == ancestor {
				break
			}
		}
		if err := visit(canonical, 0, 1); err != nil {
			return nil, err
		}
	}
	for _, root := range c.LookupRoots {
		if err := visit(root, 0, MaxDiscoveryDepth); err != nil {
			return nil, err
		}
	}
	// Registered checkouts outside default lookup roots remain route candidates.
	for identity, p := range byIdentity {
		if _, exists := found[identity]; !exists {
			found[identity] = p
		}
	}
	projects := make([]state.Project, 0, len(found))
	for _, p := range found {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return projects, nil
}

func ignoredDirectory(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__":
		return true
	}
	return false
}
