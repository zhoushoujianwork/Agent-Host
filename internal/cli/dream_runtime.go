package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/dream"
	"agent-host/internal/harness"
	"agent-host/internal/harness/nativepolicy"
	"agent-host/internal/host"
	"agent-host/internal/managedskills"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
	"agent-host/internal/state"
)

type dreamRuntimeConfig struct {
	CleanupRetention     string   `json:"cleanup_retention,omitempty"`
	Enabled              bool     `json:"enabled"`
	HotEnabled           bool     `json:"hot_enabled"`
	DraftSkills          bool     `json:"draft_skills,omitempty"`
	SkillsRoot           string   `json:"skills_root,omitempty"`
	SourceRoot           string   `json:"source_root"`
	MemoryOffloadConfig  string   `json:"memory_offload_config"`
	CacheRoot            string   `json:"cache_root"`
	HotRoot              string   `json:"hot_root,omitempty"`
	SharingPolicy        string   `json:"sharing_policy,omitempty"`
	PersonID             string   `json:"person_id"`
	AgentID              string   `json:"agent_id"`
	AudienceID           string   `json:"audience_id"`
	Scope                string   `json:"scope"`
	Interval             string   `json:"interval"`
	IdleFor              string   `json:"idle_for,omitempty"`
	ActivityPollInterval string   `json:"activity_poll_interval,omitempty"`
	ActivityStateHomes   []string `json:"activity_state_homes,omitempty"`
	Batch                int      `json:"batch"`
	Timeout              string   `json:"timeout"`
}

func dreamRuntimeConfigID(path string, c *dreamRuntimeConfig) string {
	if c == nil {
		return ""
	}
	return channel.Key("dream-config-v1", path, c.PersonID, c.AgentID, c.AudienceID, c.Scope)
}

func startDreamBackground(ctx context.Context, store *state.Store, o options) (func(), <-chan error, error) {
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		if invalidateErr := dream.InvalidateAuthorizationsForConfig(ctx, store.Home(), o.dreamConfig, "runtime", "dream configuration is unavailable"); invalidateErr != nil {
			return nil, nil, errors.Join(err, invalidateErr)
		}
		return nil, nil, err
	}
	if c == nil || !c.Enabled {
		return func() {}, nil, nil
	}
	partition := state.DreamPartition{PersonID: c.PersonID, AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope}
	if c.DraftSkills && (o.dreamValidatedPartition == nil || *o.dreamValidatedPartition != partition) {
		return nil, nil, errors.New("automatic Skill drafts require a matching authenticated channel Dream partition")
	}
	profile, _, err := offloadchannel.Load(c.MemoryOffloadConfig)
	if err != nil {
		return nil, nil, err
	}
	client, err := profile.NewClient()
	if err != nil {
		return nil, nil, err
	}
	library, ok := client.(*offloadexec.Library)
	if !ok || library.Backend() != "claude" || library.Model() == "" {
		return nil, nil, errors.New("dream background requires configured strict tool-free Claude backend")
	}
	interval, _ := time.ParseDuration(c.Interval)
	timeout, _ := time.ParseDuration(c.Timeout)
	idleFor, _ := time.ParseDuration(c.IdleFor)
	pollInterval, _ := time.ParseDuration(c.ActivityPollInterval)
	cycleCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	pstore := store.WithPartition(c.AgentID, c.AudienceID)
	extractor := host.DreamExtractor{Runner: nativepolicy.NewWithLibrary("", library), Model: library.Model(), Backend: library.Backend()}
	runner := dream.Background{Store: pstore, ConfigID: dreamRuntimeConfigID(o.dreamConfig, c), Interval: interval, Timeout: timeout, QuietWindow: idleFor, PollInterval: pollInterval, Probe: dreamActivityProbe(store, c.ActivityStateHomes), Run: func(runCtx context.Context) ([]state.DreamReceipt, error) {
		count, archiveErr := pstore.ArchiveExpiredDream(runCtx, partition, time.Now().UTC(), c.Batch)
		_ = count
		if archiveErr != nil {
			return nil, archiveErr
		}
		if _, retireErr := pstore.RetireExpiredDreamCandidates(runCtx, partition, time.Now().UTC(), c.Batch); retireErr != nil {
			return nil, retireErr
		}
		receipts, processErr := (dream.Lifecycle{Store: pstore, Extractor: extractor, Batch: c.Batch}).Process(runCtx, c.SourceRoot, dream.Destination{PersonID: c.PersonID, Agent: c.AgentID, Audience: c.AudienceID, MemoryScope: c.Scope})
		if c.DraftSkills {
			procedures, listErr := pstore.PendingDreamProcedurePage(runCtx, partition, c.Batch)
			processErr = errors.Join(processErr, listErr)
			if listErr == nil {
				for _, candidate := range procedures {
					_, draftErr := dream.DraftSkillAutomatically(runCtx, pstore, candidate.ID, c.SkillsRoot, partition, time.Now().UTC())
					processErr = errors.Join(processErr, draftErr)
				}
			}
		}
		if c.HotEnabled {
			bundle, exportErr := exportAndPublishRuntimeHOT(runCtx, store, o, c)
			if exportErr != nil {
				return receipts, errors.Join(processErr, exportErr)
			}
			retention, _ := time.ParseDuration(c.CleanupRetention)
			cleanup, cleanupErr := dream.CleanupHOT(runCtx, pstore, c.CacheRoot, bundle, time.Now().UTC(), retention, c.Batch)
			if len(cleanup.Skipped) > 0 {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("dream HOT cleanup skipped: %v", cleanup.Skipped))
			}
			processErr = errors.Join(processErr, cleanupErr)
			if c.HotRoot != "" {
				currentCleanup, currentCleanupErr := dream.CleanupCurrent(runCtx, c.HotRoot, dream.HOTSource{StateHome: store.Home(), ConfigID: dreamRuntimeConfigID(o.dreamConfig, c)}, partition, time.Now().UTC(), retention, c.Batch)
				if len(currentCleanup.Skipped) > 0 {
					currentCleanupErr = errors.Join(currentCleanupErr, fmt.Errorf("dream current cleanup skipped: %v", currentCleanup.Skipped))
				}
				processErr = errors.Join(processErr, currentCleanupErr)
			}
		}
		return receipts, processErr
	}}
	go func() { done <- runner.Serve(cycleCtx); close(done) }()
	return cancel, done, nil
}

