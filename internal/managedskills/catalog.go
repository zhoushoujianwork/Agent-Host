// Package managedskills owns authored publication, immutable revisions and
// effective selection. Experience admission remains owned by the memory store.
package managedskills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agent-host/internal/state"
	"gopkg.in/yaml.v3"
)

const ContractVersion = "managed-skills-v1"
const MaxFileBytes = 256 << 10
const MaxCatalogBytes = 8 << 20

type Entry struct {
	ToolContractDigest string            `json:"tool_contract_digest,omitempty"`
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	Kind               string            `json:"kind"`
	CanonicalRef       string            `json:"canonical_ref"`
	Revision           int               `json:"revision"`
	Digest             string            `json:"digest"`
	Owner              string            `json:"owner"`
	Applicability      string            `json:"applicability"`
	Agents             []string          `json:"agents,omitempty"`
	Scopes             []string          `json:"scopes,omitempty"`
	Audiences          []string          `json:"audiences,omitempty"`
	RequiredPlugins    []string          `json:"required_plugins,omitempty"`
	RequiredContracts  map[string]string `json:"required_contracts,omitempty"`
	RequiredTools      []string          `json:"required_tools,omitempty"`
	Provenance         []string          `json:"provenance"`
	ExpiresAt          *time.Time        `json:"expires_at,omitempty"`
	Files              map[string]string `json:"files"`
	Helpers            bool              `json:"helpers,omitempty"`
	Executables        []string          `json:"executables,omitempty"`
}

type Revision struct {
	Entry      Entry  `json:"entry"`
	Reviewer   string `json:"reviewer,omitempty"`
	ReviewNote string `json:"review_note,omitempty"`
	Published  bool   `json:"published"`
	Withdrawn  bool   `json:"withdrawn"`
}
type Record struct {
	ID        string     `json:"id"`
	Enabled   bool       `json:"enabled"`
	Selected  int        `json:"selected"`
	Revisions []Revision `json:"revisions"`
}
type Audit struct {
	At       time.Time `json:"at"`
	Action   string    `json:"action"`
	ID       string    `json:"id"`
	Revision int       `json:"revision"`
	Actor    string    `json:"actor"`
	Digest   string    `json:"digest"`
}
type Catalog struct {
	Version string   `json:"version"`
	Records []Record `json:"records"`
	Audit   []Audit  `json:"audit"`
}
type Plugin struct {
	Tools    []string `json:"tools"`
	Contract string   `json:"contract"`
}

type Identity struct {
	Agent, Audience string
	Scopes          []string
}
type Snapshot struct {
	Candidates []Entry           `json:"-"`
	Entries    []Entry           `json:"entries"`
	Unready    map[string]string `json:"unready,omitempty"`
	Digest     string            `json:"digest"`
}

