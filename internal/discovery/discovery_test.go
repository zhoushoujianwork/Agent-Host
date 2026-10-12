package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

var testContext = context.Background()

func fixtureConfig(t *testing.T) Config {
	t.Helper()
	return Config{DWSExecutable: os.Args[0], Profile: "corp:owner", IntervalSeconds: 300, InitialWindowHours: 24, ReconciliationWindowHours: 6, RetentionDays: 30, PageLimit: 2, MaxMessages: 200, TimeoutSeconds: 10, Model: "test-model", Sources: []Source{{ConversationID: "selected-conversation", Type: "dm", PeerOpenDingTalkID: "peer-id", ProjectID: "project-a"}}}
}

func fixtureEvidence(cfg Config, at time.Time, message, text string) state.DiscoveryEvidence {
	s := cfg.Sources[0].Attribution(cfg)
	return state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{SourceID: s.ID, Profile: s.Profile, TenantID: s.TenantID, PrincipalID: s.PrincipalID, ConversationID: s.ConversationID, ProjectID: s.ProjectID, MessageID: message, AuthorID: "peer", Author: json.RawMessage(`"Peer"`), Text: text, CreatedAt: at, Availability: "available"})
}

func fixtureStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err = store.RegisterProject(testContext, "project-a", "A", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPrivateConfigAndStrictBounds(t *testing.T) {
	cfg := fixtureConfig(t)
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "discovery.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("public config accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(t.TempDir(), "config-link")
	os.Symlink(path, link)
	if _, err := LoadConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
	for name, mutate := range map[string]func(*Config){"profile alias": func(c *Config) { c.Profile = "default" }, "all chats": func(c *Config) { c.Sources = nil }, "missing peer": func(c *Config) { c.Sources[0].PeerOpenDingTalkID = "" }, "model flag": func(c *Config) { c.Model = "--dangerous" }, "model whitespace": func(c *Config) { c.Model = "test\nmodel" }, "stalled overlap": func(c *Config) { c.ReconciliationWindowHours = c.InitialWindowHours }, "unbounded messages": func(c *Config) { c.MaxMessages = 501 }} {
		t.Run(name, func(t *testing.T) {
			c := fixtureConfig(t)
			mutate(&c)
			if err := c.Check(testContext, nil); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	duplicate := strings.Replace(string(raw), `"profile":"corp:owner"`, `"profile":"corp:owner","PROFILE":"other:owner"`, 1)
	os.WriteFile(path, []byte(duplicate), 0600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("duplicate config field accepted")
	}
}

func envelopeFixture(cfg Config, r Range, complete bool) []byte {
	e := map[string]any{"contractVersion": "im.message-list.v1", "messages": []any{map[string]any{"messageId": "m1", "conversationId": cfg.Sources[0].ConversationID, "senderId": "peer", "sender": "Peer", "text": "The deployment needs a decision.", "createTime": r.From.Add(time.Hour).UnixMilli()}}, "count": 1, "complete": complete, "partial": false, "hasMore": !complete, "pagesFetched": 2, "paginationKnown": true, "queryRange": map[string]any{"startTime": r.From.UnixMilli(), "endTime": r.To.UnixMilli(), "order": "asc", "semantics": "[start,end)"}, "failedCount": 0, "failures": []any{}}
	if !complete {
		e["nextPage"] = map[string]any{"time": r.From.Add(2 * time.Hour).UnixMilli(), "direction": "newer"}
		e["stopReason"] = "page_limit"
		e["truncated"] = true
		e["truncatedByPageLimit"] = true
	}
	b, _ := json.Marshal(e)
	return b
}

func mutateEnvelope(t *testing.T, b []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	mutate(envelope)
	changed, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func failureEnvelopeFixture(t *testing.T, cfg Config, r Range) []byte {
	t.Helper()
	// DWS 1.0.61 (50eb73a0), collectAllChatMessages: an unknown lower
	// hasMore retains collected rows and emits this incomplete failure ledger.
	return mutateEnvelope(t, envelopeFixture(cfg, r, true), func(e map[string]any) {
		e["complete"] = false
		e["partial"] = true
		e["pagesFetched"] = 1
		e["paginationKnown"] = false
		e["stopReason"] = "pagination_error"
		e["failedCount"] = 1
		e["failures"] = []any{map[string]any{"page": 1, "stage": "pagination", "error": "PRIVATE_SOURCE_FAILURE_DETAIL"}}
	})
}

func TestVerifiedEnvelopeCoverageAndDenials(t *testing.T) {
	cfg := fixtureConfig(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	r := Range{now.Add(-24 * time.Hour), now}
	b := envelopeFixture(cfg, r, false)
	out, err := decodeCollection(b, cfg, cfg.Sources[0], r)
	if err != nil || out.Coverage.Complete || !out.Coverage.HasMore || len(out.Evidence) != 1 || !strings.Contains(string(out.Coverage.Ledger), `"nextPage"`) {
		t.Fatalf("lost partial coverage: %+v %v", out, err)
	}
	for name, mutate := range map[string]func(map[string]any){"wrong conversation": func(e map[string]any) { e["messages"].([]any)[0].(map[string]any)["conversationId"] = "other" }, "outside window": func(e map[string]any) {
		e["messages"].([]any)[0].(map[string]any)["createTime"] = now.Add(time.Hour).UnixMilli()
	}, "missing pagination": func(e map[string]any) { delete(e, "nextPage") }, "invented complete": func(e map[string]any) { e["complete"] = true }, "wrong range": func(e map[string]any) { e["queryRange"].(map[string]any)["endTime"] = now.Add(time.Hour).UnixMilli() }, "unknown pagination": func(e map[string]any) { e["paginationKnown"] = false }, "wrong count": func(e map[string]any) { e["count"] = 2 }} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal(b, &m)
			mutate(m)
			changed, _ := json.Marshal(m)
			if _, err := decodeCollection(changed, cfg, cfg.Sources[0], r); err == nil {
				t.Fatal("invalid response admitted")
			}
		})
	}
}

func TestVerifiedFailureLedgerPreservesIncompleteEvidence(t *testing.T) {
	cfg := fixtureConfig(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	r := Range{now.Add(-24 * time.Hour), now}
	unknownPagination := failureEnvelopeFixture(t, cfg, r)
	for _, tc := range []struct {
		name            string
		mutate          func(map[string]any)
		messages        int
		pages           int
		paginationKnown bool
		hasMore         bool
	}{
		{name: "unknown hasMore", messages: 1, pages: 1},
		{name: "missing continuation", messages: 1, pages: 1, paginationKnown: true, hasMore: true, mutate: func(e map[string]any) {
			e["paginationKnown"], e["hasMore"], e["nextPage"] = true, true, nil
		}},
		{name: "first read failure", paginationKnown: true, mutate: func(e map[string]any) {
			e["messages"], e["count"], e["pagesFetched"] = []any{}, 0, 0
			e["partial"], e["paginationKnown"], e["stopReason"] = false, true, "read_failure"
			e["failures"] = []any{map[string]any{"page": 1, "stage": "read", "error": "PRIVATE_SOURCE_FAILURE_DETAIL"}}
		}},
		{name: "time filter failure", messages: 1, pages: 1, paginationKnown: true, mutate: func(e map[string]any) {
			e["paginationKnown"], e["stopReason"] = true, "time_filter_error"
			e["failures"] = []any{map[string]any{"stage": "time_filter", "messageIds": []string{"unparseable-id"}, "error": "PRIVATE_SOURCE_FAILURE_DETAIL"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := unknownPagination
			if tc.mutate != nil {
				b = mutateEnvelope(t, b, tc.mutate)
			}
			out, err := decodeCollection(b, cfg, cfg.Sources[0], r)
			if err != nil {
				t.Fatal(err)
			}
			if out.Coverage.Complete || len(out.Evidence) != tc.messages || out.Coverage.Count != tc.messages || out.Coverage.PagesFetched != tc.pages || out.Coverage.HasMore != tc.hasMore || out.Coverage.Partial != (tc.messages > 0) {
				t.Fatalf("incomplete source facts changed: %+v", out.Coverage)
			}
			var ledger messageEnvelope
			if err := json.Unmarshal(out.Coverage.Ledger, &ledger); err != nil {
				t.Fatal(err)
			}
			if ledger.Complete == nil || *ledger.Complete || ledger.PaginationKnown == nil || *ledger.PaginationKnown != tc.paginationKnown || ledger.FailedCount != 1 || ledger.StopReason == "" || len(ledger.NextPage) > 0 && string(ledger.NextPage) != "null" {
				t.Fatalf("unknown coverage or absent continuation was invented: %s", out.Coverage.Ledger)
			}
			if tc.messages > 0 && (out.Evidence[0].ConversationID != cfg.Sources[0].ConversationID || out.Evidence[0].MessageID != "m1" || out.Evidence[0].Profile != cfg.Profile) {
				t.Fatal("incomplete evidence lost its exact source attribution")
			}
		})
	}
}

func TestVerifiedFailureLedgerStillRejectsMalformedResponses(t *testing.T) {
	cfg := fixtureConfig(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	r := Range{now.Add(-24 * time.Hour), now}
	b := failureEnvelopeFixture(t, cfg, r)
	for name, mutate := range map[string]func(map[string]any){
		"unknown complete coverage": func(e map[string]any) { e["complete"] = true },
		"complete with failure": func(e map[string]any) {
			e["complete"], e["paginationKnown"], e["partial"] = true, true, false
		},
		"negative pages":           func(e map[string]any) { e["pagesFetched"] = -1 },
		"pages over cap":           func(e map[string]any) { e["pagesFetched"] = cfg.PageLimit + 1 },
		"zero pages with evidence": func(e map[string]any) { e["pagesFetched"] = 0 },
		"zero pages without read failure": func(e map[string]any) {
			e["messages"], e["count"], e["pagesFetched"], e["partial"] = []any{}, 0, 0, false
		},
		"wrong failure count":    func(e map[string]any) { e["failedCount"] = 2 },
		"negative failure count": func(e map[string]any) { e["failedCount"] = -1 },
		"null failures":          func(e map[string]any) { e["failures"] = nil },
		"unexplained unknown pagination": func(e map[string]any) {
			e["failures"] = []any{map[string]any{"stage": "read"}}
		},
		"wrong message count": func(e map[string]any) { e["count"] = 2 },
		"wrong conversation": func(e map[string]any) {
			e["messages"].([]any)[0].(map[string]any)["conversationId"] = "unselected"
		},
		"outside range": func(e map[string]any) {
			e["messages"].([]any)[0].(map[string]any)["createTime"] = r.To.UnixMilli()
		},
		"wrong range": func(e map[string]any) {
			e["queryRange"].(map[string]any)["startTime"] = r.From.Add(time.Hour).UnixMilli()
		},
		"duplicate revisions": func(e map[string]any) {
			messages := e["messages"].([]any)
			e["messages"], e["count"] = append(messages, messages[0]), 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := mutateEnvelope(t, b, mutate)
			if _, err := decodeCollection(changed, cfg, cfg.Sources[0], r); err == nil || strings.Contains(err.Error(), "PRIVATE_SOURCE_FAILURE_DETAIL") {
				t.Fatalf("malformed failure ledger accepted or disclosed source data: %v", err)
			}
		})
	}
}

func TestVerifiedSourceTimestampFormats(t *testing.T) {
	want := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	for _, value := range []json.RawMessage{json.RawMessage(`"2026-10-11T10:00:00Z"`), json.RawMessage(`"2026-10-11 18:00:00"`), json.RawMessage(fmt.Sprint(want.Unix())), json.RawMessage(fmt.Sprint(want.UnixMilli()))} {
		got, err := sourceTime(value)
		if err != nil || !got.Equal(want) {
			t.Fatalf("source timestamp %s: got=%v err=%v", value, got, err)
		}
	}
}

type providerFunc func(context.Context, Source, Range) (Collection, error)

func (f providerFunc) Fetch(ctx context.Context, s Source, r Range) (Collection, error) {
	return f(ctx, s, r)
}

type extractorFunc func(context.Context, []state.DiscoveryEvidence) (Extraction, error)

func (f extractorFunc) Extract(ctx context.Context, e []state.DiscoveryEvidence) (Extraction, error) {
	return f(ctx, e)
}

func TestCollectorPartialCheckpointRestartDedupAndIndependentExtraction(t *testing.T) {
	cfg := fixtureConfig(t)
	store := fixtureStore(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	e := fixtureEvidence(cfg, now.Add(-12*time.Hour), "m1", "Discuss a deployment decision")
	var requested []Range
	complete := false
	extractCalls := 0
	provider := providerFunc(func(_ context.Context, _ Source, r Range) (Collection, error) {
		requested = append(requested, r)
		return Collection{Coverage: state.DiscoveryCollection{Complete: complete, Partial: !complete, HasMore: !complete, Ledger: json.RawMessage(`{"hasMore":true,"nextPage":{"time":1234567890123}}`)}, Evidence: []state.DiscoveryEvidence{e}}, nil
	})
	extractor := extractorFunc(func(_ context.Context, e []state.DiscoveryEvidence) (Extraction, error) {
		extractCalls++
		return Extraction{Drafts: []state.TopicDraft{{Title: "Deployment decision", Summary: "A participant requested a deployment decision.", EvidenceIDs: []string{e[0].ID}}}, Model: "test-model", Audit: json.RawMessage(`{"result":"extracted"}`)}, nil
	})
	c := NewCollector(store, cfg, provider, extractor)
	c.now = func() time.Time { return now }
	out, err := c.Collect(testContext)
	if err != nil || out.Sources[0].AddedEvidence != 1 || extractCalls != 0 {
		t.Fatalf("partial intake: %+v %v", out, err)
	}
	source, _ := store.DiscoverySource(testContext, cfg.Sources[0].Attribution(cfg).ID)
	if !source.Checkpoint.CompleteThrough.IsZero() || source.Checkpoint.PendingFrom.IsZero() {
		t.Fatal("partial advanced complete checkpoint")
	}
	complete = true
	// Recreate the collector exactly as a restarted Host would.
	c = NewCollector(store, cfg, provider, extractor)
	c.now = func() time.Time { return now.Add(time.Hour) }
	out, err = c.Collect(testContext)
	if err == nil || !out.Sources[0].Blocked || len(requested) != 1 {
		t.Fatalf("partial range silently replayed after restart: %+v %v", out, err)
	}
	// The operator explicitly raises limits to retry the same retained range.
	cfg.PageLimit = 3
	c = NewCollector(store, cfg, provider, extractor)
	c.now = func() time.Time { return now.Add(time.Hour) }
	out, err = c.Collect(testContext)
	if err != nil || out.Sources[0].AddedEvidence != 0 || out.Sources[0].AddedTopics != 1 || extractCalls != 1 || requested[0] != requested[1] {
		t.Fatalf("restart overlap: %+v %v ranges=%+v", out, err, requested)
	}
	// Move the clock without changing source revisions: no repeated extraction.
	c.now = func() time.Time { return now.Add(2 * time.Hour) }
	provider = providerFunc(func(_ context.Context, _ Source, r Range) (Collection, error) {
		return Collection{Coverage: state.DiscoveryCollection{Complete: true}, Evidence: nil}, nil
	})
	c.provider = provider
	out, err = c.Collect(testContext)
	if err != nil || extractCalls != 1 || out.Sources[0].AddedTopics != 0 {
		t.Fatalf("unchanged source extracted again: %+v %v", out, err)
	}
	topics, _ := store.TopicProposals(testContext, "pending", 10)
	if len(topics) != 1 {
		t.Fatal("topic proposal was duplicated")
	}
}

func TestCollectorFailureRetainsEvidenceAndDoesNotAdvanceCoverage(t *testing.T) {
	cfg := fixtureConfig(t)
	store := fixtureStore(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	e := fixtureEvidence(cfg, now.Add(-time.Hour), "m1", "Substantive source")
	provider := providerFunc(func(_ context.Context, _ Source, _ Range) (Collection, error) {
		return Collection{Coverage: state.DiscoveryCollection{Complete: true}, Evidence: []state.DiscoveryEvidence{e}}, nil
	})
	extractor := extractorFunc(func(context.Context, []state.DiscoveryEvidence) (Extraction, error) {
		return Extraction{Drafts: []state.TopicDraft{}, Audit: json.RawMessage(`{"reason":"native_failed"}`)}, errors.New("native extractor unavailable")
	})
	c := NewCollector(store, cfg, provider, extractor)
	c.now = func() time.Time { return now }
	out, err := c.Collect(testContext)
	if err == nil || out.Sources[0].ExtractionError == "" {
		t.Fatal("extraction failure hidden")
	}
	source, _ := store.DiscoverySource(testContext, e.SourceID)
	if source.ExtractionStatus != "failed" || source.Checkpoint.CompleteThrough != now {
		t.Fatalf("independent extraction state %+v", source)
	}
	retained, _ := store.DiscoveryEvidence(testContext, e.SourceID, 10)
	if len(retained) != 1 {
		t.Fatal("failed extraction lost source evidence")
	}
	c.provider = providerFunc(func(context.Context, Source, Range) (Collection, error) {
		return Collection{}, errors.New("authentication unavailable")
	})
	c.now = func() time.Time { return now.Add(time.Hour) }
	_, err = c.Collect(testContext)
	if err == nil {
		t.Fatal("collection failure hidden")
	}
	source, _ = store.DiscoverySource(testContext, e.SourceID)
	if source.Checkpoint.CompleteThrough != now || source.Checkpoint.PendingTo.IsZero() {
		t.Fatal("failed collection advanced coverage")
	}
}

func TestCollectorReportsBoundedExtractionCoverageAndDeadline(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.MaxMessages = 300
	store := fixtureStore(t)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	evidence := []state.DiscoveryEvidence{}
	for i := 0; i < 201; i++ {
		evidence = append(evidence, fixtureEvidence(cfg, now.Add(-time.Duration(i+1)*time.Minute), fmt.Sprintf("m%d", i), "Bounded discussion evidence"))
	}
	provider := providerFunc(func(context.Context, Source, Range) (Collection, error) {
		return Collection{Coverage: state.DiscoveryCollection{Complete: true}, Evidence: evidence}, nil
	})
	extractor := extractorFunc(func(ctx context.Context, e []state.DiscoveryEvidence) (Extraction, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Duration(cfg.TimeoutSeconds+1)*time.Second {
			t.Fatal("native extraction lacks bounded deadline")
		}
		if len(e) != MaxTopicEvidence {
			t.Fatal("unexpected bounded topic source size")
		}
		return Extraction{Drafts: []state.TopicDraft{}, Model: "test", Audit: json.RawMessage(`{"status":"extracted"}`)}, nil
	})
	c := NewCollector(store, cfg, provider, extractor)
	c.now = func() time.Time { return now }
	out, err := c.Collect(testContext)
	if err != nil || !out.Sources[0].ExtractionPartial || out.Sources[0].ExtractionAvailable != 201 || out.Sources[0].ExtractionSelected != 200 {
		t.Fatalf("bounded analysis looked complete: %+v %v", out, err)
	}
	source, _ := store.DiscoverySource(testContext, cfg.Sources[0].Attribution(cfg).ID)
	if !strings.Contains(string(source.ExtractionAudit), `"partial":true`) {
		t.Fatal("bounded analysis coverage absent from durable audit")
	}
}

type runnerFunc func(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error)

func (f runnerFunc) Run(ctx context.Context, r harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	return f(ctx, r, emit)
}

func TestNativeTopicExtractorRestrictionsAndValidation(t *testing.T) {
	cfg := fixtureConfig(t)
	e := fixtureEvidence(cfg, time.Now().UTC(), "m1", "A substantive issue is unresolved.")
	for name, output := range map[string]string{"valid": `{"drafts":[{"title":"Issue","summary":"A participant raised an unresolved issue.","reported_outcome":"","unresolved_question":"The issue remains unresolved.","evidence_ids":["` + e.ID + `"]}]}`, "chatter": `{"drafts":[]}`, "unknown source": `{"drafts":[{"title":"Issue","summary":"Unresolved","reported_outcome":"","unresolved_question":"","evidence_ids":["unknown"]}]}`, "null": `{"drafts":null}`, "extra field": `{"drafts":[],"execute":true}`, "duplicate field": `{"drafts":[],"drafts":[]}`, "trailing": `{"drafts":[]} {}`} {
		t.Run(name, func(t *testing.T) {
			runner := runnerFunc(func(_ context.Context, r harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
				if !r.Extraction || !r.ToolsDisabled || r.Resume || len(r.MCPServers) > 0 || r.PermissionMode != "" || r.NativeProfile != "" || r.HostInstructions != "" || r.NativeSessionID == "" || !json.Valid([]byte(r.JSONSchema)) {
					t.Fatalf("unsafe extraction request: %+v", r)
				}
				if !strings.Contains(r.Prompt, "untrusted data") || !strings.Contains(r.Prompt, e.ID) {
					t.Fatal("source boundary missing")
				}
				return harness.Result{Status: "completed", ExitCode: 0, Model: "resolved-test", StructuredOutput: json.RawMessage(output)}, nil
			})
			out, err := NewExtractor(runner, cfg.Model).Extract(testContext, []state.DiscoveryEvidence{e})
			if name == "valid" || name == "chatter" {
				if err != nil || !json.Valid(out.Audit) {
					t.Fatalf("valid extraction failed: %+v %v", out, err)
				}
			} else if err == nil {
				t.Fatal("invalid extraction accepted")
			}
		})
	}
	runner := runnerFunc(func(_ context.Context, _ harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
		return harness.Result{}, emit(harness.Observation{Tool: &harness.ToolObservation{Name: "Read"}})
	})
	if _, err := NewExtractor(runner, cfg.Model).Extract(testContext, []state.DiscoveryEvidence{e}); err == nil {
		t.Fatal("extractor tool attempt accepted")
	}
}

func TestDWSSubprocessStableIdentityAndReadOnlyArguments(t *testing.T) {
	cfg := fixtureConfig(t)
	dir := t.TempDir()
	cfg.DWSExecutable = filepath.Join(dir, "dws")
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	r := Range{now.Add(-24 * time.Hour), now}
	profilePath, payloadPath, argsPath := filepath.Join(dir, "profiles.json"), filepath.Join(dir, "payload.json"), filepath.Join(dir, "args.txt")
	os.WriteFile(profilePath, []byte(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), 0600)
	os.WriteFile(payloadPath, envelopeFixture(cfg, r, true), 0600)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nif [ \"$1\" = \"profile\" ]; then cat " + quote(profilePath) + "; exit 0; fi\nprintf '%s\\n' \"$@\" > " + quote(argsPath) + "\ncat " + quote(payloadPath) + "\n"
	os.WriteFile(cfg.DWSExecutable, []byte(script), 0700)
	out, err := NewDWSProvider(cfg).Fetch(testContext, cfg.Sources[0], r)
	if err != nil || len(out.Evidence) != 1 {
		t.Fatalf("DWS fixture read: %+v %v", out, err)
	}
	args, _ := os.ReadFile(argsPath)
	for _, flag := range []string{"+chat-messages", "--profile\ncorp:owner", "--open-dingtalk-id\npeer-id", "--page-all", "--page-limit\n2", "--max-items\n200", "--start", "--end", "--order\nasc"} {
		if !strings.Contains(string(args), flag) {
			t.Fatalf("missing verified argument %s: %s", flag, args)
		}
	}
	for _, flag := range []string{"--yes", "send", "--download-resources", "--chat-query", "--user-query"} {
		if strings.Contains(string(args), flag) {
			t.Fatal("unrequested action or target resolution in DWS invocation")
		}
	}
	groupCfg := cfg
	groupCfg.Sources = append([]Source(nil), cfg.Sources...)
	groupCfg.Sources[0].Type = "group"
	groupCfg.Sources[0].PeerOpenDingTalkID = ""
	if _, err = NewDWSProvider(groupCfg).Fetch(testContext, groupCfg.Sources[0], r); err != nil {
		t.Fatal(err)
	}
	groupArgs, _ := os.ReadFile(argsPath)
	if !strings.Contains(string(groupArgs), "--conversation-id\nselected-conversation") || strings.Contains(string(groupArgs), "--group") {
		t.Fatal("group source used natural target resolution")
	}
	os.Remove(argsPath)
	os.WriteFile(profilePath, []byte(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"other","userId":"owner"}]}`), 0600)
	if _, err = NewDWSProvider(cfg).Fetch(testContext, cfg.Sources[0], r); err == nil {
		t.Fatal("identity drift accepted")
	}
	if _, err = os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("history queried before identity verification")
	}
	os.WriteFile(profilePath, []byte(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), 0600)
	errorScript := "#!/bin/sh\nif [ \"$1\" = \"profile\" ]; then cat " + quote(profilePath) + "; exit 0; fi\nprintf '%s' '{\"error\":{\"code\":4,\"category\":\"auth\",\"reason\":\"auth_refresh_failed\",\"message\":\"PRIVATE_SOURCE_AND_TOKEN_BODY\"}}' >&2\nexit 4\n"
	os.WriteFile(cfg.DWSExecutable, []byte(errorScript), 0700)
	if _, err = NewDWSProvider(cfg).Fetch(testContext, cfg.Sources[0], r); err == nil || strings.Contains(err.Error(), "PRIVATE_SOURCE_AND_TOKEN_BODY") {
		t.Fatalf("raw DWS stderr escaped into error: %v", err)
	}
}

func TestDWSSubprocessFailureLedgerRetainsEvidenceWithoutAdvancingCoverage(t *testing.T) {
	cfg := fixtureConfig(t)
	dir := t.TempDir()
	cfg.DWSExecutable = filepath.Join(dir, "dws")
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	r := Range{now.Add(-24 * time.Hour), now}
	payloadPath := filepath.Join(dir, "incomplete.json")
	if err := os.WriteFile(payloadPath, failureEnvelopeFixture(t, cfg, r), 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nif [ \"$1\" = \"profile\" ]; then printf '%s' '{\"success\":true,\"profiles\":[{\"profile\":\"corp:owner\",\"corpId\":\"corp\",\"userId\":\"owner\"}]}'; exit 0; fi\ncat " + quote(payloadPath) + "\nprintf '%s' '{\"error\":{\"code\":5,\"category\":\"api\",\"reason\":\"chat_messages_incomplete\",\"message\":\"PRIVATE_SOURCE_AND_TOKEN_BODY\"}}' >&2\nexit 5\n"
	if err := os.WriteFile(cfg.DWSExecutable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	provider := NewDWSProvider(cfg)
	result, err := provider.Fetch(testContext, cfg.Sources[0], r)
	if err == nil || len(result.Evidence) != 1 || result.Coverage.Complete || result.Coverage.Error == "" {
		t.Fatalf("valid stdout failure ledger discarded: coverage=%+v evidence=%d err=%v", result.Coverage, len(result.Evidence), err)
	}
	for _, private := range []string{"PRIVATE_SOURCE_AND_TOKEN_BODY", "PRIVATE_SOURCE_FAILURE_DETAIL", "The deployment needs a decision."} {
		if strings.Contains(err.Error(), private) || strings.Contains(result.Coverage.Error, private) {
			t.Fatal("private subprocess output escaped into operational error")
		}
	}
	store := fixtureStore(t)
	extractCalls := 0
	extractor := extractorFunc(func(context.Context, []state.DiscoveryEvidence) (Extraction, error) {
		extractCalls++
		return Extraction{}, errors.New("incomplete source must not be extracted")
	})
	collector := NewCollector(store, cfg, provider, extractor)
	collector.now = func() time.Time { return now }
	report, err := collector.Collect(testContext)
	if err == nil || len(report.Sources) != 1 || report.Sources[0].AddedEvidence != 1 || report.Sources[0].Complete || extractCalls != 0 {
		t.Fatalf("incomplete source was lost or analyzed as complete: %+v %v", report, err)
	}
	source, err := store.DiscoverySource(testContext, cfg.Sources[0].Attribution(cfg).ID)
	if err != nil {
		t.Fatal(err)
	}
	if !source.Checkpoint.CompleteThrough.IsZero() || !source.Checkpoint.PendingFrom.Equal(r.From) || !source.Checkpoint.PendingTo.Equal(r.To) || source.LastCollection == nil || source.LastCollection.Complete {
		t.Fatal("failed source ledger advanced its complete checkpoint")
	}
	var ledger messageEnvelope
	if err := json.Unmarshal(source.LastCollection.Ledger, &ledger); err != nil || ledger.PaginationKnown == nil || *ledger.PaginationKnown || ledger.Complete == nil || *ledger.Complete {
		t.Fatal("unknown source coverage was not durably preserved")
	}
	evidence, err := store.DiscoveryEvidence(testContext, source.ID, 10)
	if err != nil || len(evidence) != 1 || evidence[0].ID != result.Evidence[0].ID {
		t.Fatal("failed source lost its immutable evidence")
	}
	report, err = collector.Collect(testContext)
	if err == nil || report.Sources[0].AddedEvidence != 0 || report.Sources[0].Complete || extractCalls != 0 {
		t.Fatalf("failure retry duplicated or promoted evidence: %+v %v", report, err)
	}
}