func loadDreamRuntime(path string) (*dreamRuntimeConfig, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("dream config must be an absolute clean path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, errors.New("dream config path must have no symlink ancestry")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("dream config must be owner-private regular file")
	}
	if info.Size() > 64<<10 {
		return nil, errors.New("dream config exceeds 64 KiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c dreamRuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return nil, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("dream config must contain one JSON object")
	}
	if c.DraftSkills && !c.Enabled {
		return nil, errors.New("draft_skills requires enabled idle Dream lifecycle")
	}
	if c.HotRoot != "" {
		if !c.HotEnabled {
			return nil, errors.New("dream hot_root requires hot_enabled")
		}
		if err := validateDreamPath(c.HotRoot, true); err != nil {
			return nil, errors.New("dream unified HOT root: " + err.Error())
		}
	}
	if c.SharingPolicy != "" {
		if !c.HotEnabled {
			return nil, errors.New("dream sharing_policy requires hot_enabled")
		}
		if c.HotRoot == "" {
			return nil, errors.New("dream sharing_policy requires hot_root for durable coordination")
		}
		if !filepath.IsAbs(c.SharingPolicy) || filepath.Clean(c.SharingPolicy) != c.SharingPolicy {
			return nil, errors.New("dream sharing policy must be an absolute clean path")
		}
		canonical, pathErr := filepath.EvalSymlinks(c.SharingPolicy)
		if pathErr != nil || canonical != c.SharingPolicy {
			return nil, errors.New("dream sharing policy must exist without symlink ancestry")
		}
		if _, pathErr = privateServicePath(c.SharingPolicy, false); pathErr != nil {
			return nil, errors.New("dream sharing policy: " + pathErr.Error())
		}
	}
	if !c.Enabled && !c.HotEnabled {
		return &c, nil
	}
	if c.PersonID == "" || c.AgentID == "" || c.AudienceID == "" || c.Scope == "" || c.Batch < 1 || c.Batch > 200 {
		return nil, errors.New("enabled dream capability requires exact identity and batch 1..200")
	}
	if c.CleanupRetention == "" {
		c.CleanupRetention = "720h"
	}
	if retention, e := time.ParseDuration(c.CleanupRetention); e != nil || retention < 24*time.Hour {
		return nil, errors.New("dream cleanup retention must be at least 24h")
	}
	if c.HotEnabled {
		if err := validateDreamPath(c.CacheRoot, true); err != nil {
			return nil, errors.New("dream HOT cache root: " + err.Error())
		}
	}
	if c.DraftSkills {
		if c.AudienceID != state.OwnerAudienceID {
			return nil, errors.New("draft_skills requires the trusted owner audience")
		}
		if err := validateDreamPath(c.SkillsRoot, true); err != nil {
			return nil, errors.New("dream managed Skills root: " + err.Error())
		}
		if _, err := managedskills.Load(c.SkillsRoot); err != nil {
			return nil, errors.New("dream managed Skills catalog: " + err.Error())
		}
	}
	if !c.Enabled {
		return &c, nil
	}
	if c.IdleFor == "" {
		c.IdleFor = "60s"
	}
	if c.ActivityPollInterval == "" {
		c.ActivityPollInterval = "1s"
	}
	if err := validateDreamPath(c.SourceRoot, true); err != nil {
		return nil, errors.New("dream source root: " + err.Error())
	}
	if err := validateDreamPath(c.MemoryOffloadConfig, false); err != nil {
		return nil, errors.New("dream memory model config: " + err.Error())
	}
	interval, err := time.ParseDuration(c.Interval)
	if err != nil || interval < time.Minute {
		return nil, errors.New("dream interval must be at least one minute")
	}
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil || timeout <= 0 || timeout > interval {
		return nil, errors.New("dream timeout must be positive and no greater than interval")
	}
	idleFor, err := time.ParseDuration(c.IdleFor)
	if err != nil || idleFor <= 0 {
		return nil, errors.New("dream idle_for must be positive")
	}
	pollInterval, err := time.ParseDuration(c.ActivityPollInterval)
	if err != nil || pollInterval <= 0 {
		return nil, errors.New("dream activity_poll_interval must be positive")
	}
	seenHomes := map[string]bool{}
	for _, home := range c.ActivityStateHomes {
		if !filepath.IsAbs(home) || filepath.Clean(home) != home || seenHomes[home] {
			return nil, errors.New("dream activity_state_homes must contain unique clean absolute paths")
		}
		seenHomes[home] = true
	}
	return &c, nil
}

func dreamActivityProbe(local *state.Store, additional []string) dream.ActivityProbe {
	homes := append([]string{local.Home()}, additional...)
	return func(ctx context.Context) (dream.Activity, error) {
		var result dream.Activity
		seen := map[string]bool{}
		for _, home := range homes {
			if seen[home] {
				continue
			}
			seen[home] = true
			store := local
			closeStore := false
			if home != local.Home() {
				var err error
				store, err = state.OpenForActivityInspection(home)
				if err != nil {
					return result, fmt.Errorf("dream activity state %s: %w", home, err)
				}
				closeStore = true
			}
			activity, err := store.ForegroundActivity(ctx)
			if closeStore {
				err = errors.Join(err, store.Close())
			}
			if err != nil {
				return result, fmt.Errorf("dream activity state %s: %w", home, err)
			}
			result.Busy = result.Busy || activity.Busy
			if activity.LastForeground.After(result.LastForeground) {
				result.LastForeground = activity.LastForeground
			}
		}
		return result, nil
	}
}

func validateDreamPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("must be an absolute clean path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("must exist without symlink ancestry")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("must be an owner-private path of the required kind")
	}
	return nil
}

