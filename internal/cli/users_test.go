package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func usersFixture(t *testing.T) (string, agentCatalog) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(dir, "agents.json"), agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant"}}})
	a := userAccount{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "alice"}
	linked := userAccount{Channel: "feishu", AccountID: "cli_bot", TenantID: "tenant", SenderID: "ou_alice"}
	writeBotJSON(t, filepath.Join(dir, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", Accounts: []userAccount{a, linked}, KnowledgeSources: []string{"local-documents:team"}}, {ID: "bob", Accounts: []userAccount{{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "bob"}}}}})
	for id, body := range map[string]string{"alice": "Alice enjoys hiking. Use concise English.", "bob": "Bob enjoys chess. Explain in detail."} {
		path := filepath.Join(dir, "users", id)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "USER.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := loadAgents(filepath.Join(dir, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.ownerID = "owner"
	return root, c
}

func TestUsersResolveAccountsWithoutNamesAndPreserveLegacyOwner(t *testing.T) {
	root, c := usersFixture(t)
	a := userAccount{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "alice"}
	first, err := c.privateAudience(a)
	if err != nil || first != "user:alice" {
		t.Fatal(first, err)
	}
	linked := userAccount{Channel: "feishu", AccountID: "cli_bot", TenantID: "tenant", SenderID: "ou_alice"}
	if id, err := c.privateAudience(linked); err != nil || id != first {
		t.Fatal(id, err)
	}
	for _, change := range []func(*userAccount){func(a *userAccount) { a.TenantID = "another" }, func(a *userAccount) { a.AccountID = "another" }, func(a *userAccount) { a.SenderID = "another" }, func(a *userAccount) { a.Channel = "feishu" }} {
		other := a
		change(&other)
		id, err := c.privateAudience(other)
		if err != nil || id == first || state.PrivateUserID(id) == "" {
			t.Fatal(id, err)
		}
		loaded, err := loadAgents(filepath.Join(root, "config", "agents.json"))
		if err != nil {
			t.Fatal(err)
		}
		loaded.ownerID = "owner"
		if again, err := loaded.privateAudience(other); err != nil || again != id {
			t.Fatal("identity changed on restart", again, err)
		}
	}
	a.SenderID = "owner"
	if id, err := c.privateAudience(a); err != nil || id != "owner" {
		t.Fatal(id, err)
	}
	// Concurrent observations publish one complete receipt, without lost updates.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.privateAudience(linked); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(root, "users", "alice", "accounts", accountDigest(linked)+".json"))
	if err != nil || !json.Valid(raw) || !strings.Contains(string(raw), `"user_id": "alice"`) {
		t.Fatal(string(raw), err)
	}
	// A non-owner can never gain owner policy through an account alias.
	c.users.bindings[linked] = "owner"
	if _, err := c.privateAudience(linked); err == nil {
		t.Fatal("non-owner acquired owner")
	}
}

func TestUserChannelSelectorsAndLocalInspection(t *testing.T) {
	root, c := usersFixture(t)
	for _, id := range []string{"alice", "bob", "unknown", "owner"} {
		selection, err := c.selectAgent("bot", dingtalk.Message{ConversationType: "1", TenantID: "tenant", SenderID: id})
		if err != nil || id == "owner" && selection.AudienceID != "owner" || id != "owner" && state.PrivateUserID(selection.AudienceID) == "" {
			t.Fatal(selection, err)
		}
	}
	selection, err := c.selectFeishuAgent("cli_bot", channel.Message{ConversationType: "1", TenantID: "tenant", SenderID: "ou_alice"})
	if err != nil || selection.AudienceID != "user:alice" {
		t.Fatal(selection, err)
	}
	group, err := c.selectAgent("bot", dingtalk.Message{ConversationType: "2", TenantID: "tenant", SenderID: "alice", ConversationID: "group"})
	if err != nil || state.PrivateUserID(group.AudienceID) != "" {
		t.Fatal("group became private", group, err)
	}
	var out, errout bytes.Buffer
	if err := Run(t.Context(), []string{"users", "list", "--config-dir", filepath.Join(root, "config")}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"observed_accounts"`) || strings.Contains(out.String(), "enjoys hiking") {
		t.Fatal("invalid or private profile inspection", out.String())
	}
	if err := c.users.validateOwner("dingtalk", "bot", "tenant", "alice"); err == nil {
		t.Fatal("owner was rebound to another user")
	}
}

func TestUsersResolvePrivateContextAndRejectForgedAudience(t *testing.T) {
	root, c := usersFixture(t)
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{workspace: root, defaultWorkspace: root, defaultScope: root, nativeProfile: filepath.Join(root, "native"), noTools: true}
	parent := &channel.Service{Store: store, ClientID: "bot", Profile: "fixture"}
	profiles := map[string]string{}
	for _, id := range []string{"alice", "bob", "unknown"} {
		a := userAccount{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: id}
		audience, err := c.privateAudience(a)
		if err != nil {
			t.Fatal(err)
		}
		message := state.ChannelMessage{Channel: "dingtalk", AccountID: "bot", AgentID: "default", AudienceID: audience, ConversationType: "1", ConversationID: "dm-" + id, TenantID: "tenant", SenderID: id}
		child, err := resolveAgentChannel(t.Context(), parent, c, base, client, nil, message)
		if err != nil {
			t.Fatal(err)
		}
		h := child.Host.(*host.Host)
		if h.Config.PermissionMode != "bypassPermissions" || !h.Config.ToolsDisabled || strings.Contains(h.Config.HostInstructions, "authenticated owner") || !strings.Contains(h.Config.HostInstructions, "authenticated user") {
			t.Fatal("shared execution changed private identity or explicit no-tools", h.Config.HostInstructions)
		}
		if id == "unknown" && !strings.Contains(h.Config.UserProfile, "Personal details") || id != "unknown" && !strings.Contains(strings.ToLower(h.Config.UserProfile), id) {
			t.Fatal("wrong profile", id, h.Config.UserProfile)
		}
		profiles[id] = h.Config.NativeProfile
		toolBase := base
		toolBase.noTools = false
		toolParent := *parent
		toolParent.OwnerID = "owner"
		shared, err := resolveAgentChannel(t.Context(), &toolParent, c, toolBase, client, nil, message)
		if err != nil {
			t.Fatal(err)
		}
		policy := shared.Host.(*host.Host).Config
		if policy.PermissionMode != "bypassPermissions" || policy.RestrictedReadOnly || policy.RequestApproval != nil {
			t.Fatal("multi-user profile did not receive shared execution", policy.PermissionMode, policy.RestrictedReadOnly)
		}
		view, err := assembly.MaterializeWorkspace(filepath.Join(root, "cache"), assembly.WorkspaceInput{Run: state.Run{ID: "run-" + id, AgentID: "default", AudienceID: audience, Scope: root}, Files: h.Config.ContextFiles, UserProfile: h.Config.UserProfile})
		if err != nil || view.Manifest.UserID != state.PrivateUserID(audience) {
			t.Fatal(view, err)
		}
		message.AudienceID = "user:bob"
		if id == "bob" {
			message.AudienceID = "owner"
		}
		if _, err := resolveAgentChannel(t.Context(), parent, c, base, client, nil, message); err == nil {
			t.Fatal("forged audience admitted")
		}
	}
	if profiles["alice"] == profiles["bob"] || profiles["bob"] == profiles["unknown"] {
		t.Fatal("native histories shared")
	}
	group := state.ChannelMessage{Channel: "dingtalk", AccountID: "bot", AgentID: "default", ConversationType: "2", TenantID: "tenant", ConversationID: "group", SenderID: "alice"}
	group.AudienceID = channel.Key("group-audience-v1", "dingtalk", "bot", "tenant", "group")
	child, err := resolveAgentChannel(t.Context(), parent, c, base, client, nil, group)
	if err != nil {
		t.Fatal(err)
	}
	h := child.Host.(*host.Host)
	if h.Config.UserProfile != "" || h.Config.NativeProfile == profiles["alice"] {
		t.Fatal("private profile or native history entered group")
	}
}

func TestUserProfileChangeRotatesContextAndNeverWritesConfig(t *testing.T) {
	root, c := usersFixture(t)
	a, _ := c.definition("default")
	base := options{workspace: root, nativeProfile: filepath.Join(root, "native")}
	o, err := a.options(base, "user:alice", false, false)
	if err != nil {
		t.Fatal(err)
	}
	c.userOptions(&o, "user:alice", false)
	before, err := assembly.Assemble("", "", root, root, assemblyConfig(o))
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "config", "users", "alice", "USER.md")
	if err := os.WriteFile(profile, []byte("Prefer detailed Chinese replies."), 0600); err != nil {
		t.Fatal(err)
	}
	// Already loaded tasks keep their input snapshot until an explicit restart.
	if !strings.Contains(o.userProfile, "hiking") {
		t.Fatal("active snapshot changed")
	}
	updated, err := loadAgents(filepath.Join(root, "config", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	updated.userOptions(&o, "user:alice", false)
	after, err := assembly.Assemble("", "", root, root, assemblyConfig(o))
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint || strings.Contains(o.userProfile, "hiking") {
		t.Fatal("stale profile/session survived correction")
	}
	raw, err := os.ReadFile(profile)
	if err != nil || string(raw) != "Prefer detailed Chinese replies." {
		t.Fatal("profile changed during assembly")
	}
}

func TestUserKnowledgeGrantsExcludeUnopenedSources(t *testing.T) {
	_, dir := unifiedFixture(t)
	allowed := datasourceDocumentFixture(t, dir, "local-documents", "team")
	denied := datasourceDocumentFixture(t, dir, "confluence", "private")
	if err := os.Remove(filepath.Join(dir, "credentials", "confluence.json")); err != nil {
		t.Fatal(err)
	}
	o := options{audienceID: "user:alice", knowledgeConfigs: []string{allowed, denied}, userKnowledgeRestricted: true, userKnowledgeSources: []string{"local-documents:team"}}
	if err := resolveKnowledge(&o); err != nil || len(o.knowledgeSources) != 1 || o.knowledgeSources[0].Source.Name != "local-documents:team" {
		t.Fatal(o.knowledgeSources, err)
	}
	if validate := knowledgeValidator(o); validate == nil {
		t.Fatal("grant validation missing")
	} else if err := validate(); err != nil {
		t.Fatal(err)
	}
	o.userKnowledgeSources = nil
	if err := resolveKnowledge(&o); err != nil || len(o.knowledgeSources) != 0 || len(o.mcpServers) != 0 {
		t.Fatal("ungranted sources mounted", err)
	}
}

func TestUnifiedServiceRecognizesUsersAndRequiresExplicitAgents(t *testing.T) {
	_, dir := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(dir, "users.json"), userCatalog{Version: 1, Users: []userDefinition{}})
	writeBotJSON(t, filepath.Join(dir, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"bot": map[string]any{"enabled": true, "client_id": "fixture-app", "robot_code": "fixture-robot", "client_secret_file": "credentials/bot.secret", "corp_id": "fixture-corp", "owner_user_id": "fixture-owner"}, "dws": map[string]any{"enabled": false}}})
	if _, err := discoverUnifiedChannels(dir); err == nil || !strings.Contains(err.Error(), "agents_config") {
		t.Fatal("registry silently ignored", err)
	}
	var hostConfig unifiedHost
	if err := readServiceJSON(filepath.Join(dir, "host.json"), &hostConfig); err != nil {
		t.Fatal(err)
	}
	profile := hostConfig.Profiles["default"]
	profile.Execution.AgentsConfig = "agents.json"
	hostConfig.Profiles["default"] = profile
	writeBotJSON(t, filepath.Join(dir, "host.json"), hostConfig)
	writeBotJSON(t, filepath.Join(dir, "agents.json"), agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant"}}})
	channels, err := discoverUnifiedChannels(dir)
	if err != nil || len(channels) != 1 {
		t.Fatal(channels, err)
	}
	if _, execution, _, err := loadService(channels[0].Root); err != nil || execution.AgentsConfig != filepath.Join(dir, "agents.json") {
		t.Fatal(execution, err)
	}
	// Separate channel catalogs share the root identity registry without forcing
	// a channel to inherit another channel's project allowlist.
	profile.Execution.AgentsConfig = "agents.secondary.json"
	hostConfig.Profiles["default"] = profile
	writeBotJSON(t, filepath.Join(dir, "host.json"), hostConfig)
	writeBotJSON(t, filepath.Join(dir, "agents.secondary.json"), agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant"}}})
	if _, err := discoverUnifiedChannels(dir); err != nil {
		t.Fatal("named agent catalog rejected", err)
	}
	if _, execution, _, err := loadService(channels[0].Root); err != nil || execution.AgentsConfig != filepath.Join(dir, "agents.secondary.json") {
		t.Fatal(execution, err)
	}
}

func TestUsersRejectDuplicateBindingsAndUnsafeProfiles(t *testing.T) {
	root, c := usersFixture(t)
	path := filepath.Join(root, "config", "users.json")
	c.users.Users[1].Accounts = c.users.Users[0].Accounts
	writeBotJSON(t, path, c.users)
	if _, err := loadUsers(filepath.Dir(path)); err == nil {
		t.Fatal("duplicate account binding admitted")
	}
	_, c = usersFixture(t)
	writeBotJSON(t, path, c.users)
	profile := filepath.Join(root, "config", "users", "alice", "USER.md")
	if err := os.Remove(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "config", "users", "bob", "USER.md"), profile); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUsers(filepath.Dir(path)); err == nil {
		t.Fatal("linked profile admitted")
	}
}

func TestFirstContactCreatesPrivateProfileWithoutOverwritingEdits(t *testing.T) {
	root, c := usersFixture(t)
	accounts := []userAccount{
		{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "owner"},
		{Channel: "feishu", AccountID: "cli_bot", TenantID: "tenant", SenderID: "new-person"},
	}
	for _, account := range accounts {
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.privateAudience(account); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		audience, err := c.privateAudience(account)
		if err != nil {
			t.Fatal(err)
		}
		id := state.PrivateUserID(audience)
		path := filepath.Join(root, "users", id, "USER.md")
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), "Host user ID: `"+id+"`") {
			t.Fatal("missing initialized profile", string(raw), err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("profile is not private", info, err)
		}
		if err := os.WriteFile(path, []byte("Explicitly maintained profile."), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.privateAudience(account); err != nil {
			t.Fatal(err)
		}
		o := options{}
		if err := c.userOptions(&o, audience, false); err != nil || o.userProfile != "Explicitly maintained profile." {
			t.Fatal("profile edit lost or not loaded", o.userProfile, err)
		}
		group := options{}
		if err := c.userOptions(&group, "group-audience", true); err != nil || group.userProfile != "" {
			t.Fatal("profile entered group", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "config", "users", "alice", "USER.md"), path); err != nil {
			t.Fatal(err)
		}
		if _, err := c.privateAudience(account); err == nil {
			t.Fatal("linked profile accepted")
		}
	}
	// Config is never populated as a side effect of channel intake.
	if _, err := os.Stat(filepath.Join(root, "config", "users", "owner")); !os.IsNotExist(err) {
		t.Fatal("channel intake wrote configuration", err)
	}
}

func TestBetaSharesEnabledSourcesAcrossUsersGroupsAndAgents(t *testing.T) {
	_, catalog := usersFixture(t)
	_, dir := unifiedFixture(t)
	paths := []string{
		datasourceDocumentFixture(t, dir, "confluence", "company"),
		datasourceDocumentFixture(t, dir, "local-documents", "notes"),
	}
	fingerprint := ""
	for _, agentID := range []string{"default", "other"} {
		for _, audience := range []string{"owner", "user:alice", "user:unknown", "group-audience"} {
			t.Run(agentID+"/"+audience, func(t *testing.T) {
				o := options{agentID: agentID, audienceID: audience, permissionMode: "bypassPermissions", knowledgeConfigs: paths, pluginNames: []string{}, knowledgeNames: nil}
				if err := catalog.userOptions(&o, audience, audience == "group-audience"); err != nil {
					t.Fatal(err)
				}
				if o.userKnowledgeRestricted || len(o.userKnowledgeSources) != 0 || o.permissionMode != "bypassPermissions" {
					t.Fatal("Beta policy still filters by user", o.userKnowledgeRestricted, o.permissionMode)
				}
				if err := resolveKnowledge(&o); err != nil {
					t.Fatal(err)
				}
				if len(o.knowledgeSources) != 2 || len(o.mcpServers) != 2 {
					t.Fatal("enabled sources were not shared", o.knowledgeSources)
				}
				current := assemblyConfig(o).KnowledgeFingerprint
				if fingerprint == "" {
					fingerprint = current
				}
				if current != fingerprint {
					t.Fatal("agent or audience changed source capabilities")
				}
				if validate := knowledgeValidator(o); validate == nil {
					t.Fatal("missing runtime validation")
				} else if err := validate(); err != nil {
					t.Fatal(err)
				}
				o.noTools = true
				if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
					t.Fatal("explicit no-tools ignored", err)
				}
			})
		}
	}
}
