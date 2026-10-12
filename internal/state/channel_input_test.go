package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/event"
)

func attachmentInput(id string) ChannelMessage {
	m := channelInput(id)
	m.MessageType, m.Text = "picture", "Describe this picture."
	m.Input = &ChannelInput{
		Attachments: []ChannelAttachment{{Kind: "image", Name: "diagram.png", MediaType: "image/png", Size: 100}},
		Quote:       &ChannelQuote{MessageID: "quoted-message", MessageType: "text", Body: "The current architecture", SummaryOnly: true},
	}
	return m
}

func downloadedAttachments(m ChannelMessage) []ChannelAttachment {
	attachments := append([]ChannelAttachment(nil), m.Input.Attachments...)
	for i := range attachments {
		attachments[i].Path = "/synthetic/attachments/diagram.png"
		attachments[i].SHA256 = strings.Repeat("a", 64)
		attachments[i].ContentType, attachments[i].Bytes = "image/png", 100
	}
	return attachments
}

func TestChannelInputCapabilitiesStayPrivateAndSourceUsesSavedMaterial(t *testing.T) {
	s, _ := testStore(t)
	m := attachmentInput("picture")
	const code = "synthetic-private-download-capability"
	// Transport callers cannot predeclare a trusted local download.
	m.Input.Attachments = downloadedAttachments(m)
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, []string{code}); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	queued, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || queued.Input.Attachments[0].Path != "" || queued.Input.Attachments[0].SHA256 != "" || queued.Input.Attachments[0].Bytes != 0 {
		t.Fatal("receipt trusted caller-supplied file evidence", queued, err)
	}
	if _, err := s.ChannelInputCapabilities(testContext, m.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("queued message exposed download capability", err)
	}
	if err := s.SaveChannelAttachments(testContext, m.ID, downloadedAttachments(m)); err == nil {
		t.Fatal("queued message accepted a download result")
	}
	m = claimChannel(t, s, m.Profile)
	if codes, err := s.ChannelInputCapabilities(testContext, m.ID); err != nil || !reflect.DeepEqual(codes, []string{code}) {
		t.Fatal("claimed input lost protected capability", codes, err)
	}
	attachments := downloadedAttachments(m)
	if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err != nil {
		t.Fatal(err)
	}
	run := channelRun(t, s, m)
	run.Source = &RunSource{Input: &ChannelInput{Quote: &ChannelQuote{Body: "forged caller source"}}}
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Run(testContext, run.ID)
	if err != nil || stored.Source == nil || stored.Source.Input == nil || !reflect.DeepEqual(stored.Source.Input.Attachments, attachments) || !reflect.DeepEqual(stored.Source.Input.Quote, m.Input.Quote) {
		t.Fatal("run did not inherit durable received and downloaded evidence", stored, err)
	}
	if _, err := s.ChannelInputCapabilities(testContext, m.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("executed input retained capability access", err)
	}
	if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err == nil {
		t.Fatal("download evidence changed after native run creation")
	}
	m, err = s.ChannelMessage(testContext, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ChannelMessages(testContext, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{m, listed, stored} {
		raw, err := json.Marshal(value)
		if err != nil || bytes.Contains(raw, []byte(code)) || bytes.Contains(raw, []byte("input_capabilities")) || bytes.Contains(raw, []byte(testReplyURL)) {
			t.Fatal("public records leaked a transport capability", err)
		}
	}
}

func TestChannelInputDuplicateKeepsOriginalCapabilitiesAndDownloadedEvidence(t *testing.T) {
	s, _ := testStore(t)
	m := attachmentInput("duplicate")
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, []string{"original-code"}); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	m = claimChannel(t, s, m.Profile)
	attachments := downloadedAttachments(m)
	if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err != nil {
		t.Fatal(err)
	}
	duplicate := attachmentInput(m.ID)
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, "https://example.invalid/replaced-reply", []string{"refreshed-code"}); err != nil || inserted {
		t.Fatal("redelivery was not an idempotent duplicate", inserted, err)
	}
	if codes, err := s.ChannelInputCapabilities(testContext, m.ID); err != nil || !reflect.DeepEqual(codes, []string{"original-code"}) {
		t.Fatal("redelivery replaced durable credentials", codes, err)
	}
	saved, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || saved.Status != "claimed" || !reflect.DeepEqual(saved.Input.Attachments, attachments) {
		t.Fatal("duplicate changed downloaded evidence", saved, err)
	}
	duplicate.Input.Attachments[0].Name = "different.png"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, testReplyURL, []string{"another-code"}); err == nil || inserted {
		t.Fatal("changed attachment metadata was treated as a duplicate")
	}
	duplicate = attachmentInput(m.ID)
	duplicate.Input.Quote.Body = "different quotation"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, testReplyURL, []string{"another-code"}); err == nil || inserted {
		t.Fatal("changed quotation was treated as a duplicate")
	}
}