func validateDreamRuntimeIdentity(c *dreamRuntimeConfig, o options, personID string) error {
	if c == nil || !c.Enabled && !c.HotEnabled {
		return nil
	}
	if c.PersonID != personID || c.AgentID != o.agentID || c.AudienceID != o.audienceID || c.Scope != o.scope {
		return errors.New("dream config does not match trusted runtime person, agent, audience and scope")
	}
	return nil
}

func prepareDreamHOT(store *state.Store, o *options) error {
	o.hotPersonID, o.hotAgentID, o.hotAudienceID, o.hotScope, o.hotRefresh = "", "", "", "", nil
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return err
	}
	if c == nil || !c.HotEnabled {
		return nil
	}
	if c.AgentID != o.agentID || c.AudienceID != o.audienceID || c.Scope != o.scope {
		return nil
	}
	_, err = exportAndPublishRuntimeHOT(context.Background(), store, *o, c)
	if err != nil {
		return err
	}
	o.hotPersonID, o.hotAgentID, o.hotAudienceID, o.hotScope = c.PersonID, c.AgentID, c.AudienceID, c.Scope
	o.hotRefresh = func(ctx context.Context) ([]harness.PresetDocument, string, error) {
		next, e := exportAndPublishRuntimeHOT(ctx, store, *o, c)
		if e != nil {
			return nil, "", e
		}
		return dream.PresetDocuments(next), next.Digest, nil
	}
	return nil
}

