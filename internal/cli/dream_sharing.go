package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agent-host/internal/dream"
	"agent-host/internal/state"
)

type dreamSharingIdentity struct {
	Account    *userAccount
	OwnerID    string
	LocalOwner bool
}

type dreamSharingPolicy struct {
	Version    int                 `json:"version"`
	Generation int                 `json:"generation"`
	Mounts     []dreamSharingMount `json:"mounts"`
}

type dreamSharingMount struct {
	ID                string               `json:"id"`
	CanonicalPersonID string               `json:"canonical_person_id"`
	TargetStateHome   string               `json:"target_state_home"`
	Target            state.DreamPartition `json:"target"`
	TargetAccount     *userAccount         `json:"target_account,omitempty"`
	TargetLocalOwner  bool                 `json:"target_local_owner,omitempty"`
	Sources           []dreamSharingSource `json:"sources"`
}

type dreamSharingSource struct {
	ID         string               `json:"id"`
	StateHome  string               `json:"state_home"`
	Partition  state.DreamPartition `json:"partition"`
	Account    *userAccount         `json:"account,omitempty"`
	LocalOwner bool                 `json:"local_owner,omitempty"`
	Kinds      []string             `json:"kinds"`
}

const dreamSharingSourceReadLimit = 200

func loadDreamSharingPolicy(path string) (dreamSharingPolicy, string, error) {
	var policy dreamSharingPolicy
	if err := readServiceJSON(path, &policy); err != nil {
		return policy, "", err
	}
	raw, _ := json.Marshal(policy)
	sum := sha256.Sum256(raw)
	return policy, hex.EncodeToString(sum[:]), nil
}

func loadDreamSharingUsers(agentsConfig string) (*userCatalog, string, error) {
	if agentsConfig == "" {
		return nil, "", errors.New("dream sharing requires a selected Agent catalog")
	}
	users, err := loadUsers(filepath.Dir(agentsConfig))
	if err != nil || users == nil {
		if err == nil {
			err = errors.New("users.json is required")
		}
		return nil, "", err
	}
	raw, _ := json.Marshal(users)
	sum := sha256.Sum256(raw)
	return users, hex.EncodeToString(sum[:]), nil
}

func selectDreamSharingMount(policy dreamSharingPolicy, users *userCatalog, home string, target state.DreamPartition, identity dreamSharingIdentity) (dreamSharingMount, error) {
	if policy.Version != 2 || policy.Generation < 1 || policy.Mounts == nil || len(policy.Mounts) > 32 {
		return dreamSharingMount{}, errors.New("dream sharing policy requires version 2, positive generation and at most 32 mounts")
	}
	if !validDreamSharingPartition(target) {
		return dreamSharingMount{}, errors.New("dream sharing runtime partition is invalid")
	}
	seenMounts := map[string]bool{}
	var matched []dreamSharingMount
	for _, mount := range policy.Mounts {
		if !agentIdentifier.MatchString(mount.ID) || seenMounts[mount.ID] || !agentIdentifier.MatchString(mount.CanonicalPersonID) || !validDreamSharingPartition(mount.Target) || (mount.TargetAccount != nil) == mount.TargetLocalOwner || len(mount.Sources) < 1 || len(mount.Sources) > 8 {
			return dreamSharingMount{}, errors.New("invalid dream sharing mount")
		}
		seenMounts[mount.ID] = true
		if err := validateDreamPath(mount.TargetStateHome, true); err != nil {
			return dreamSharingMount{}, fmt.Errorf("dream sharing target state home: %w", err)
		}
		if !catalogHasCanonical(users, mount.CanonicalPersonID) {
			return dreamSharingMount{}, errors.New("dream sharing canonical person is not configured")
		}
		if err := validateDreamSharingTargetBinding(mount, users); err != nil {
			return dreamSharingMount{}, err
		}
		seenSources := map[string]bool{}
		for _, source := range mount.Sources {
			if !agentIdentifier.MatchString(source.ID) || seenSources[source.ID] || !validDreamSharingPartition(source.Partition) || (source.Account != nil) == source.LocalOwner || len(source.Kinds) == 0 {
				return dreamSharingMount{}, errors.New("invalid dream sharing source")
			}
			seenSources[source.ID] = true
			if err := validateDreamPath(source.StateHome, true); err != nil {
				return dreamSharingMount{}, fmt.Errorf("dream sharing source state home: %w", err)
			}
			if err := validateDreamSharingSource(mount, source, users); err != nil {
				return dreamSharingMount{}, err
			}
		}
		if mount.TargetStateHome == home && mount.Target == target {
			matched = append(matched, mount)
		}
	}
	if len(matched) != 1 {
		return dreamSharingMount{}, errors.New("dream sharing runtime must match exactly one mount")
	}
	if err := validateDreamSharingTargetIdentity(matched[0], identity); err != nil {
		return dreamSharingMount{}, err
	}
	return matched[0], nil
}