type DraftRevisionChange struct {
	Provenance, ReferencePath, ReferenceBody, SkillAppendix string
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,95}$`)

var localReference = regexp.MustCompile(`(?:references|scripts)/[A-Za-z0-9_./-]+\.[A-Za-z0-9]+`)

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (e Entry) ContentDigest() string { e.Digest = ""; return Hash(e) }
func SafePath(p string) bool {
	return p != "" && p != "." && !strings.ContainsAny(p, "\\\x00") && path.Clean(p) == p && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../")
}

// Import pins the entire authored instruction tree. Only SKILL.md and references
// are accepted unless the operator explicitly adopts existing helpers.
func Import(source string, e Entry) (Entry, error) {
	if !identifier.MatchString(e.ID) || e.Owner == "" || e.Applicability == "" || len(e.Provenance) == 0 || len(e.Agents) == 0 || len(e.Audiences) == 0 || len(e.Scopes) == 0 {
		return e, errors.New("import requires safe id, owner, applicability, provenance and explicit agents/audiences/scopes")
	}
	if len(e.Owner) > 256 || len(e.Applicability) > 4096 {
		return e, errors.New("authored metadata exceeds descriptor budget")
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return e, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return e, errors.New("authored source must be canonical; explicitly resolve directory links before adoption")
	}
	e.Kind, e.CanonicalRef, e.Files = "authored", root, map[string]string{}
	e.Executables = nil
	total := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains symlink: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !SafePath(rel) || (rel != "SKILL.md" && rel != "agents/openai.yaml" && !strings.HasPrefix(rel, "references/") && !(e.Helpers && strings.HasPrefix(rel, "scripts/"))) {
			return fmt.Errorf("unadopted source file %s; only instructions, references and explicit helpers are supported", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
			return fmt.Errorf("invalid or oversized skill file %s", rel)
		}
		if strings.HasPrefix(rel, "scripts/") && info.Mode().Perm()&0100 != 0 {
			e.Executables = append(e.Executables, rel)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !utf8.Valid(body) || strings.Contains(string(body), "!`") {
			return fmt.Errorf("invalid or executable dynamic context in %s", rel)
		}
		total += len(body)
		if total > MaxCatalogBytes {
			return errors.New("authored skill exceeds content budget")
		}
		e.Files[rel] = string(body)
		return nil
	})
	if err != nil {
		return e, err
	}
	for p, body := range e.Files {
		if p != "SKILL.md" && !strings.HasSuffix(p, ".md") {
			continue
		}
		for _, ref := range localReference.FindAllString(body, -1) {
			if !SafePath(ref) {
				return e, fmt.Errorf("unsafe local reference %s", ref)
			}
			// A reference can be relative to the skill root or the current file.
			if _, ok := e.Files[ref]; !ok {
				if _, ok = e.Files[path.Join(path.Dir(p), ref)]; !ok {
					return e, fmt.Errorf("unavailable local reference %s in %s", ref, p)
				}
			}
		}
	}
	body := e.Files["SKILL.md"]
	if !strings.HasPrefix(body, "---\n") {
		return e, errors.New("SKILL.md requires YAML frontmatter")
	}
	parts := strings.SplitN(body, "---", 3)
	if len(parts) != 3 {
		return e, errors.New("unclosed skill frontmatter")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &meta); err != nil {
		return e, err
	}
	if !identifier.MatchString(meta.Name) || strings.TrimSpace(meta.Description) == "" || len(meta.Description) > 2048 {
		return e, errors.New("skill requires safe name and description")
	}
	// Native permissions and hooks are never adopted from source frontmatter.
	var keys map[string]any
	if err := yaml.Unmarshal([]byte(parts[1]), &keys); err != nil {
		return e, err
	}
	for key := range keys {
		if key != "name" && key != "description" && key != "metadata" {
			return e, fmt.Errorf("unsupported native frontmatter %s", key)
		}
	}
	e.Name, e.Description = meta.Name, meta.Description
	return e, nil
}