func TestChannelInputRejectsIncompleteOrChangedDownloadEvidence(t *testing.T) {
	s, _ := testStore(t)
	m := attachmentInput("download")
	if _, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, []string{"code"}); err != nil {
		t.Fatal(err)
	}
	claimChannel(t, s, m.Profile)
	for name, change := range map[string]func(*ChannelAttachment){
		"changed name": func(a *ChannelAttachment) { a.Name = "other.png" },
		"changed kind": func(a *ChannelAttachment) { a.Kind = "file" },
		"changed size": func(a *ChannelAttachment) { a.Size++ },
		"missing path": func(a *ChannelAttachment) { a.Path = "" },
		"missing hash": func(a *ChannelAttachment) { a.SHA256 = "" },
		"missing type": func(a *ChannelAttachment) { a.ContentType = "" },
		"empty bytes":  func(a *ChannelAttachment) { a.Bytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			attachments := downloadedAttachments(m)
			change(&attachments[0])
			if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err == nil {
				t.Fatal("invalid download evidence accepted")
			}
		})
	}
	if err := s.SaveChannelAttachments(testContext, m.ID, nil); err == nil {
		t.Fatal("missing attachment accepted")
	}
}

func TestChannelInputRecoveryPreservesQueuedMediaWithoutReplayingClaimedMedia(t *testing.T) {
	s, home := testStore(t)
	for _, id := range []string{"claimed", "queued"} {
		if _, err := s.EnqueueChannelMessage(testContext, attachmentInput(id), testReplyURL, []string{"code-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	claimed := claimChannel(t, s, "profile")
	if err := s.SaveChannelAttachments(testContext, claimed.ID, downloadedAttachments(claimed)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	unknown, err := restarted.ChannelMessage(testContext, claimed.ID)
	if err != nil || unknown.Status != "unknown" || !reflect.DeepEqual(unknown.Input.Attachments, downloadedAttachments(claimed)) {
		t.Fatal("interrupted media was replayed or lost its evidence", unknown, err)
	}
	if _, err := restarted.ChannelInputCapabilities(testContext, unknown.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unknown task regained download credentials", err)
	}
	next := claimChannel(t, restarted, "profile")
	if next.ID != "queued" || !reflect.DeepEqual(next.Input, attachmentInput("queued").Input) {
		t.Fatal("queued media was changed during recovery", next)
	}
	if codes, err := restarted.ChannelInputCapabilities(testContext, next.ID); err != nil || !reflect.DeepEqual(codes, []string{"code-queued"}) {
		t.Fatal("queued media lost its download capability", codes, err)
	}
	if _, err := restarted.ClaimNextChannelMessage(testContext, "profile"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("recovery requeued claimed media", err)
	}
}

func TestChannelInputLegacyUnsupportedRedeliveryDoesNotRewriteOrExecute(t *testing.T) {
	for _, kind := range []string{"picture", "file", "richText", "markdown"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			old := channelInput("legacy")
			old.MessageType, old.Text = kind, ""
			enqueueChannel(t, s, old)
			claimChannel(t, s, old.Profile)
			if err := s.FinishChannelMessage(testContext, old.ID, "failed", "unsupported message type"); err != nil {
				t.Fatal(err)
			}
			var before, after []byte
			if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", old.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			upgraded := attachmentInput(old.ID)
			upgraded.MessageType = kind
			codes := []string{"new-code"}
			if kind == "markdown" {
				upgraded.Text = "Forwarded Markdown message"
				upgraded.Input = &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "Previously forwarded text", Title: "Forwarded discussion"}}
				codes = nil
			}
			if inserted, err := s.EnqueueChannelMessage(testContext, upgraded, testReplyURL, codes); err != nil || inserted {
				t.Fatal("upgraded redelivery did not remain a duplicate", inserted, err)
			}
			if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", old.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatal("upgraded redelivery rewrote legacy evidence", err)
			}
			if _, err := s.ClaimNextChannelMessage(testContext, old.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("upgraded redelivery replayed the old input", err)
			}
		})
	}
}

