// Package knowledge provides owner-configured runtime source tools.
package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"agent-host/internal/componentconfig"
)

const ConnectorVersion = "knowledge-v6:confluence-personal-blog"

type Source struct {
	ConfigPath        string   `json:"-"`
	AccessFingerprint string   `json:"-"`
	Name              string   `json:"name"`
	Provider          string   `json:"provider"`
	Endpoint          string   `json:"endpoint,omitempty"`
	SourceURLBase     string   `json:"source_url_base,omitempty"`
	CredentialFile    string   `json:"credential_file,omitempty"`
	Root              string   `json:"root,omitempty"`
	Paths             []string `json:"paths,omitempty"`
	Principal         string   `json:"principal"`
	Description       string   `json:"description"`
	Projects          []string `json:"projects,omitempty"`
	Audiences         []string `json:"audiences,omitempty"`
}

type Configuration struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

type ResolvedSource struct {
	ConfigPath  string `json:"-"`
	Source      Source `json:"source"`
	Fingerprint string `json:"fingerprint"`
	Credential  string `json:"-"`
}

// Hint contains only navigation metadata, never endpoints or credential paths.
type Hint struct {
	Projects    []string `json:"projects,omitempty"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
}

func (r ResolvedSource) Hint() Hint {
	tools := []string{"search_confluence", "read_confluence_page", "create_confluence_page", "update_confluence_page", "read_confluence_blogpost", "create_confluence_blogpost", "update_confluence_blogpost"}
	if r.Source.Provider == "dokki" {
		tools = []string{"list_dokki_workspaces", "search_dokki", "read_dokki_resource"}
	} else if r.Source.Provider == "local_markdown" {
		tools = []string{"search_local_markdown", "read_local_markdown"}
	}
	return Hint{Name: r.Source.Name, Provider: r.Source.Provider, Description: r.Source.Description, Tools: tools}
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var projectIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var audienceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Load validates local configuration only; it never connects to a source.
func Load(path string) (Configuration, error) {
	var c Configuration
	raw, err := readPrivate(path, 64<<10)
	if err != nil {
		return c, fmt.Errorf("knowledge configuration: %w", err)
	}
	original := raw
	raw, err = componentconfig.Settings(path, raw, "")
	if err != nil {
		return c, err
	}
	pluginID := componentconfig.DataSourceID(path)
	if pluginID != "" {
		provider, known := componentconfig.DataSources[pluginID]
		if !known || provider == "" {
			return c, errors.New("unknown document datasource plugin")
		}
		var settings struct {
			Sources []Source `json:"sources"`
		}
		if err := decodeStrict(raw, &settings); err != nil || settings.Sources == nil {
			return c, errors.New("document datasource requires sources")
		}
		c.Version = 1
		c.Sources = settings.Sources
		for i := range c.Sources {
			if !identifier.MatchString(c.Sources[i].Name) || c.Sources[i].Provider != "" {
				return c, errors.New("datasource sources require local names and must not repeat provider")
			}
			c.Sources[i].Provider = provider
			c.Sources[i].Name = pluginID + ":" + c.Sources[i].Name
		}
	} else if err := decodeStrict(raw, &c); err != nil || c.Version != 1 || c.Sources == nil || len(c.Sources) > 32 {
		return c, errors.New("knowledge configuration requires version 1 and up to 32 sources using documented fields")
	}
	if len(c.Sources) > 32 {
		return c, errors.New("datasource exceeds 32 sources")
	}
	if pluginID != "" {
		for i := range c.Sources {
			source := &c.Sources[i]
			source.ConfigPath = path
			source.Root = componentconfig.Resolve(path, source.Root)
			source.CredentialFile = componentconfig.Resolve(path, source.CredentialFile)
			// Older scope fields are accepted for migration but have no effect.
			source.Projects, source.Audiences = nil, nil
		}
	} else if filepath.Base(path) == "plugins.knowledge.json" {
		envelope, e := componentconfig.Decode(original)
		if e != nil {
			return c, e
		}
		if envelope.Access == nil || len(envelope.Access.Audiences) == 0 {
			return c, errors.New("knowledge plugin requires audience access")
		}
		var sources []Source
		accessRaw, _ := json.Marshal(envelope.Access)
		accessHash := sha256.Sum256(accessRaw)
		for _, source := range c.Sources {
			source.AccessFingerprint = hex.EncodeToString(accessHash[:])
			source.Root = componentconfig.Resolve(path, source.Root)
			source.CredentialFile = componentconfig.Resolve(path, source.CredentialFile)
			if source.Audiences == nil {
				source.Audiences = envelope.Access.Audiences
			} else {
				var allowed []string
				for _, audience := range source.Audiences {
					for _, grant := range envelope.Access.Audiences {
						if audience == grant {
							allowed = append(allowed, audience)
						}
					}
				}
				source.Audiences = allowed
				if len(allowed) == 0 {
					continue
				}
			}
			sources = append(sources, source)
		}
		c.Sources = sources
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if !validSourceName(s.Name) || pluginID == "" && !identifier.MatchString(s.Name) || seen[s.Name] {
			return c, errors.New("knowledge source names must be unique lowercase identifiers")
		}
		seen[s.Name] = true
		if err := validateSource(s); err != nil {
			return c, err
		}
	}
	for i := range c.Sources {
		c.Sources[i].ConfigPath = path
	}
	return c, nil
}

func validSourceName(name string) bool {
	parts := strings.Split(name, ":")
	if len(parts) == 1 {
		return identifier.MatchString(name)
	}
	if len(parts) != 2 || !identifier.MatchString(parts[1]) {
		return false
	}
	provider, ok := componentconfig.DataSources[parts[0]]
	return ok && provider != ""
}

// MountName keeps namespaced source identities valid in native MCP tool names.
func MountName(name string) string { return "knowledge_" + strings.ReplaceAll(name, ":", "_") }

func validateSource(s Source) error {
	if !validSourceName(s.Name) {
		return errors.New("knowledge source name must be a lowercase identifier")
	}
	if s.Provider == "local_markdown" {
		if s.Endpoint != "" || s.CredentialFile != "" || s.SourceURLBase != "" {
			return errors.New("local Markdown sources use root and paths, without endpoints or credentials")
		}
		if !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || strings.ContainsAny(s.Root, "\x00\r\n") || len(s.Paths) == 0 || len(s.Paths) > 128 {
			return errors.New("local Markdown sources require a clean absolute root and 1..128 explicit relative paths")
		}
		paths := map[string]bool{}
		for _, path := range s.Paths {
			if !validLocalPath(path) || paths[path] {
				return errors.New("local Markdown paths must be distinct clean relative paths without hidden segments or traversal")
			}
			paths[path] = true
		}
	} else {
		if s.Provider != "dokki" && s.Provider != "confluence" {
			return errors.New("knowledge provider must be dokki, confluence or local_markdown")
		}
		if s.Root != "" || s.Paths != nil {
			return errors.New("root and paths are supported only for local Markdown")
		}
		if s.Provider != "dokki" && s.SourceURLBase != "" {
			return errors.New("source_url_base is supported only for Dokki; Confluence uses upstream page links")
		}
		if !validEndpoint(s.Endpoint) || s.SourceURLBase != "" && !validEndpoint(s.SourceURLBase) {
			return errors.New("knowledge endpoints must be HTTPS URLs without credentials, query or fragment (HTTP loopback is allowed for tests)")
		}
		if !filepath.IsAbs(s.CredentialFile) || filepath.Clean(s.CredentialFile) != s.CredentialFile {
			return errors.New("knowledge credential_file must be a clean absolute path")
		}
	}
	if strings.TrimSpace(s.Principal) != s.Principal || s.Principal == "" || len(s.Principal) > 256 || strings.ContainsAny(s.Principal, "\x00\r\n") || strings.TrimSpace(s.Description) == "" || len(s.Description) > 1024 || strings.ContainsRune(s.Description, '\x00') {
		return errors.New("knowledge sources require a principal label and a short description")
	}
	if (!globalSource(s) && len(s.Projects) == 0) || len(s.Projects) > 128 {
		return errors.New("knowledge sources require explicit projects (use default for unregistered workspaces)")
	}
	projects := map[string]bool{}
	for _, id := range s.Projects {
		if !projectIdentifier.MatchString(id) || projects[id] {
			return errors.New("knowledge projects must be distinct registered IDs or default")
		}
		projects[id] = true
	}
	if s.Audiences != nil {
		if len(s.Audiences) == 0 || len(s.Audiences) > 128 {
			return errors.New("knowledge audiences must be omitted or contain 1..128 audience IDs")
		}
		audiences := map[string]bool{}
		for _, id := range s.Audiences {
			if !audienceIdentifier.MatchString(id) || audiences[id] {
				return errors.New("knowledge audiences must contain distinct audience IDs")
			}
			audiences[id] = true
		}
	}
	return nil
}

func validEndpoint(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}

func resolve(s Source) (ResolvedSource, error) {
	if s.Provider == "local_markdown" {
		if err := validateLocalSource(s); err != nil {
			return ResolvedSource{}, err
		}
		return fingerprintSource(s, ""), nil
	}
	raw, err := readPrivate(s.CredentialFile, 16<<10)
	if err != nil {
		return ResolvedSource{}, fmt.Errorf("knowledge source %s credential: %w", s.Name, err)
	}
	var c struct {
		APIKey string `json:"api_key"`
		PAT    string `json:"pat"`
	}
	if err := decodeStrict(raw, &c); err != nil {
		return ResolvedSource{}, errors.New("knowledge credential must contain only its provider's api_key or pat")
	}
	token := c.PAT
	if s.Provider == "dokki" {
		token = c.APIKey
	}
	if token == "" || token != strings.TrimSpace(token) || strings.ContainsAny(token, "\x00\r\n") || s.Provider == "dokki" && c.PAT != "" || s.Provider == "confluence" && c.APIKey != "" {
		return ResolvedSource{}, errors.New("knowledge credential must contain a nonempty single-line token for its provider")
	}
	// Credential changes rotate the binding conservatively; token values never
	// enter argv, context, state, or error output.
	return fingerprintSource(s, token), nil
}

func fingerprintSource(s Source, token string) ResolvedSource {
	payload, _ := json.Marshal(struct {
		Version string
		Source  Source
		Token   string
		Access  string
	}{ConnectorVersion, s, token, s.AccessFingerprint})
	digest := sha256.Sum256(payload)
	return ResolvedSource{Source: s, ConfigPath: s.ConfigPath, Fingerprint: hex.EncodeToString(digest[:]), Credential: token}
}

// ResolveConfiguredSource checks exactly this declaration for local operators,
// including audience-restricted sources, without selecting its neighboring sources.
func ResolveConfiguredSource(s Source) (ResolvedSource, error) {
	if err := validateSource(s); err != nil {
		return ResolvedSource{}, err
	}
	return resolve(s)
}

// Select reads credentials only for the selected project. An unavailable
// unselected credential cannot block an unrelated task.
func Select(path, project string) ([]ResolvedSource, error) {
	if project == "" {
		project = "default"
	}
	return SelectAgentAudienceProjects(path, []string{project}, nil, "owner")
}

func Fingerprint(selected []ResolvedSource) string {
	ids := make([]string, 0, len(selected))
	for _, r := range selected {
		ids = append(ids, r.Fingerprint)
	}
	raw, _ := json.Marshal(ids)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ResolveSource rechecks the exact launch binding at every MCP operation.
func ResolveSource(path, name, expected string) (ResolvedSource, error) {
	if len(expected) != 64 {
		return ResolvedSource{}, errors.New("knowledge MCP requires a pinned source fingerprint")
	}
	c, err := Load(path)
	if err != nil {
		return ResolvedSource{}, err
	}
	for _, s := range c.Sources {
		if s.Name == name {
			r, err := resolve(s)
			if err != nil {
				return ResolvedSource{}, err
			}
			if r.Fingerprint != expected {
				return ResolvedSource{}, errors.New("knowledge source access or configuration changed; start a new session")
			}
			return r, nil
		}
	}
	return ResolvedSource{}, errors.New("knowledge source is no longer configured")
}

func readPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("must use a clean absolute file path")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("must be an owner-private regular file (0600 or 0400)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open private file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private file changed while opening")
	}
	owner, ok := after.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("private file must belong to the current user")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("private file could not be read within its size limit")
	}
	return raw, nil
}

func decodeStrict(raw []byte, out any) error {
	// Reject duplicate object keys, including nested source declarations.
	keys := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
		t, err := keys.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for keys.More() {
				k, err := keys.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[strings.ToLower(key)] {
					return errors.New("duplicate JSON field")
				}
				seen[strings.ToLower(key)] = true
				if err := value(); err != nil {
					return err
				}
			}
			_, err = keys.Token()
			return err
		case json.Delim('['):
			for keys.More() {
				if err := value(); err != nil {
					return err
				}
			}
			_, err = keys.Token()
			return err
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid JSON fields or types")
	}
	var tail any
	if err := d.Decode(&tail); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}