func Load(root string) (Catalog, error) {
	c := Catalog{Version: ContractVersion}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return c, errors.New("managed skill root must be a clean absolute path")
	}
	// Check the existing ancestor too, before an absent catalog can be created.
	for p := root; ; p = filepath.Dir(p) {
		actual, err := filepath.EvalSymlinks(p)
		if err == nil {
			if actual != p {
				return c, errors.New("managed skill root contains symlinks")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return c, err
		}
		if filepath.Dir(p) == p {
			return c, err
		}
	}
	p := filepath.Join(root, "catalog.json")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 32<<20 {
		return c, errors.New("managed catalog must be private, regular and within budget")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Version != ContractVersion {
		return c, errors.New("unsupported managed skill catalog version")
	}
	seen := map[string]bool{}
	for _, r := range c.Records {
		if !identifier.MatchString(r.ID) || seen[r.ID] || r.Selected < 0 || r.Selected > len(r.Revisions) {
			return c, errors.New("invalid managed skill record")
		}
		seen[r.ID] = true
		for i, v := range r.Revisions {
			if v.Entry.ID != r.ID || v.Entry.Revision != i+1 || v.Entry.Digest != v.Entry.ContentDigest() {
				return c, fmt.Errorf("managed skill revision integrity failed: %s", r.ID)
			}
		}
	}
	return c, nil
}

// Update serializes lifecycle mutations and atomically replaces the catalog.
// Pinned revisions are never overwritten; review and selection are audit state.
func Update(root, action, id, actor, digest, note string, revision int, imported *Entry) (Catalog, error) {
	if actor == "" {
		return Catalog{}, errors.New("lifecycle action requires actor")
	}
	if _, err := Load(root); err != nil {
		return Catalog{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Catalog{}, err
	}
	release, err := state.Acquire(root)
	if err != nil {
		return Catalog{}, err
	}
	defer release()
	c, err := Load(root)
	if err != nil {
		return c, err
	}
	i := -1
	for n, r := range c.Records {
		if r.ID == id {
			i = n
		}
	}
	if action == "import" {
		if imported == nil || imported.ID != id || imported.Kind != "authored" {
			return c, errors.New("only explicitly adopted authored sources may be imported")
		}
		if i < 0 {
			c.Records = append(c.Records, Record{ID: id})
			i = len(c.Records) - 1
		}
		e := *imported
		e.Revision = len(c.Records[i].Revisions) + 1
		e.Digest = e.ContentDigest()
		c.Records[i].Revisions = append(c.Records[i].Revisions, Revision{Entry: e})
		revision = e.Revision
		digest = e.Digest
	} else {
		if i < 0 || revision < 1 || revision > len(c.Records[i].Revisions) {
			return c, errors.New("skill revision not found")
		}
		r := &c.Records[i]
		v := &r.Revisions[revision-1]
		if digest == "" || digest != v.Entry.Digest {
			return c, errors.New("expected revision digest required; inspect before changing lifecycle")
		}
		switch action {
		case "review":
			if v.Published || v.Withdrawn || note == "" {
				return c, errors.New("review requires draft and evidence note")
			}
			v.Reviewer, v.ReviewNote = actor, note
		case "publish":
			if v.Reviewer == "" || v.ReviewNote == "" || v.Withdrawn {
				return c, errors.New("publication requires reviewed eligible revision")
			}
			v.Published = true
		case "enable", "rollback":
			if !v.Published || v.Withdrawn || (v.Entry.ExpiresAt != nil && !v.Entry.ExpiresAt.After(time.Now())) {
				return c, errors.New("selection requires published eligible revision")
			}
			for n, other := range c.Records {
				if n != i && other.Enabled && other.Selected > 0 && other.Revisions[other.Selected-1].Entry.Name == v.Entry.Name {
					return c, errors.New("duplicate enabled skill name; disable conflicting identity explicitly")
				}
			}
			r.Selected, r.Enabled = revision, true
		case "disable":
			r.Enabled = false
		case "withdraw":
			v.Withdrawn = true
			if r.Selected == revision {
				r.Enabled = false
			}
		default:
			return c, errors.New("unknown managed skill lifecycle action")
		}
	}
	c.Audit = append(c.Audit, Audit{At: time.Now().UTC(), Action: action, ID: id, Revision: revision, Actor: actor, Digest: digest})
	return c, saveCatalog(root, c)
}

// AppendDraftRevision appends one immutable, unreviewed revision while holding
// the catalog lock. Repeating the same provenance returns its exact revision.
func AppendDraftRevision(root, id, actor string, expectedRevision int, expectedDigest string, change DraftRevisionChange) (Entry, error) {
	if actor == "" || id == "" || expectedRevision < 1 || expectedDigest == "" || change.Provenance == "" || !SafePath(change.ReferencePath) || !strings.HasPrefix(change.ReferencePath, "references/") || !strings.HasSuffix(change.ReferencePath, ".md") || strings.TrimSpace(change.ReferenceBody) == "" || strings.TrimSpace(change.SkillAppendix) == "" || !utf8.ValidString(change.ReferenceBody) || !utf8.ValidString(change.SkillAppendix) || strings.Contains(change.ReferenceBody, "!`") || strings.Contains(change.SkillAppendix, "!`") {
		return Entry{}, errors.New("draft revision requires actor, exact target and safe correction reference")
	}
	if _, err := Load(root); err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Entry{}, err
	}
	release, err := state.Acquire(root)
	if err != nil {
		return Entry{}, err
	}
	defer release()
	c, err := Load(root)
	if err != nil {
		return Entry{}, err
	}
	index := -1
	for i := range c.Records {
		if c.Records[i].ID != id {
			continue
		}
		index = i
		for _, revision := range c.Records[i].Revisions {
			for _, provenance := range revision.Entry.Provenance {
				if provenance == change.Provenance {
					return revision.Entry, nil
				}
			}
		}
	}
	if index < 0 {
		return Entry{}, errors.New("target Skill is not an authored managed Skill; candidate remains pending")
	}
	record := &c.Records[index]
	if len(record.Revisions) != expectedRevision {
		return Entry{}, errors.New("target Skill revision changed; candidate remains pending")
	}
	latest := record.Revisions[len(record.Revisions)-1].Entry
	if latest.Revision != expectedRevision || latest.Digest != expectedDigest || latest.Kind != "authored" {
		return Entry{}, errors.New("target Skill digest changed or is not authored; candidate remains pending")
	}
	raw, err := json.Marshal(latest)
	if err != nil {
		return Entry{}, err
	}
	var next Entry
	if err = json.Unmarshal(raw, &next); err != nil {
		return Entry{}, err
	}
	if _, exists := next.Files[change.ReferencePath]; exists {
		return Entry{}, errors.New("target Skill correction reference already exists without matching provenance")
	}
	next.Files[change.ReferencePath] = change.ReferenceBody
	next.Files["SKILL.md"] = strings.TrimRight(next.Files["SKILL.md"], "\n") + "\n\n" + strings.TrimSpace(change.SkillAppendix) + "\n"
	if len(next.Files[change.ReferencePath]) > MaxFileBytes || len(next.Files["SKILL.md"]) > MaxFileBytes {
		return Entry{}, errors.New("draft Skill correction exceeds file budget")
	}
	totalBytes := 0
	for _, body := range next.Files {
		totalBytes += len(body)
	}
	if totalBytes > MaxCatalogBytes {
		return Entry{}, errors.New("draft Skill correction exceeds authored tree budget")
	}
	next.Provenance = append(next.Provenance, change.Provenance)
	next.Revision = len(record.Revisions) + 1
	next.Digest = next.ContentDigest()
	record.Revisions = append(record.Revisions, Revision{Entry: next})
	c.Audit = append(c.Audit, Audit{At: time.Now().UTC(), Action: "draft-revision", ID: id, Revision: next.Revision, Actor: actor, Digest: next.Digest})
	if err = saveCatalog(root, c); err != nil {
		return Entry{}, err
	}
	return next, nil
}

func saveCatalog(root string, c Catalog) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return errors.New("managed catalog exceeds storage budget; no lifecycle update was saved")
	}
	f, err := os.CreateTemp(root, ".catalog-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(raw, '\n'))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(root, "catalog.json"))
}