func TestMarkdownRedeliveryCollisionExceptionRequiresLegacyRejection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		status   string
		cause    string
		oldInput *ChannelInput
		newInput *ChannelInput
	}{
		{name: "queued"},
		{name: "completed", status: "completed"},
		{name: "other failure", status: "failed", cause: "invalid text length"},
		{name: "previous text", text: "Earlier parsed text", status: "failed", cause: "unsupported message type"},
		{name: "previous material", status: "failed", cause: "unsupported message type", oldInput: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "Earlier parsed material"}}},
		{name: "missing new material", status: "failed", cause: "unsupported message type", newInput: &ChannelInput{}},
		{name: "wrong new quote type", status: "failed", cause: "unsupported message type", newInput: &ChannelInput{Quote: &ChannelQuote{MessageType: "text", Body: "Forwarded text"}}},
		{name: "empty new quote", status: "failed", cause: "unsupported message type", newInput: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			old := channelInput("markdown-collision")
			old.MessageType, old.Text, old.Input = "markdown", tc.text, tc.oldInput
			enqueueChannel(t, s, old)
			if tc.status != "" {
				claimChannel(t, s, old.Profile)
				if err := s.FinishChannelMessage(testContext, old.ID, tc.status, tc.cause); err != nil {
					t.Fatal(err)
				}
			}
			upgraded := old
			upgraded.Text = "Forwarded Markdown message"
			upgraded.Input = tc.newInput
			if upgraded.Input == nil {
				upgraded.Input = &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "Forwarded text"}}
			}
			if inserted, err := s.EnqueueChannelMessage(testContext, upgraded, testReplyURL); err == nil || inserted {
				t.Fatal("nonlegacy markdown collision was accepted", inserted, err)
			}
		})
	}
}