func validateDreamSharingTargetBinding(mount dreamSharingMount, users *userCatalog) error {
	if mount.TargetLocalOwner {
		if mount.CanonicalPersonID != "owner" || mount.Target.AudienceID != state.OwnerAudienceID || mount.Target.PersonID != "local-owner" {
			return errors.New("dream sharing local target is not the trusted reserved owner")
		}
		return nil
	}
	if mount.TargetAccount.validate() != nil {
		return errors.New("dream sharing target account is invalid")
	}
	if mount.Target.PersonID != mount.TargetAccount.SenderID && mount.Target.PersonID != mount.CanonicalPersonID {
		return errors.New("dream sharing target person is not the bound raw or canonical identity")
	}
	canonical, explicit := users.bindings[*mount.TargetAccount]
	if !explicit || canonical != mount.CanonicalPersonID {
		return errors.New("dream sharing target account lacks an explicit canonical binding")
	}
	expectedAudience := "user:" + canonical
	if canonical == "owner" {
		expectedAudience = state.OwnerAudienceID
	}
	if mount.Target.AudienceID != expectedAudience {
		return errors.New("dream sharing target audience does not match the canonical binding")
	}
	return nil
}

func validateDreamSharingTargetIdentity(mount dreamSharingMount, identity dreamSharingIdentity) error {
	if mount.TargetLocalOwner {
		if !identity.LocalOwner {
			return errors.New("dream sharing target is not trusted local owner admission")
		}
		return nil
	}
	if identity.LocalOwner || identity.Account == nil || *identity.Account != *mount.TargetAccount {
		return errors.New("dream sharing target account does not match trusted channel admission")
	}
	if mount.CanonicalPersonID == "owner" && (identity.OwnerID == "" || mount.TargetAccount.SenderID != identity.OwnerID) {
		return errors.New("dream sharing owner binding changed")
	}
	return nil
}

func validateDreamSharingSource(mount dreamSharingMount, source dreamSharingSource, users *userCatalog) error {
	kinds := map[string]bool{}
	for _, kind := range source.Kinds {
		if kind != "preference" && kind != "project_fact" && kind != "active_task" || kinds[kind] {
			return errors.New("dream sharing source kinds are invalid")
		}
		kinds[kind] = true
	}
	if source.Partition.AgentID != mount.Target.AgentID {
		return errors.New("dream sharing cannot cross Agent identity")
	}
	if source.Partition.Scope != mount.Target.Scope && (kinds["project_fact"] || kinds["active_task"]) {
		return errors.New("dream sharing facts and tasks cannot cross scope")
	}
	if source.LocalOwner {
		if mount.CanonicalPersonID != "owner" || source.Partition.PersonID != "local-owner" || source.Partition.AudienceID != state.OwnerAudienceID {
			return errors.New("dream sharing local source is not the reserved owner")
		}
		return nil
	}
	if source.Account.validate() != nil || users.bindings[*source.Account] != mount.CanonicalPersonID {
		return errors.New("dream sharing source account lacks the same explicit canonical binding")
	}
	if source.Partition.PersonID != source.Account.SenderID && source.Partition.PersonID != mount.CanonicalPersonID {
		return errors.New("dream sharing source person is not the bound raw or canonical identity")
	}
	expected := map[string]bool{"user:" + mount.CanonicalPersonID: true, "user:u-" + accountDigest(*source.Account)[:32]: true}
	if mount.CanonicalPersonID == "owner" {
		expected[state.OwnerAudienceID] = true
	}
	if !expected[source.Partition.AudienceID] {
		return errors.New("dream sharing source audience is not an allowed private identity")
	}
	return nil
}

func catalogHasCanonical(users *userCatalog, id string) bool {
	for _, user := range users.Users {
		if user.ID == id {
			return true
		}
	}
	return false
}