func publishDreamCurrent(ctx context.Context, c *dreamRuntimeConfig, configPath, stateHome string, bundle dream.HOTBundle) error {
	if c == nil || c.HotRoot == "" {
		return nil
	}
	_, err := dream.PublishCurrentContext(ctx, c.HotRoot, dream.HOTSource{StateHome: stateHome, ConfigID: dreamRuntimeConfigID(configPath, c)}, bundle)
	return err
}

func exportAndPublishRuntimeHOT(ctx context.Context, store *state.Store, o options, captured *dreamRuntimeConfig) (dream.HOTBundle, error) {
	if captured == nil {
		return dream.HOTBundle{}, errors.New("dream HOT runtime configuration is missing")
	}
	target := state.DreamPartition{PersonID: captured.PersonID, AgentID: captured.AgentID, AudienceID: captured.AudienceID, Scope: captured.Scope}
	if captured.SharingPolicy == "" {
		hadSharedAuthorization, revokeErr := dream.InvalidateAuthorizationIfPresent(ctx, dream.CoordinationTarget{StateHome: store.Home(), Partition: target}, "runtime", "sharing_policy was removed from the trusted target configuration")
		if revokeErr != nil {
			return dream.HOTBundle{}, revokeErr
		}
		if hadSharedAuthorization {
			return dream.HOTBundle{}, errors.New("dream shared authorization was revoked; local HOT cannot replace it implicitly")
		}
		fresh, err := loadDreamRuntime(o.dreamConfig)
		if err != nil || fresh == nil || !fresh.HotEnabled || fresh.SharingPolicy != "" || (state.DreamPartition{PersonID: fresh.PersonID, AgentID: fresh.AgentID, AudienceID: fresh.AudienceID, Scope: fresh.Scope}) != target {
			if err == nil {
				err = errors.New("dream runtime configuration changed; restart is required")
			}
			return dream.HOTBundle{}, err
		}
		bundle, err := exportRuntimeHOT(ctx, store, o, fresh)
		if err == nil {
			_, err = dream.PublishHOT(fresh.CacheRoot, bundle)
		}
		if err == nil {
			err = publishDreamCurrent(ctx, fresh, o.dreamConfig, store.Home(), bundle)
		}
		return bundle, err
	}
	coordinationTarget := dream.CoordinationTarget{StateHome: store.Home(), ConfigID: dreamRuntimeConfigID(o.dreamConfig, captured), ConfigPath: o.dreamConfig, HotRoot: captured.HotRoot, PolicyPath: captured.SharingPolicy, Partition: target}
	bundle, _, err := dream.CoordinateSharedPublish(ctx, coordinationTarget, func() (dream.AuthorizationObservation, dream.SharedHOTBuilder, error) {
		return prepareRuntimeSharedHOT(ctx, store, o, captured)
	}, func(bundle dream.HOTBundle) error {
		if _, publishErr := dream.PublishHOT(captured.CacheRoot, bundle); publishErr != nil {
			return publishErr
		}
		return publishDreamCurrent(ctx, captured, o.dreamConfig, store.Home(), bundle)
	})
	return bundle, err
}

