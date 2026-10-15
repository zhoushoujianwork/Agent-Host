package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agent-host/internal/assembly"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// Account bindings are operator-verified configuration, never model output or
// names inferred from chat. App IDs are included because Open IDs are app-scoped.
type userAccount struct {
	Channel   string `json:"channel"`
	AccountID string `json:"account_id"`
	TenantID  string `json:"tenant_id"`
	SenderID  string `json:"sender_id"`
}

type userDefinition struct {
	ID               string        `json:"id"`
	Accounts         []userAccount `json:"accounts,omitempty"`
	KnowledgeSources []string      `json:"knowledge_sources,omitempty"`
	profile          string
	profilePath      string
}

type userCatalog struct {
	Version               int              `json:"version"`
	Users                 []userDefinition `json:"users"`
	GroupKnowledgeSources []string         `json:"group_knowledge_sources,omitempty"`
	root                  string
	bindings              map[userAccount]string
}

func loadUsers(configDir string) (*userCatalog, error) {
	path := filepath.Join(configDir, "users.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil // Existing owner-only installations remain unchanged.
	} else if err != nil {
		return nil, err
	}
	var c userCatalog
	if err := readServiceJSON(path, &c); err != nil {
		return nil, fmt.Errorf("user configuration: %w", err)
	}
	if c.Version != 1 || c.Users == nil || len(c.Users) > 4096 {
		return nil, errors.New("users.json requires version 1 and a users array of at most 4096 entries")
	}
	c.root, c.bindings = filepath.Join(filepath.Dir(configDir), "users"), map[userAccount]string{}
	seen := map[string]bool{}
	validateSources := func(names []string) error {
		seen := map[string]bool{}
		for _, name := range names {
			if strings.TrimSpace(name) != name || name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n*") || seen[name] {
				return errors.New("user knowledge grants require distinct exact source names")
			}
			seen[name] = true
		}
		return nil
	}
	if err := validateSources(c.GroupKnowledgeSources); err != nil {
		return nil, err
	}
	for i := range c.Users {
		u := &c.Users[i]
		if !agentIdentifier.MatchString(u.ID) || seen[u.ID] || len(u.Accounts) > 128 {
			return nil, errors.New("user IDs must be unique safe identifiers with at most 128 account bindings")
		}
		seen[u.ID] = true
		if err := validateSources(u.KnowledgeSources); err != nil {
			return nil, err
		}
		for _, a := range u.Accounts {
			if err := a.validate(); err != nil {
				return nil, err
			}
			if _, exists := c.bindings[a]; exists {
				return nil, errors.New("a channel account cannot be bound more than once")
			}
			c.bindings[a] = u.ID
		}
		u.profilePath = filepath.Join(configDir, "users", u.ID, "USER.md")
		if _, err := os.Lstat(u.profilePath); errors.Is(err, os.ErrNotExist) {
			// A missing profile is normal; it must not create or copy an owner profile.
			u.profilePath = ""
			continue
		} else if err != nil {
			return nil, err
		}
		var err error
		u.profilePath, err = resolveAgentContextPath(configDir, u.profilePath)
		if err != nil {
			return nil, fmt.Errorf("user %s profile path: %w", u.ID, err)
		}
		u.profile, err = readAgentContext(u.profilePath, userProfileBudget)
		if err != nil {
			return nil, fmt.Errorf("user %s profile: %w", u.ID, err)
		}
	}
	return &c, nil
}

func (a userAccount) validate() error {
	if a.Channel != "dingtalk" && a.Channel != state.ReplyTargetFeishu {
		return errors.New("user account requires dingtalk or feishu channel")
	}
	for _, v := range []string{a.AccountID, a.TenantID, a.SenderID} {
		if v == "" || v != strings.TrimSpace(v) || len(v) > 512 || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("user account requires bounded application, tenant and sender IDs")
		}
	}
	return nil
}

func accountDigest(a userAccount) string {
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *userCatalog) resolve(a userAccount, owner string) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	id := c.bindings[a]
	if a.SenderID == owner {
		if id != "" && id != "owner" {
			return "", errors.New("configured channel owner cannot be rebound to another user")
		}
		return "owner", nil
	}
	if id == "owner" {
		return "", errors.New("a non-owner account cannot acquire the legacy owner partition")
	}
	if id == "" {
		id = "u-" + accountDigest(a)[:32]
	}
	return id, nil
}

