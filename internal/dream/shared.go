package dream

import (
	"errors"
	"sort"
	"strings"
	"time"

	"agent-host/internal/state"
)

type HOTProjectionAccount struct {
	Channel   string `json:"channel"`
	AccountID string `json:"account_id"`
	TenantID  string `json:"tenant_id"`
	SenderID  string `json:"sender_id"`
}

type HOTProjectionSource struct {
	ID         string                `json:"id"`
	StateHome  string                `json:"state_home"`
	Partition  state.DreamPartition  `json:"partition"`
	Account    *HOTProjectionAccount `json:"account,omitempty"`
	LocalOwner bool                  `json:"local_owner,omitempty"`
	Kinds      []string              `json:"kinds"`
}

type HOTProjectionMemory struct {
	SourceID string `json:"source_id"`
	MemoryID string `json:"memory_id"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
}

type HOTProjection struct {
	CanonicalPersonID string                `json:"canonical_person_id"`
	MountID           string                `json:"mount_id"`
	Generation        int                   `json:"generation"`
	PolicyDigest      string                `json:"policy_digest"`
	BindingDigest     string                `json:"binding_digest"`
	Sources           []HOTProjectionSource `json:"sources"`
	Memories          []HOTProjectionMemory `json:"memories"`
}

type SharedHOTSource struct {
	Metadata HOTProjectionSource
	Memories []state.Memory
}

type sharedMemory struct {
	source HOTProjectionSource
	memory state.Memory
}

type sharedMemoryKey struct {
	kind string
	text string
}

type sharedMemoryGroup struct {
	key        sharedMemoryKey
	references []sharedMemory
	sources    []string
	seenSource map[string]bool
}

// ExportSharedHOT produces a deterministic compatibility projection into an
// existing exact target partition. It never mutates or renames source records.
func ExportSharedHOT(target state.DreamPartition, canonicalPersonID, mountID string, generation int, policyDigest, bindingDigest string, sources []SharedHOTSource, now time.Time) (HOTBundle, error) {
	if !validSharedPartition(target) || canonicalPersonID == "" || mountID == "" || generation < 1 || !validProjectionDigest(policyDigest) || !validProjectionDigest(bindingDigest) || len(sources) < 1 || len(sources) > 8 {
		return HOTBundle{}, errors.New("invalid shared HOT projection identity")
	}
	ordered := append([]SharedHOTSource(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Metadata.ID < ordered[j].Metadata.ID })
	seenSources := map[string]bool{}
	var selected []sharedMemory
	projection := &HOTProjection{CanonicalPersonID: canonicalPersonID, MountID: mountID, Generation: generation, PolicyDigest: policyDigest, BindingDigest: bindingDigest}
	for _, source := range ordered {
		if source.Metadata.ID == "" || seenSources[source.Metadata.ID] || source.Metadata.StateHome == "" || (source.Metadata.Account == nil) != source.Metadata.LocalOwner || len(source.Metadata.Kinds) == 0 {
			return HOTBundle{}, errors.New("invalid shared HOT source")
		}
		seenSources[source.Metadata.ID] = true
		kinds := map[string]bool{}
		for _, kind := range source.Metadata.Kinds {
			if kind != "preference" && kind != "project_fact" && kind != "active_task" || kinds[kind] {
				return HOTBundle{}, errors.New("invalid shared HOT source kinds")
			}
			kinds[kind] = true
		}
		sort.Strings(source.Metadata.Kinds)
		projection.Sources = append(projection.Sources, source.Metadata)
		for _, memory := range source.Memories {
			if memory.SenderID != source.Metadata.Partition.PersonID || memory.AgentID != source.Metadata.Partition.AgentID || memory.AudienceID != source.Metadata.Partition.AudienceID || memory.Scope != source.Metadata.Partition.Scope || memory.Status != "active" || memory.SourceType != "dream_reviewed" || memory.Revision < 1 {
				return HOTBundle{}, errors.New("shared HOT memory attribution mismatch")
			}
			if !kinds[memory.Kind] {
				continue
			}
			selected = append(selected, sharedMemory{source: source.Metadata, memory: memory})
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if a.source.ID != b.source.ID {
			return a.source.ID < b.source.ID
		}
		if a.memory.ID != b.memory.ID {
			return a.memory.ID < b.memory.ID
		}
		return a.memory.Revision < b.memory.Revision
	})
	groups := make([]sharedMemoryGroup, 0, len(selected))
	groupIndex := map[sharedMemoryKey]int{}
	for _, item := range selected {
		if item.memory.ExpiresAt != nil && !item.memory.ExpiresAt.After(now) {
			continue
		}
		key := sharedMemoryKey{kind: item.memory.Kind, text: item.memory.Text}
		index, exists := groupIndex[key]
		if !exists {
			index = len(groups)
			groupIndex[key] = index
			groups = append(groups, sharedMemoryGroup{key: key, seenSource: map[string]bool{}})
		}
		group := &groups[index]
		group.references = append(group.references, item)
		if !group.seenSource[item.source.ID] {
			group.seenSource[item.source.ID] = true
			group.sources = append(group.sources, item.source.ID)
		}
	}
	bundle := HOTBundle{Version: "dream-hot-v2", Partition: target, Documents: map[string]string{"USER.md": "", "MEMORY.md": "", "TASKS.md": ""}, Projection: projection}
	var includedReferences []sharedMemory
	for _, group := range groups {
		name := "MEMORY.md"
		if group.key.kind == "preference" {
			name = "USER.md"
		} else if group.key.kind == "active_task" {
			name = "TASKS.md"
		}
		line := "- [source:" + strings.Join(group.sources, ",") + "] " + group.key.text + "\n"
		total := len(bundle.Documents["USER.md"]) + len(bundle.Documents["MEMORY.md"]) + len(bundle.Documents["TASKS.md"])
		if total+len(line) > 12<<10 {
			continue
		}
		bundle.Documents[name] += line
		includedReferences = append(includedReferences, group.references...)
	}
	sort.Slice(includedReferences, func(i, j int) bool {
		a, b := includedReferences[i], includedReferences[j]
		if a.source.ID != b.source.ID {
			return a.source.ID < b.source.ID
		}
		if a.memory.ID != b.memory.ID {
			return a.memory.ID < b.memory.ID
		}
		return a.memory.Revision < b.memory.Revision
	})
	for _, item := range includedReferences {
		bundle.Memories = append(bundle.Memories, HOTManifestEntry{ID: item.source.ID + ":" + item.memory.ID, Revision: item.memory.Revision, Kind: item.memory.Kind, ExpiresAt: item.memory.ExpiresAt})
		projection.Memories = append(projection.Memories, HOTProjectionMemory{SourceID: item.source.ID, MemoryID: item.memory.ID, Revision: item.memory.Revision, Kind: item.memory.Kind})
	}
	bundle.Digest = hotBundleDigest(bundle)
	return bundle, nil
}

func validProjectionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validSharedPartition(partition state.DreamPartition) bool {
	for _, value := range []string{partition.PersonID, partition.AgentID, partition.AudienceID, partition.Scope} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}