func bindDreamHOT(store *state.Store, o *options, personID string, private bool) error {
	o.hotPersonID, o.hotAgentID, o.hotAudienceID, o.hotScope, o.hotRefresh = "", "", "", "", nil
	if !private {
		return nil
	}
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		if store != nil {
			_, invalidateErr := dream.InvalidateAuthorizationIfPresent(context.Background(), dream.CoordinationTarget{StateHome: store.Home(), Partition: state.DreamPartition{PersonID: personID, AgentID: o.agentID, AudienceID: o.audienceID, Scope: o.scope}}, "runtime", "dream configuration is unavailable for the trusted target")
			err = errors.Join(err, invalidateErr)
		}
		return err
	}
	if c == nil {
		return nil
	}
	if c.PersonID != personID || c.AgentID != o.agentID || c.AudienceID != o.audienceID || c.Scope != o.scope {
		return nil
	}
	if !c.HotEnabled {
		if store == nil {
			return nil
		}
		_, invalidateErr := dream.InvalidateAuthorizationIfPresent(context.Background(), dream.CoordinationTarget{StateHome: store.Home(), Partition: state.DreamPartition{PersonID: personID, AgentID: o.agentID, AudienceID: o.audienceID, Scope: o.scope}}, "runtime", "dream sharing was disabled for the trusted target")
		return invalidateErr
	}
	return prepareDreamHOT(store, o)
}

func validateFullHostChatDream(o options) error {
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil || c == nil || !c.Enabled && !c.HotEnabled {
		return err
	}
	if c.Enabled {
		return errors.New("Full Host Chat supports HOT-only dream configuration; background lifecycle belongs to a channel Store owner")
	}
	if c.PersonID != "local-owner" || c.AudienceID != state.OwnerAudienceID {
		return errors.New("Full Host Chat dream configuration must target the trusted local owner audience")
	}
	return nil
}

func validateFullHostChannelDream(ctx context.Context, store *state.Store, o *options, agents agentCatalog, personID, channelID, accountID, tenantID string) error {
	o.dreamValidatedPartition = nil
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil || c == nil || !c.Enabled && !c.HotEnabled {
		return err
	}
	if personID == "" || c.PersonID != personID {
		return errors.New("dream destination does not match the trusted channel owner")
	}
	audience := state.OwnerAudienceID
	if agents.users != nil {
		identity, resolveErr := agents.users.resolve(userAccount{Channel: channelID, AccountID: accountID, TenantID: tenantID, SenderID: personID}, agents.ownerID)
		err = resolveErr
		if err != nil {
			return err
		}
		if identity != "owner" {
			audience = "user:" + identity
		}
	}
	if c.AudienceID != audience {
		return errors.New("dream destination does not match the owner's trusted private audience")
	}
	a, err := agents.definition(c.AgentID)
	if err != nil {
		return errors.New("dream destination Agent is not present in the channel catalog")
	}
	selected, err := a.options(*o, audience, false, false)
	if err != nil {
		return err
	}
	validScope := c.Scope == selected.scope || c.Scope == selected.defaultScope || c.Scope == selected.workspace
	projects, projectErr := a.projects(ctx, store.WithPartition(c.AgentID, audience))
	if projectErr != nil {
		return projectErr
	}
	for _, project := range projects {
		validScope = validScope || c.Scope == project.Scope
	}
	if !validScope {
		return errors.New("dream destination scope is not selected by the configured channel Agent")
	}
	p := state.DreamPartition{PersonID: c.PersonID, AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope}
	o.dreamValidatedPartition = &p
	o.dreamSharingIdentity = dreamSharingIdentity{Account: &userAccount{Channel: channelID, AccountID: accountID, TenantID: tenantID, SenderID: personID}, OwnerID: personID}
	return nil
}

func runWithDreamBackground(ctx context.Context, store *state.Store, o options, serve func(context.Context) error) error {
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop, done, err := startDreamBackground(serviceCtx, store, o)
	if err != nil {
		return err
	}
	if done == nil {
		return serve(serviceCtx)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serve(serviceCtx) }()
	select {
	case serveErr := <-serveDone:
		stop()
		backgroundErr := <-done
		if errors.Is(backgroundErr, context.Canceled) {
			backgroundErr = nil
		}
		return errors.Join(serveErr, backgroundErr)
	case backgroundErr := <-done:
		cancel()
		return errors.Join(backgroundErr, <-serveDone)
	}
}