func TestChannelInputMigrationFromV10PreservesRawHistoryAndMemoryEvidence(t *testing.T) {
	s, home := priorSchemaV11Store(t)
	m := channelInput("historical")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	e := event.Event{RunID: r.ID, SessionID: r.SessionID, Type: "fixture.evidence", Message: "Historical execution evidence"}
	if err := s.AppendEvent(testContext, &e); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, r.ID, "completed", "Historical answer", "model", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "historical-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"SELECT payload FROM sessions ORDER BY id", "SELECT payload FROM runs ORDER BY id",
		"SELECT payload FROM channel_messages ORDER BY id", "SELECT payload FROM events ORDER BY run_id,seq",
		"SELECT payload FROM memories ORDER BY id", "SELECT payload FROM memory_revisions ORDER BY memory_id,revision",
		"SELECT memory_id || ':' || run_id FROM memory_evidence ORDER BY memory_id,run_id",
	}
	snapshot := func(store *Store) [][][]byte {
		t.Helper()
		var result [][][]byte
		for _, query := range queries {
			rows, err := store.db.Query(query)
			if err != nil {
				t.Fatal(err)
			}
			var records [][]byte
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				records = append(records, raw)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			result = append(result, records)
		}
		return result
	}
	before := snapshot(s)
	if _, err := s.db.Exec("ALTER TABLE channel_messages DROP COLUMN input_capabilities"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=10"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("schema did not migrate to attachment support", version, err)
	}
	if after := snapshot(migrated); !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote historical payloads or source evidence")
	}
	var caps string
	if err := migrated.db.QueryRow("SELECT input_capabilities FROM channel_messages WHERE id=?", m.ID).Scan(&caps); err != nil || caps != "[]" {
		t.Fatal("legacy input received synthetic credentials", caps, err)
	}
	saved, err := migrated.Run(testContext, r.ID)
	if err != nil || saved.Source == nil || saved.Source.Input != nil || saved.Source.MessageID != m.MessageID {
		t.Fatal("historical run lost source readability", saved, err)
	}
	if memory, err := migrated.Memory(testContext, "historical-memory"); err != nil || memory.SourceRunID != r.ID || memory.Text != r.Input {
		t.Fatal("historical memory is no longer readable", memory, err)
	}
}

func TestChannelInputMigrationFromV11PreservesCapabilitiesAndSourceEvidence(t *testing.T) {
	s, home := priorSchemaV11Store(t)
	m := attachmentInput("historical-media")
	const capability = "synthetic-preserved-v11-capability"
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, []string{capability}); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	m = claimChannel(t, s, m.Profile)
	attachments := downloadedAttachments(m)
	if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err != nil {
		t.Fatal(err)
	}
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	e := event.Event{RunID: r.ID, SessionID: r.SessionID, Type: "fixture.evidence", Message: "Existing media execution evidence"}
	if err := s.AppendEvent(testContext, &e); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, r.ID, "completed", "Existing native answer", "model", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "historical-media-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "Existing formatted channel answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	// Snapshot every pre-existing operational table, including private transport
	// fields, provenance indexes, and memory annotation/FTS evidence.
	snapshot := func(store *Store) map[string][][]any {
		t.Helper()
		rows, err := store.db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, table)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		result := map[string][][]any{}
		for _, table := range tables {
			// Names come only from SQLite, and quoting preserves unusual identifiers.
			rows, err := store.db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY 1`)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var records [][]any
			for rows.Next() {
				record := make([]any, len(columns))
				target := make([]any, len(columns))
				for i := range record {
					target[i] = &record[i]
				}
				if err := rows.Scan(target...); err != nil {
					t.Fatal(err)
				}
				for i, value := range record {
					if raw, ok := value.([]byte); ok {
						record[i] = bytes.Clone(raw)
					}
				}
				records = append(records, record)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			result[table] = records
		}
		return result
	}
	before := snapshot(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("schema did not migrate to current version", version, err)
	}
	after := snapshot(migrated)
	for table, records := range before {
		if !reflect.DeepEqual(records, after[table]) {
			t.Fatalf("additive migration changed existing table %s", table)
		}
	}
	saved, err := migrated.ChannelMessage(testContext, m.ID)
	if err != nil || saved.ReplyText != "Existing formatted channel answer" || saved.ReplyStatus != "accepted" || saved.Input == nil || !reflect.DeepEqual(saved.Input.Attachments, attachments) {
		t.Fatal("existing formatted reply or input evidence changed", saved, err)
	}
	run, err := migrated.Run(testContext, r.ID)
	if err != nil || run.Source == nil || run.Source.Input == nil || !reflect.DeepEqual(run.Source.Input.Attachments, attachments) || !reflect.DeepEqual(run.Source.Input.Quote, m.Input.Quote) {
		t.Fatal("existing execution source evidence changed", run.Source, err)
	}
}