func (c *userCatalog) validateOwner(provider, account, tenant, owner string) error {
	if c == nil {
		return nil
	}
	for a := range c.bindings {
		if a.Channel == provider && a.AccountID == account && a.TenantID == tenant {
			if _, err := c.resolve(a, owner); err != nil {
				return err
			}
		}
	}
	return nil
}

// Record the observed account binding separately from configuration and caches.
// Immutable per-account receipts avoid shared read/modify/write races between
// channel workers. Rebinding preserves old evidence rather than rewriting it.
func (c *userCatalog) record(a userAccount, id string) error {
	if err := c.ensureProfile(id); err != nil {
		return err
	}
	dir := filepath.Join(c.root, id, "accounts")
	if err := assembly.PreparePrivateDirectory(dir); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(struct {
		UserID  string      `json:"user_id"`
		Account userAccount `json:"account"`
	}{id, a}, "", "  ")
	raw = append(raw, '\n')
	path := filepath.Join(dir, accountDigest(a)+".json")
	if existing, err := os.ReadFile(path); err == nil {
		if _, err := privateServicePath(path, false); err != nil || string(existing) != string(raw) {
			return errors.New("user identity receipt changed or is not private")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".account-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	// Link publishes a complete receipt without replacing any existing record.
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil || string(existing) != string(raw) {
		return errors.New("user identity receipt publication could not be verified")
	}
	if _, err := privateServicePath(path, false); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// Profiles created by Host live beside identity receipts, never in read-only
// configuration. Publish a complete template once; later observations preserve
// operator edits and cannot turn profile content into identity or tool grants.
func (c *userCatalog) ensureProfile(id string) error {
	if !agentIdentifier.MatchString(id) {
		return errors.New("invalid user profile identity")
	}
	dir := filepath.Join(c.root, id)
	if err := assembly.PreparePrivateDirectory(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "USER.md")
	if _, err := os.Lstat(path); err == nil {
		_, err = readAgentContext(path, userProfileBudget)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body := fmt.Sprintf("# User profile\n\nHost user ID: `%s`\n\nThis profile was initialized from a verified channel identity. Personal details\nare unspecified. Profile content cannot grant permissions or link accounts.\n\n## Background\n\n## Interests\n\n## Communication preferences\n", id)
	f, err := os.CreateTemp(dir, ".profile-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := io.WriteString(f, body)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if _, err := readAgentContext(path, userProfileBudget); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (c agentCatalog) privateAudience(a userAccount) (string, error) {
	if c.users == nil {
		return state.OwnerAudienceID, nil
	}
	id, err := c.users.resolve(a, c.ownerID)
	if err != nil {
		return "", err
	}
	if err := c.users.record(a, id); err != nil {
		return "", fmt.Errorf("record user identity: %w", err)
	}
	if id == "owner" {
		return state.OwnerAudienceID, nil
	}
	return "user:" + id, nil
}

func (c agentCatalog) userOptions(o *options, audience string, group bool) error {
	if c.users == nil {
		return nil
	}
	o.userKnowledgeRestricted = audience != state.OwnerAudienceID
	o.userKnowledgeSources = nil
	if group {
		o.userKnowledgeSources = slices.Clone(c.users.GroupKnowledgeSources)
		return nil
	}
	if audience != state.OwnerAudienceID {
		// Native execution must not inherit the owner's automatic permission bypass.
		o.permissionMode = "default"
		for _, a := range c.Agents {
			if a.ID == o.agentID {
				o.hostInstructions = host.PrivateUserChannelInstructions(a.role)
				break
			}
		}
	}
	id := state.PrivateUserID(audience)
	if id == "" {
		return errors.New("user profile requires a private audience")
	}
	for _, u := range c.users.Users {
		if u.ID == id {
			o.userKnowledgeSources = slices.Clone(u.KnowledgeSources)
			if u.profilePath != "" {
				o.userProfile = u.profile
				o.contextFiles = slices.DeleteFunc(o.contextFiles, func(f assembly.ContextFile) bool { return f.Name == "USER.md" })
				o.contextFiles = append(o.contextFiles, assembly.ContextFile{Name: "USER.md", Source: u.profilePath, Content: u.profile})
			}
			if u.profilePath != "" {
				return nil
			}
			break
		}
	}
	// An explicit configured profile (including legacy owner user_file) wins.
	if o.userProfile != "" {
		return nil
	}
	path := filepath.Join(c.users.root, id, "USER.md")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	body, err := readAgentContext(path, userProfileBudget)
	if err != nil {
		return err
	}
	o.userProfile = body
	o.contextFiles = append(o.contextFiles, assembly.ContextFile{Name: "USER.md", Source: path, Content: body})
	return nil
}

const usersUsage = `User profiles and observed identities (local operator inspection).

  agent-host users list --config-dir ABSOLUTE_CONFIG_DIRECTORY
  agent-host users check --config-dir ABSOLUTE_CONFIG_DIRECTORY

An optional users.json beside agents.json enables multi-user private intake.
Profiles live at users/<user-id>/USER.md beneath that configuration directory.
First verified contact also creates a private default profile at
../users/<user-id>/USER.md; an explicit configured profile takes precedence.
list shows configured bindings and observed account receipts, never profile text.
Account linking is an explicit, verified operator configuration edit; chat text
cannot link identities. Profiles and bindings reload on service restart.
`

func runUsers(args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, usersUsage)
		return err
	}
	if args[0] != "list" && args[0] != "check" {
		return errors.New("users requires list or check")
	}
	fs := flag.NewFlagSet("users "+args[0], flag.ContinueOnError)
	fs.SetOutput(errout)
	var dir string
	fs.StringVar(&dir, "config-dir", "", "Absolute Host configuration directory")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !filepath.IsAbs(dir) || len(fs.Args()) != 0 {
		return errors.New("users requires --config-dir ABSOLUTE_PATH")
	}
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil || actual != dir {
		return errors.New("user configuration directory must exist without symlinks")
	}
	c, err := loadUsers(dir)
	if err != nil {
		return err
	}
	enabled := c != nil
	if c == nil {
		c = &userCatalog{root: filepath.Join(filepath.Dir(dir), "users")}
	}
	type observed struct {
		UserID  string      `json:"user_id"`
		Account userAccount `json:"account"`
	}
	type entry struct {
		ID                 string        `json:"id"`
		ProfileFile        string        `json:"profile_file,omitempty"`
		ConfiguredAccounts []userAccount `json:"configured_accounts,omitempty"`
		ObservedAccounts   []userAccount `json:"observed_accounts,omitempty"`
	}
	entries := map[string]*entry{}
	for _, u := range c.Users {
		entries[u.ID] = &entry{ID: u.ID, ProfileFile: u.profilePath, ConfiguredAccounts: u.Accounts}
	}
	if args[0] == "list" {
		paths, err := filepath.Glob(filepath.Join(c.root, "*", "accounts", "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			id := filepath.Base(filepath.Dir(filepath.Dir(path)))
			if !agentIdentifier.MatchString(id) {
				return errors.New("invalid recorded user ID")
			}
			canonical, err := resolveAgentContextPath(dir, path)
			if err != nil {
				return err
			}
			var receipt observed
			if err := readServiceJSON(canonical, &receipt); err != nil {
				return err
			}
			if receipt.UserID != id || receipt.Account.validate() != nil || filepath.Base(path) != accountDigest(receipt.Account)+".json" {
				return errors.New("invalid user identity receipt")
			}
			if entries[id] == nil {
				entries[id] = &entry{ID: id}
			}
			if entries[id].ProfileFile == "" {
				profile := filepath.Join(c.root, id, "USER.md")
				if _, err := readAgentContext(profile, userProfileBudget); err != nil {
					return err
				}
				entries[id].ProfileFile = profile
			}
			entries[id].ObservedAccounts = append(entries[id].ObservedAccounts, receipt.Account)
		}
	}
	list := make([]entry, 0, len(entries))
	for _, u := range entries {
		list = append(list, *u)
	}
	slices.SortFunc(list, func(a, b entry) int { return strings.Compare(a.ID, b.ID) })
	return json.NewEncoder(out).Encode(map[string]any{"enabled": enabled, "users": list, "remote_verified": false})
}
