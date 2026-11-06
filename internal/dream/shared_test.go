package dream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func projectionDigestFixture(value byte) string { return strings.Repeat(string(value), 64) }

func TestHOTV1DigestRemainsByteCompatible(t *testing.T) {
	bundle := HOTBundle{Version: "dream-hot-v1", Partition: state.DreamPartition{PersonID: "p", AgentID: "a", AudienceID: "u", Scope: "s"}, Documents: map[string]string{"USER.md": "u", "MEMORY.md": "m", "TASKS.md": "t"}, Memories: []HOTManifestEntry{{ID: "memory", Revision: 2, Kind: "preference"}}}
	raw, _ := json.Marshal(struct {
		Version   string
		Partition state.DreamPartition
		Documents map[string]string
		Memories  []HOTManifestEntry
	}{bundle.Version, bundle.Partition, bundle.Documents, bundle.Memories})
	want := sha256.Sum256(raw)
	if got := hotBundleDigest(bundle); got != hex.EncodeToString(want[:]) {
		t.Fatalf("v1 digest changed: %s", got)
	}
}

func TestHOTV2AuthorityRevisionChangesDigest(t *testing.T) {
	bundle := HOTBundle{Version: "dream-hot-v2", Partition: state.DreamPartition{PersonID: "p", AgentID: "a", AudienceID: "u", Scope: "s"}, Documents: map[string]string{"USER.md": "u", "MEMORY.md": "", "TASKS.md": ""}, Projection: &HOTProjection{CanonicalPersonID: "p", MountID: "m", Generation: 1, PolicyDigest: strings.Repeat("a", 64), BindingDigest: strings.Repeat("b", 64)}}
	legacyRaw, _ := json.Marshal(struct {
		Version    string
		Partition  state.DreamPartition
		Documents  map[string]string
		Memories   []HOTManifestEntry
		Projection *HOTProjection
	}{bundle.Version, bundle.Partition, bundle.Documents, bundle.Memories, bundle.Projection})
	legacySum := sha256.Sum256(legacyRaw)
	bundle.Digest = hotBundleDigest(bundle)
	if bundle.Digest != hex.EncodeToString(legacySum[:]) {
		t.Fatal("authority-free v2 digest lost phase 2 compatibility", bundle.Digest)
	}
	one, err := ApplyHOTAuthority(bundle, HOTAuthority{ID: "target", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	two, err := ApplyHOTAuthority(bundle, HOTAuthority{ID: "target", Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if one.Digest == two.Digest || one.Documents["USER.md"] != two.Documents["USER.md"] {
		t.Fatal("authority revision did not independently rotate v2 digest")
	}
}

func TestExportSharedHOTDeduplicatesExactTextAndPreservesSourceRefs(t *testing.T) {
	target := state.DreamPartition{PersonID: "raw-target", AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	source := func(id, person, text string) SharedHOTSource {
		partition := state.DreamPartition{PersonID: person, AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
		return SharedHOTSource{Metadata: HOTProjectionSource{ID: id, StateHome: "/state/" + id, Partition: partition, Account: &HOTProjectionAccount{Channel: "dingtalk", AccountID: "app", TenantID: "tenant", SenderID: person}, Kinds: []string{"preference"}}, Memories: []state.Memory{{ID: "memory", SenderID: person, AgentID: "agent", AudienceID: "user:alice", Scope: "scope", Kind: "preference", Text: text, Status: "active", Revision: 1, SourceType: "dream_reviewed", ExpiresAt: &expiry}}}
	}
	one, two := source("one", "raw-one", "same"), source("two", "raw-two", "same")
	one.Memories = append(one.Memories, state.Memory{ID: "unmounted-kind", SenderID: "raw-one", AgentID: "agent", AudienceID: "user:alice", Scope: "scope", Kind: "project_fact", Text: "must not leak", Status: "active", Revision: 1, SourceType: "dream_reviewed"})
	bundle, err := ExportSharedHOT(target, "alice", "mount", 1, projectionDigestFixture('a'), projectionDigestFixture('b'), []SharedHOTSource{two, one}, now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != "dream-hot-v2" || bundle.Documents["USER.md"] != "- [source:one,two] same\n" || len(bundle.Projection.Memories) != 2 || len(bundle.Memories) != 2 {
		t.Fatalf("shared projection mismatch: %+v", bundle)
	}
	changed, err := ExportSharedHOT(target, "alice", "mount", 2, projectionDigestFixture('a'), projectionDigestFixture('b'), []SharedHOTSource{source("one", "raw-one", "same"), source("two", "raw-two", "same")}, now)
	if err != nil || changed.Digest == bundle.Digest || changed.Documents["USER.md"] != bundle.Documents["USER.md"] {
		t.Fatal("authorization generation did not rotate digest without changing documents")
	}
	conflict, err := ExportSharedHOT(target, "alice", "mount", 1, projectionDigestFixture('a'), projectionDigestFixture('b'), []SharedHOTSource{source("one", "raw-one", "first"), source("two", "raw-two", "second")}, now)
	if err != nil || conflict.Documents["USER.md"] != "- [source:one] first\n- [source:two] second\n" {
		t.Fatalf("different source facts were semantically merged: %+v %v", conflict.Documents, err)
	}
}

func TestExportSharedHOTGroupsBeforeBudgetAndDropsExpiredAtCombination(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	target := state.DreamPartition{PersonID: "target", AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
	source := func(id, person string, memories ...state.Memory) SharedHOTSource {
		partition := state.DreamPartition{PersonID: person, AgentID: "agent", AudienceID: "user:alice", Scope: "scope"}
		for index := range memories {
			memories[index].SenderID, memories[index].AgentID, memories[index].AudienceID, memories[index].Scope = person, "agent", "user:alice", "scope"
			memories[index].Kind, memories[index].Status, memories[index].Revision, memories[index].SourceType = "preference", "active", 1, "dream_reviewed"
		}
		return SharedHOTSource{Metadata: HOTProjectionSource{ID: id, StateHome: "/state/" + id, Partition: partition, Account: &HOTProjectionAccount{Channel: "dingtalk", AccountID: "app", TenantID: "tenant", SenderID: person}, Kinds: []string{"preference"}}, Memories: memories}
	}
	validUntil := now.Add(time.Hour)
	expiredAtCombination := now
	one := source("one", "raw-one",
		state.Memory{ID: "a-selected", Text: "selected", ExpiresAt: &validUntil},
		state.Memory{ID: "b-too-large", Text: strings.Repeat("x", 13<<10), ExpiresAt: &validUntil},
		state.Memory{ID: "c-expired", Text: "expired", ExpiresAt: &expiredAtCombination})
	two := source("two", "raw-two", state.Memory{ID: "a-duplicate", Text: "selected", ExpiresAt: &validUntil})
	bundle, err := ExportSharedHOT(target, "alice", "mount", 1, projectionDigestFixture('a'), projectionDigestFixture('b'), []SharedHOTSource{one, two}, now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Documents["USER.md"] != "- [source:one,two] selected\n" || len(bundle.Projection.Memories) != 2 || len(bundle.Memories) != 2 {
		t.Fatalf("grouping, budget, or expiry lost source evidence: %+v", bundle)
	}
}