func allows(values []string, value string) bool {
	for _, v := range values {
		if v == value || v == "*" {
			return true
		}
	}
	return false
}
func Resolve(c Catalog, identity Identity, plugins map[string]Plugin, now time.Time) Snapshot {
	s := Snapshot{Unready: map[string]string{}}
	for _, r := range c.Records {
		if !r.Enabled || r.Selected == 0 {
			continue
		}
		v := r.Revisions[r.Selected-1]
		e := v.Entry
		if !v.Published || v.Withdrawn || (e.ExpiresAt != nil && !e.ExpiresAt.After(now)) || !allows(e.Agents, identity.Agent) || !allows(e.Audiences, identity.Audience) {
			continue
		}
		matched := false
		for _, scope := range identity.Scopes {
			if allows(e.Scopes, scope) {
				matched = true
			}
		}
		if !matched {
			continue
		}
		s.Candidates = append(s.Candidates, e)
		reason := DependencyError(e, plugins)
		if reason != "" {
			s.Unready[e.ID] = reason
			continue
		}
		s.Entries = append(s.Entries, e)
	}
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].ID < s.Entries[j].ID })
	s.Digest = Hash(struct {
		Version    string
		Entries    []Entry
		Candidates []Entry
		Unready    map[string]string
		Plugins    map[string]Plugin
	}{ContractVersion, s.Entries, s.Candidates, s.Unready, plugins})
	return s
}

// DependencyError evaluates the exact mounted tool snapshot, including per-run Host tools.
func DependencyError(e Entry, plugins map[string]Plugin) string {
	reason := ""
	for _, p := range e.RequiredPlugins {
		if _, ok := plugins[p]; !ok {
			reason = "required plugin unavailable: " + p
		}
	}
	for _, tool := range e.RequiredTools {
		found := false
		for _, plugin := range plugins {
			for _, t := range plugin.Tools {
				if t == tool {
					found = true
				}
			}
		}
		if !found {
			reason = "required tool unavailable: " + tool
		}
	}
	contracts := make([]string, 0, len(e.RequiredContracts))
	for name := range e.RequiredContracts {
		contracts = append(contracts, name)
	}
	sort.Strings(contracts)
	for _, name := range contracts {
		expected := e.RequiredContracts[name]
		if plugin, ok := plugins[name]; !ok || plugin.Contract != expected {
			reason = "incompatible plugin contract: " + name
		}
	}
	return reason
}
