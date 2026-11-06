package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/dream"
	"agent-host/internal/state"
)

func sharingStateFixture(t *testing.T, partition state.DreamPartition, memories ...state.Memory) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range memories {
		raw, _ := json.Marshal(memory)
		var expiry any
		if memory.ExpiresAt != nil {
			expiry = memory.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err = db.Exec("INSERT INTO memories(id,agent_id,audience_id,sender_id,scope,text,status,expires_at,payload) VALUES(?,?,?,?,?,?,?,?,?)", memory.ID, partition.AgentID, partition.AudienceID, partition.PersonID, partition.Scope, memory.Text, memory.Status, expiry, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return home
}

func sharingMemory(id, person, agent, audience, scope, kind, text string, expiry time.Time) state.Memory {
	return state.Memory{ID: id, SenderID: person, AgentID: agent, AudienceID: audience, Scope: scope, Kind: kind, Text: text, Status: "active", Revision: 1, SourceType: "dream_reviewed", CreatedAt: expiry.Add(-time.Hour), ExpiresAt: &expiry}
}

func TestDreamSharingTwoExplicitChannelsAndFailClosedRefresh(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err = os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(configDir, "agents.json")
	writeBotJSON(t, agentsPath, agentCatalog{DefaultAgent: "agent", Agents: []agentDefinition{{ID: "agent", Name: "Agent"}}})
	ding := userAccount{Channel: "dingtalk", AccountID: "ding-app", TenantID: "tenant", SenderID: "ding-alice"}
	fei := userAccount{Channel: "feishu", AccountID: "fei-app", TenantID: "tenant", SenderID: "fei-alice"}
	usersPath := filepath.Join(configDir, "users.json")
	users := userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", Accounts: []userAccount{ding, fei}}}}
	writeBotJSON(t, usersPath, users)
	now := time.Now().UTC().Round(time.Second)
	targetHome := sharingStateFixture(t, state.DreamPartition{})
	target := state.DreamPartition{PersonID: ding.SenderID, AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
	onePartition := state.DreamPartition{PersonID: ding.SenderID, AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
	twoPartition := state.DreamPartition{PersonID: fei.SenderID, AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
	oneHome := sharingStateFixture(t, onePartition,
		sharingMemory("a-fact", ding.SenderID, "agent", "user:alice", "scope", "project_fact", "not mounted", now.Add(time.Hour)),
		sharingMemory("z-one", ding.SenderID, "agent", "user:alice", "scope", "preference", "same preference", now.Add(time.Hour)))
	twoHome := sharingStateFixture(t, twoPartition, sharingMemory("two", fei.SenderID, "agent", "user:alice", "scope", "preference", "same preference", now.Add(time.Hour)))
	policyPath := filepath.Join(configDir, "dream-sharing.json")
	policy := dreamSharingPolicy{Version: 1, Generation: 1, Mounts: []dreamSharingMount{{ID: "alice-share", CanonicalPersonID: "alice", TargetStateHome: targetHome, Target: target, TargetAccount: &ding, Sources: []dreamSharingSource{{ID: "ding", StateHome: oneHome, Partition: onePartition, Account: &ding, Kinds: []string{"preference"}}, {ID: "fei", StateHome: twoHome, Partition: twoPartition, Account: &fei, Kinds: []string{"preference"}}}}}}
	writeBotJSON(t, policyPath, policy)
	cache, _ := filepath.EvalSymlinks(t.TempDir())
	hot, _ := filepath.EvalSymlinks(t.TempDir())
	for _, path := range []string{cache, hot} {
		if err = os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	dreamPath := writeDreamConfig(t, `{"hot_enabled":true,"cache_root":"`+cache+`","hot_root":"`+hot+`","sharing_policy":"`+policyPath+`","person_id":"`+target.PersonID+`","agent_id":"agent","audience_id":"user:alice","scope":"scope","batch":1}`)
	store, err := state.Open(targetHome, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	o := options{agentsConfig: agentsPath, dreamConfig: dreamPath, agentID: "agent", audienceID: "user:alice", scope: "scope", dreamSharingIdentity: dreamSharingIdentity{Account: &ding, OwnerID: "owner"}}
	if err = prepareDreamHOT(store, &o); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(hot)
	targetLink, _ := os.Readlink(filepath.Join(hot, entries[0].Name(), "current"))
	raw, _ := os.ReadFile(filepath.Join(hot, entries[0].Name(), filepath.FromSlash(targetLink), "manifest.json"))
	var bundle dream.HOTBundle
	if json.Unmarshal(raw, &bundle) != nil || bundle.Version != "dream-hot-v2" || bundle.Projection == nil || len(bundle.Projection.Memories) != 2 {
		t.Fatalf("v2 manifest: %s", raw)
	}
	documents, firstDigest, err := o.hotRefresh(context.Background())
	if err != nil || firstDigest != bundle.Digest || len(documents) == 0 || string(documents[0].Content) != "- [source:ding,fei] same preference\n" {
		t.Fatalf("initial and request shared HOT differ: %+v %v", documents, err)
	}

	policy.Generation++
	writeBotJSON(t, policyPath, policy)
	_, secondDigest, err := o.hotRefresh(context.Background())
	if err != nil || secondDigest == firstDigest {
		t.Fatal("generation did not rotate authorization digest", err)
	}
	extra := userAccount{Channel: "dingtalk", AccountID: "other-app", TenantID: "tenant", SenderID: "alice-other"}
	users.Users[0].Accounts = append(users.Users[0].Accounts, extra)
	writeBotJSON(t, usersPath, users)
	boundDocuments, bindingDigest, err := o.hotRefresh(context.Background())
	if err != nil || bindingDigest == secondDigest || len(boundDocuments) == 0 || string(boundDocuments[0].Content) != "- [source:ding,fei] same preference\n" {
		t.Fatal("binding change did not rotate unchanged shared HOT", err)
	}
	users.Users[0].Accounts = []userAccount{ding, fei}
	writeBotJSON(t, usersPath, users)
	dreamRaw, err := os.ReadFile(dreamPath)
	if err != nil {
		t.Fatal(err)
	}
	var dreamConfig map[string]any
	if err = json.Unmarshal(dreamRaw, &dreamConfig); err != nil {
		t.Fatal(err)
	}
	delete(dreamConfig, "sharing_policy")
	withoutSharing, _ := json.Marshal(dreamConfig)
	if err = os.WriteFile(dreamPath, withoutSharing, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = o.hotRefresh(context.Background()); err == nil {
		t.Fatal("removing sharing_policy from a live shared runtime fell back to local HOT")
	}
	if err = os.WriteFile(dreamPath, dreamRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err = o.hotRefresh(context.Background()); err == nil {
		t.Fatal("deleted sharing policy fell back to old HOT")
	}
	writeBotJSON(t, policyPath, policy)
	users.Users[0].Accounts = []userAccount{ding}
	writeBotJSON(t, usersPath, users)
	if _, _, err = o.hotRefresh(context.Background()); err == nil {
		t.Fatal("removed source binding was accepted")
	}
	writeBotJSON(t, usersPath, userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", Accounts: []userAccount{ding, fei}}}})
	if err = os.Rename(filepath.Join(twoHome, "state.db"), filepath.Join(twoHome, "state.db.missing")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = o.hotRefresh(context.Background()); err == nil {
		t.Fatal("unreadable source fell back to old HOT")
	}
}

func TestDreamSharingPolicyBoundariesAndLegacyNoPolicy(t *testing.T) {
	ding := userAccount{Channel: "dingtalk", AccountID: "app", TenantID: "tenant", SenderID: "alice-raw"}
	users := &userCatalog{Users: []userDefinition{{ID: "alice"}}, bindings: map[userAccount]string{ding: "alice"}}
	target := state.DreamPartition{PersonID: ding.SenderID, AgentID: "agent", AudienceID: "user:alice", Scope: "scope-a"}
	home, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	base := dreamSharingMount{ID: "mount", CanonicalPersonID: "alice", TargetStateHome: home, Target: target, TargetAccount: &ding}
	valid := dreamSharingSource{ID: "source", StateHome: home, Partition: target, Account: &ding, Kinds: []string{"preference"}}
	for name, mutate := range map[string]func(*dreamSharingMount){
		"different agent": func(m *dreamSharingMount) { m.Sources[0].Partition.AgentID = "other" },
		"cross scope fact": func(m *dreamSharingMount) {
			m.Sources[0].Partition.Scope = "scope-b"
			m.Sources[0].Kinds = []string{"project_fact"}
		},
		"group audience":         func(m *dreamSharingMount) { m.Sources[0].Partition.AudienceID = "group" },
		"different person":       func(m *dreamSharingMount) { m.Sources[0].Partition.PersonID = "other" },
		"empty target scope":     func(m *dreamSharingMount) { m.Target.Scope = "" },
		"oversized source scope": func(m *dreamSharingMount) { m.Sources[0].Partition.Scope = strings.Repeat("x", 513) },
	} {
		t.Run(name, func(t *testing.T) {
			mount := base
			mount.Sources = []dreamSharingSource{valid}
			mutate(&mount)
			policy := dreamSharingPolicy{Version: 1, Generation: 1, Mounts: []dreamSharingMount{mount}}
			if _, err := selectDreamSharingMount(policy, users, home, target, dreamSharingIdentity{Account: &ding}); err == nil {
				t.Fatal("unsafe mount accepted")
			}
		})
	}
	wrongTargetPerson := base
	wrongTargetPerson.Target.PersonID = "other"
	if err := validateDreamSharingTargetBinding(wrongTargetPerson, users); err == nil || !strings.Contains(err.Error(), "target person") {
		t.Fatal("target partition accepted a person outside its account binding", err)
	}
	preference := base
	preference.Sources = []dreamSharingSource{valid}
	preference.Sources[0].Partition.Scope = "scope-b"
	if _, err := selectDreamSharingMount(dreamSharingPolicy{Version: 1, Generation: 1, Mounts: []dreamSharingMount{preference}}, users, home, target, dreamSharingIdentity{Account: &ding}); err != nil {
		t.Fatal("explicit cross-scope preference rejected", err)
	}
	local := base
	local.Sources = []dreamSharingSource{valid}
	local.TargetAccount, local.TargetLocalOwner, local.CanonicalPersonID = nil, true, "alice"
	if _, err := selectDreamSharingMount(dreamSharingPolicy{Version: 1, Generation: 1, Mounts: []dreamSharingMount{local}}, users, home, target, dreamSharingIdentity{LocalOwner: true}); err == nil || !strings.Contains(err.Error(), "reserved owner") {
		t.Fatal("CLI self-reported another canonical person", err)
	}
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bundle, err := exportRuntimeHOT(context.Background(), store, options{dreamSharingIdentity: dreamSharingIdentity{Account: &ding}}, &dreamRuntimeConfig{PersonID: target.PersonID, AgentID: target.AgentID, AudienceID: target.AudienceID, Scope: target.Scope, Batch: 10})
	if err != nil || bundle.Version != "dream-hot-v1" || bundle.Projection != nil || len(bundle.Memories) != 0 {
		t.Fatalf("users binding without sharing_policy changed legacy HOT: %+v %v", bundle, err)
	}
}

func TestDreamSharingPolicyRequiresHOT(t *testing.T) {
	policy := filepath.Join(t.TempDir(), "sharing.json")
	writeBotJSON(t, policy, dreamSharingPolicy{Version: 1, Generation: 1, Mounts: []dreamSharingMount{}})
	path := writeDreamConfig(t, `{"sharing_policy":"`+policy+`"}`)
	if _, err := loadDreamRuntime(path); err == nil || !strings.Contains(err.Error(), "requires hot_enabled") {
		t.Fatal("sharing policy without HOT was accepted", err)
	}
}