func validDreamSharingPartition(partition state.DreamPartition) bool {
	for _, value := range []string{partition.PersonID, partition.AgentID, partition.AudienceID, partition.Scope} {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func exportRuntimeHOT(ctx context.Context, store *state.Store, o options, config *dreamRuntimeConfig) (dream.HOTBundle, error) {
	target := state.DreamPartition{PersonID: config.PersonID, AgentID: config.AgentID, AudienceID: config.AudienceID, Scope: config.Scope}
	if config.SharingPolicy != "" {
		return dream.HOTBundle{}, errors.New("shared HOT export requires durable coordinated publication")
	}
	return dream.ExportHOT(ctx, store.WithPartition(config.AgentID, config.AudienceID), target, time.Now().UTC(), config.Batch)
}

func prepareRuntimeSharedHOT(ctx context.Context, store *state.Store, o options, captured *dreamRuntimeConfig) (dream.AuthorizationObservation, dream.SharedHOTBuilder, error) {
	target := state.DreamPartition{PersonID: captured.PersonID, AgentID: captured.AgentID, AudienceID: captured.AudienceID, Scope: captured.Scope}
	config, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return dream.AuthorizationObservation{}, nil, dream.RevokeAuthorizationError(err)
	}
	if config == nil || !config.HotEnabled || config.SharingPolicy == "" || config.HotRoot != captured.HotRoot || config.SharingPolicy != captured.SharingPolicy || config.CacheRoot != captured.CacheRoot || (state.DreamPartition{PersonID: config.PersonID, AgentID: config.AgentID, AudienceID: config.AudienceID, Scope: config.Scope}) != target {
		return dream.AuthorizationObservation{}, nil, dream.RevokeAuthorizationError(errors.New("dream shared runtime configuration changed or sharing was disabled"))
	}
	policy, policyDigest, err := loadDreamSharingPolicy(config.SharingPolicy)
	if err != nil {
		return dream.AuthorizationObservation{}, nil, dream.RevokeAuthorizationError(err)
	}
	users, bindingDigest, err := loadDreamSharingUsers(o.agentsConfig)
	if err != nil {
		return dream.AuthorizationObservation{}, nil, dream.RevokeAuthorizationError(err)
	}
	mount, err := selectDreamSharingMount(policy, users, store.Home(), target, o.dreamSharingIdentity)
	if err != nil {
		return dream.AuthorizationObservation{}, nil, dream.RevokeAuthorizationError(err)
	}
	observation := dream.AuthorizationObservation{Generation: policy.Generation, PolicyDigest: policyDigest, BindingDigest: bindingDigest, MountID: mount.ID, CanonicalPersonID: mount.CanonicalPersonID, LocalTarget: mount.TargetLocalOwner}
	build := func(authority dream.HOTAuthority) (dream.HOTBundle, error) {
		readNow := time.Now().UTC()
		shared := make([]dream.SharedHOTSource, 0, len(mount.Sources))
		for _, source := range mount.Sources {
			memories, readErr := state.ReadDreamMemoriesSnapshot(ctx, source.StateHome, source.Partition, readNow, dreamSharingSourceReadLimit)
			if readErr != nil {
				return dream.HOTBundle{}, fmt.Errorf("dream sharing source %s: %w", source.ID, readErr)
			}
			metadata := dream.HOTProjectionSource{ID: source.ID, StateHome: source.StateHome, Partition: source.Partition, LocalOwner: source.LocalOwner, Kinds: append([]string(nil), source.Kinds...)}
			if source.Account != nil {
				metadata.Account = &dream.HOTProjectionAccount{Channel: source.Account.Channel, AccountID: source.Account.AccountID, TenantID: source.Account.TenantID, SenderID: source.Account.SenderID}
			}
			shared = append(shared, dream.SharedHOTSource{Metadata: metadata, Memories: memories})
		}
		afterConfig, reloadErr := loadDreamRuntime(o.dreamConfig)
		if reloadErr != nil {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(reloadErr)
		}
		if afterConfig == nil || !afterConfig.HotEnabled || afterConfig.SharingPolicy == "" || afterConfig.HotRoot != config.HotRoot || afterConfig.SharingPolicy != config.SharingPolicy || afterConfig.CacheRoot != config.CacheRoot || (state.DreamPartition{PersonID: afterConfig.PersonID, AgentID: afterConfig.AgentID, AudienceID: afterConfig.AudienceID, Scope: afterConfig.Scope}) != target {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(errors.New("dream shared runtime configuration changed during source read"))
		}
		afterPolicy, afterPolicyDigest, reloadErr := loadDreamSharingPolicy(afterConfig.SharingPolicy)
		if reloadErr != nil {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(reloadErr)
		}
		afterUsers, afterBindingDigest, reloadErr := loadDreamSharingUsers(o.agentsConfig)
		if reloadErr != nil {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(reloadErr)
		}
		if policyDigest != afterPolicyDigest || bindingDigest != afterBindingDigest {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(errors.New("dream sharing authorization changed during source read"))
		}
		if _, reloadErr = selectDreamSharingMount(afterPolicy, afterUsers, store.Home(), target, o.dreamSharingIdentity); reloadErr != nil {
			return dream.HOTBundle{}, dream.RevokeAuthorizationError(reloadErr)
		}
		bundle, buildErr := dream.ExportSharedHOT(target, mount.CanonicalPersonID, mount.ID, policy.Generation, policyDigest, bindingDigest, shared, time.Now().UTC())
		if buildErr != nil {
			return dream.HOTBundle{}, buildErr
		}
		return dream.ApplyHOTAuthority(bundle, authority)
	}
	return observation, build, nil
}
