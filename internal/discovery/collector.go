package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-host/internal/state"
)

type SourceReport struct {
	SourceID            string    `json:"source_id"`
	Complete            bool      `json:"complete"`
	Partial             bool      `json:"partial"`
	HasMore             bool      `json:"has_more"`
	From                time.Time `json:"from"`
	To                  time.Time `json:"to"`
	Messages            int       `json:"messages"`
	AddedEvidence       int       `json:"added_evidence"`
	AddedTopics         int       `json:"added_topics"`
	CollectionError     string    `json:"collection_error,omitempty"`
	ExtractionError     string    `json:"extraction_error,omitempty"`
	Blocked             bool      `json:"blocked,omitempty"`
	ExtractionAvailable int       `json:"extraction_available,omitempty"`
	ExtractionSelected  int       `json:"extraction_selected,omitempty"`
	ExtractionPartial   bool      `json:"extraction_partial,omitempty"`
	TargetVerification  string    `json:"target_verification,omitempty"`
}
type Report struct {
	Sources []SourceReport `json:"sources"`
}
type Collector struct {
	store     *state.Store
	config    Config
	provider  Provider
	extractor Extractor
	now       func() time.Time
}

func NewCollector(store *state.Store, cfg Config, provider Provider, extractor Extractor) *Collector {
	return &Collector{store: store, config: cfg, provider: provider, extractor: extractor, now: time.Now}
}

// Collect is one bounded pass on the Host's serialized Store. Partial ranges
// are retained and retried exactly; no fabricated continuation advances them.
func (c *Collector) Collect(ctx context.Context) (Report, error) {
	out := Report{Sources: []SourceReport{}}
	if c == nil || c.store == nil || c.provider == nil || c.extractor == nil {
		return out, errors.New("discovery collector requires store, provider and extractor")
	}
	if err := c.config.Check(ctx, c.store); err != nil {
		return out, err
	}
	now := c.now().UTC().Truncate(time.Millisecond)
	if err := c.store.PruneDiscoveryCache(ctx, now.Add(-time.Duration(c.config.RetentionDays)*24*time.Hour)); err != nil {
		return out, err
	}
	var failures []error
	for _, selected := range c.config.Sources {
		if err := ctx.Err(); err != nil {
			return out, errors.Join(append(failures, err)...)
		}
		source := selected.Attribution(c.config)
		old, err := c.store.DiscoverySource(ctx, source.ID)
		if err == nil {
			source = old
		} else if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		r := collectionRange(source.Checkpoint, c.config, now)
		report := SourceReport{SourceID: source.ID, From: r.From, To: r.To}
		limits := collectionLimits(c.config)
		if source.LastCollection != nil && !source.LastCollection.Complete && source.LastCollection.Error == "" && source.LastCollection.LimitsFingerprint == limits {
			report.Blocked, report.Partial, report.HasMore = true, true, source.LastCollection.HasMore
			report.CollectionError = "bounded DWS range is incomplete; raise page/message limits and restart or inspect its retained continuation; no identical collection was replayed"
			failures = append(failures, fmt.Errorf("discovery source %s: %s", source.ID, report.CollectionError))
			out.Sources = append(out.Sources, report)
			continue
		}
		result, fetchErr := c.provider.Fetch(ctx, selected, r)
		coverage := result.Coverage
		coverage.From, coverage.To, coverage.At, coverage.Count = r.From, r.To, now, len(result.Evidence)
		coverage.LimitsFingerprint = limits
		if fetchErr != nil {
			coverage.Complete = false
			coverage.Partial = true
			coverage.Error = fetchErr.Error()
			report.CollectionError = coverage.Error
			failures = append(failures, fmt.Errorf("discovery source %s collection: %w", source.ID, fetchErr))
		}
		added, err := c.store.SaveDiscoveryCollection(ctx, source, coverage, result.Evidence)
		if err != nil {
			return out, fmt.Errorf("persist discovery source %s: %w", source.ID, err)
		}
		report.Complete, report.Partial, report.HasMore, report.Messages, report.AddedEvidence = coverage.Complete, coverage.Partial, coverage.HasMore, len(result.Evidence), added
		report.TargetVerification = coverage.TargetVerification
		// Discovery only analyzes complete windows. Partial source evidence stays
		// inspectable without treating a missing context slice as a full topic.
		if coverage.Complete {
			available, err := c.store.DiscoveryEvidenceCount(ctx, source.ID)
			if err != nil {
				return out, err
			}
			evidence, err := c.store.DiscoveryEvidence(ctx, source.ID, MaxTopicEvidence)
			if err != nil {
				return out, err
			}
			digest := EvidenceDigest(evidence)
			report.ExtractionAvailable, report.ExtractionSelected, report.ExtractionPartial = available, len(evidence), available > len(evidence)
			if source.ExtractionDigest != digest || source.ExtractionStatus != "completed" {
				extractionCtx, cancel := context.WithTimeout(ctx, time.Duration(c.config.TimeoutSeconds)*time.Second)
				extracted, extractErr := c.extractor.Extract(extractionCtx, evidence)
				cancel()
				// Preserve bounded analysis coverage alongside the model's audit.
				extracted.Audit, _ = json.Marshal(struct {
					Available int             `json:"available_snapshots"`
					Selected  int             `json:"selected_snapshots"`
					Partial   bool            `json:"partial"`
					Native    json.RawMessage `json:"native"`
				}{available, len(evidence), available > len(evidence), extracted.Audit})
				errText := ""
				if extractErr != nil {
					errText = extractErr.Error()
					report.ExtractionError = errText
					failures = append(failures, fmt.Errorf("discovery source %s extraction: %w", source.ID, extractErr))
				}
				added, err = c.store.SaveTopicExtraction(ctx, source.ID, digest, extracted.Model, extracted.Drafts, extracted.Audit, errText)
				if err != nil {
					return out, err
				}
				report.AddedTopics = added
			}
		}
		out.Sources = append(out.Sources, report)
	}
	if err := c.store.PruneDiscoveryCache(ctx, now.Add(-time.Duration(c.config.RetentionDays)*24*time.Hour)); err != nil {
		return out, errors.Join(append(failures, err)...)
	}
	return out, errors.Join(failures...)
}

func collectionLimits(cfg Config) string {
	return fmt.Sprintf("pages=%d;messages=%d", cfg.PageLimit, cfg.MaxMessages)
}

func collectionRange(cp state.DiscoveryCheckpoint, cfg Config, now time.Time) Range {
	if !cp.PendingFrom.IsZero() && !cp.PendingTo.IsZero() {
		return Range{cp.PendingFrom, cp.PendingTo}
	}
	from := now.Add(-time.Duration(cfg.InitialWindowHours) * time.Hour)
	if !cp.CompleteThrough.IsZero() {
		from = cp.CompleteThrough.Add(-time.Duration(cfg.ReconciliationWindowHours) * time.Hour)
	}
	to := now
	// A stopped Host catches up through bounded windows rather than silently
	// skipping the gap or requesting an unlimited historical archive.
	if max := from.Add(time.Duration(cfg.InitialWindowHours) * time.Hour); to.After(max) {
		to = max
	}
	return Range{from, to}
}

// Loop runs under the existing Host ownership lock. Each report contains only
// source IDs/counts/coverage; unchanged successful polls produce no topics.
func (c *Collector) Loop(ctx context.Context, report func(Report, error)) error {
	if c == nil {
		return errors.New("discovery collector is unavailable")
	}
	for {
		result, err := c.Collect(ctx)
		if report != nil {
			report(result, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(time.Duration(c.config.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
