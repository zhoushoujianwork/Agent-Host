package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
	"agent-host/internal/state"
)

type dreamRuntimeConfig struct {
	Enabled             bool   `json:"enabled"`
	HotEnabled          bool   `json:"hot_enabled"`
	SourceRoot          string `json:"source_root"`
	MemoryOffloadConfig string `json:"memory_offload_config"`
	CacheRoot           string `json:"cache_root"`
	PersonID            string `json:"person_id"`
	AgentID             string `json:"agent_id"`
	AudienceID          string `json:"audience_id"`
	Scope               string `json:"scope"`
	Interval            string `json:"interval"`
	Batch               int    `json:"batch"`
	Timeout             string `json:"timeout"`
}

func startDreamBackground(ctx context.Context, store *state.Store, o options) (func(), <-chan error, error) {
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || !c.Enabled {
		return func() {}, nil, nil
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
	cycleCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	pstore := store.WithPartition(c.AgentID, c.AudienceID)
	extractor := host.DreamExtractor{Runner: nativepolicy.NewWithLibrary("", library), Model: library.Model(), Backend: library.Backend()}
	runner := dream.Background{Store: pstore, ConfigID: channel.Key("dream-config-v1", o.dreamConfig, c.PersonID, c.AgentID, c.AudienceID, c.Scope), Interval: interval, Timeout: timeout, Run: func(runCtx context.Context) ([]state.DreamReceipt, error) {
		count, archiveErr := pstore.ArchiveExpiredDream(runCtx, state.DreamPartition{PersonID: c.PersonID, AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope}, time.Now().UTC(), c.Batch)
		_ = count
		if archiveErr != nil {
			return nil, archiveErr
		}
		receipts, processErr := (dream.Lifecycle{Store: pstore, Extractor: extractor, Batch: c.Batch}).Process(runCtx, c.SourceRoot, dream.Destination{PersonID: c.PersonID, Agent: c.AgentID, Audience: c.AudienceID, MemoryScope: c.Scope})
		if c.HotEnabled {
			bundle, exportErr := dream.ExportHOT(runCtx, pstore, state.DreamPartition{PersonID: c.PersonID, AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope}, time.Now().UTC(), c.Batch)
			if exportErr != nil {
				return receipts, errors.Join(processErr, exportErr)
			}
			if _, publishErr := dream.PublishHOT(c.CacheRoot, bundle); publishErr != nil {
				return receipts, errors.Join(processErr, publishErr)
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
	if !c.Enabled && !c.HotEnabled {
		return &c, nil
	}
	if c.PersonID == "" || c.AgentID == "" || c.AudienceID == "" || c.Scope == "" || c.Batch < 1 || c.Batch > 200 {
		return nil, errors.New("enabled dream capability requires exact identity and batch 1..200")
	}
	if c.HotEnabled {
		if err := validateDreamPath(c.CacheRoot, true); err != nil {
			return nil, errors.New("dream HOT cache root: " + err.Error())
		}
	}
	if !c.Enabled {
		return &c, nil
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
	return &c, nil
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
	c, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return err
	}
	if c == nil || !c.HotEnabled {
		return nil
	}
	if c.AgentID != o.agentID || c.AudienceID != o.audienceID || c.Scope != o.scope {
		return errors.New("dream config does not match trusted runtime agent, audience and scope")
	}
	p := state.DreamPartition{PersonID: c.PersonID, AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope}
	bundle, err := dream.ExportHOT(context.Background(), store.WithPartition(c.AgentID, c.AudienceID), p, time.Now().UTC(), c.Batch)
	if err != nil {
		return err
	}
	if _, err = dream.PublishHOT(c.CacheRoot, bundle); err != nil {
		return err
	}
	o.hotPersonID = c.PersonID
	o.hotRefresh = func(ctx context.Context) ([]harness.PresetDocument, string, error) {
		next, e := dream.ExportHOT(ctx, store.WithPartition(c.AgentID, c.AudienceID), p, time.Now().UTC(), c.Batch)
		if e != nil {
			return nil, "", e
		}
		if _, e = dream.PublishHOT(c.CacheRoot, next); e != nil {
			return nil, "", e
		}
		return dream.PresetDocuments(next), next.Digest, nil
	}
	return nil
}
