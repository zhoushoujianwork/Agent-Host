package discovery

import (
	"agent-host/internal/state"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestOwnerAnsweringConfigDefaultsAndBindings(t *testing.T) {
	cfg := fixtureConfig(t)
	if cfg.OwnerAnswering.Enabled {
		t.Fatal("answering enabled by default")
	}
	if err := cfg.Check(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	valid := func() Config {
		c := fixtureConfig(t)
		c.OwnerAnswering = OwnerAnsweringConfig{Enabled: true, AgentID: "default", Instructions: "Use configured authorized tools for this project only.", Bindings: []AnsweringBinding{{ConversationID: c.Sources[0].ConversationID, PeerOpenDingTalkID: c.Sources[0].PeerOpenDingTalkID, Owner: AnsweringAuthor{ID: "owner-observed", Type: "person"}, Peer: AnsweringAuthor{ID: "peer-observed", Type: "person"}}}}
		return c
	}
	cfg = valid()
	if err := cfg.Check(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	verified := valid()
	verified.OwnerAnswering.Bindings[0].Peer.Type = ""
	verified.OwnerAnswering.Bindings[0].Peer.VerifiedContact = true
	if err := verified.Check(t.Context(), nil); err != nil {
		t.Fatal("explicit verified untyped contact rejected", err)
	}
	for name, change := range map[string]func(*Config){
		"missing agent":    func(c *Config) { c.OwnerAnswering.AgentID = "" },
		"missing scope":    func(c *Config) { c.OwnerAnswering.Instructions = " " },
		"missing bindings": func(c *Config) { c.OwnerAnswering.Bindings = nil },
		"unselected":       func(c *Config) { c.OwnerAnswering.Bindings[0].ConversationID = "another" },
		"wrong peer":       func(c *Config) { c.OwnerAnswering.Bindings[0].PeerOpenDingTalkID = "another" },
		"missing type":     func(c *Config) { c.OwnerAnswering.Bindings[0].Peer.Type = "" },
		"missing ID":       func(c *Config) { c.OwnerAnswering.Bindings[0].Owner.ID = "" },
		"overlap":          func(c *Config) { c.OwnerAnswering.Bindings[0].Owner.ID = c.OwnerAnswering.Bindings[0].Peer.ID },
		"duplicate": func(c *Config) {
			c.OwnerAnswering.Bindings = append(c.OwnerAnswering.Bindings, c.OwnerAnswering.Bindings[0])
		},
		"group": func(c *Config) { c.Sources[0].Type = "group"; c.Sources[0].PeerOpenDingTalkID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			change(&c)
			if err := c.Check(t.Context(), nil); err == nil {
				t.Fatal("invalid delegation accepted")
			}
		})
	}
}

func TestOwnerAnsweringMissingTypeRequiresExplicitDeclaration(t *testing.T) {
	var a AnsweringAuthor
	if err := json.Unmarshal([]byte(`{"id":"peer-open","verified_contact":true}`), &a); err == nil {
		t.Fatal("omitted type silently granted missing-type matching")
	}
	if err := json.Unmarshal([]byte(`{"id":"peer-open","sender_type":null,"verified_contact":true}`), &a); err == nil {
		t.Fatal("null type granted missing-type matching")
	}
	if err := json.Unmarshal([]byte(`{"id":"peer-open","sender_type":"","verified_contact":true}`), &a); err != nil || !a.VerifiedContact || a.Type != "" {
		t.Fatal("explicit verified missing type rejected", err)
	}
}

func TestOwnerAnsweringUntypedProviderFixture(t *testing.T) {
	cfg := fixtureConfig(t)
	s := cfg.Sources[0]
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	r := Range{From: now.Add(-time.Minute), To: now}
	// Synthetic version of the real provider contract: ID/name/text are present;
	// senderType/messageType are genuinely absent, not guessed from the display.
	body := []byte(fmt.Sprintf(`{"contractVersion":"im.message-list.v1","messages":[{"messageId":"fixture-untyped","conversationId":%q,"senderId":%q,"sender":"Fixture Contact","text":"Please check the test service","createTime":%d}],"count":1,"complete":true,"paginationKnown":true,"pagesFetched":1,"failedCount":0,"failures":[],"queryRange":{"startTime":%d,"endTime":%d,"order":"asc","semantics":"[start,end)"}}`, s.ConversationID, s.PeerOpenDingTalkID, now.Add(-time.Second).UnixMilli(), r.From.UnixMilli(), r.To.UnixMilli()))
	col, err := decodeCollection(body, cfg, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.Evidence) != 1 || col.Evidence[0].AuthorID != s.PeerOpenDingTalkID || col.Evidence[0].SenderType != "" || col.Evidence[0].MessageType != "" || col.Evidence[0].Text == "" {
		t.Fatal("untyped provider evidence altered", col)
	}
}

func TestOwnerAnsweringCollectorOnlyPassesCompleteProviderWindows(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "complete"}[complete], func(t *testing.T) {
			cfg := fixtureConfig(t)
			store := fixtureStore(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			calls := 0
			provider := providerFunc(func(_ context.Context, s Source, r Range) (Collection, error) {
				return Collection{Evidence: []state.DiscoveryEvidence{fixtureEvidence(cfg, r.To.Add(-time.Second), "message", "question")}, Coverage: state.DiscoveryCollection{Complete: complete, Partial: !complete}}, nil
			})
			extractor := extractorFunc(func(context.Context, []state.DiscoveryEvidence) (Extraction, error) { return Extraction{}, nil })
			collector := NewCollector(store, cfg, provider, extractor).WithOwnerAnswering(func(_ context.Context, s Source, col Collection) error {
				calls++
				if s != cfg.Sources[0] || len(col.Evidence) != 1 || !col.Coverage.Complete || !col.Coverage.To.Equal(now) {
					t.Fatal("provider evidence/window lost")
				}
				return nil
			})
			collector.now = func() time.Time { return now }
			if _, err := collector.Collect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if complete && calls != 1 || !complete && calls != 0 {
				t.Fatal("partial window reached answering", calls)
			}
		})
	}
}
