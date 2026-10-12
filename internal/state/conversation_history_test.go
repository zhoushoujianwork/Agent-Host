package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func historyTask(t *testing.T, s *Store, m ChannelMessage, projects []ProjectRef) ChannelMessage {
	t.Helper()
	m.Profile = "history-" + m.ID
	var codes []string
	if m.Input != nil {
		for range m.Input.Attachments {
			codes = append(codes, "private-download-capability")
		}
	}
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, codes); err != nil || !inserted {
		t.Fatal("history receipt", inserted, err)
	}
	m = claimChannel(t, s, m.Profile)
	if m.Input != nil && len(m.Input.Attachments) != 0 {
		attachments := append([]ChannelAttachment(nil), m.Input.Attachments...)
		for i := range attachments {
			attachments[i].Path, attachments[i].SHA256 = "/private/downloaded/secret-file", strings.Repeat("a", 64)
			attachments[i].ContentType, attachments[i].Bytes = "text/plain", 10
		}
		if err := s.SaveChannelAttachments(testContext, m.ID, attachments); err != nil {
			t.Fatal(err)
		}
	}
	session := Session{AgentID: m.AgentID, AudienceID: m.AudienceID, ID: "session-" + m.ID, Scope: m.Scope, NativeSessionID: "native-" + m.ID}
	binding := "history-binding-" + m.ID
	if len(projects) != 0 {
		session.Projects, session.RuntimeFingerprint = projects, "runtime-v1"
		if len(projects) == 1 {
			session.ProjectID, session.ProjectName, session.Scope, session.Workspace = projects[0].ID, projects[0].Name, projects[0].Scope, projects[0].Workspace
		} else {
			session.Scope = "project-set-history"
			workspace, err := CanonicalWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session.Workspace = workspace
			for _, p := range projects {
				session.AdditionalDirectories = append(session.AdditionalDirectories, p.Workspace)
			}
		}
	} else {
		binding = m.Binding
	}
	partition := s.WithPartition(m.AgentID, m.AudienceID)
	if err := partition.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, binding, session.ID); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		if err := s.ResolveChannelProjects(testContext, m.ID, binding, session.ProjectID, session.Workspace, session.Scope, session.RuntimeFingerprint, projects, session.AdditionalDirectories); err != nil {
			t.Fatal(err)
		}
	}
	run := Run{AgentID: m.AgentID, AudienceID: m.AudienceID, ID: "run-" + m.ID, SessionID: session.ID, Scope: session.Scope, Input: m.Text,
		NativeSessionID: session.NativeSessionID, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace,
		Projects: projects, AdditionalDirectories: session.AdditionalDirectories, RuntimeFingerprint: session.RuntimeFingerprint}
	if err := partition.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := s.ChannelMessage(testContext, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func historyProject(t *testing.T, s *Store, id string) ProjectRef {
	t.Helper()
	p, err := s.RegisterProject(testContext, id, id, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ProjectRef{ID: p.ID, Name: p.Name, Workspace: p.Workspace, Scope: p.Scope}
}

func historyBinding(t *testing.T, s *Store, id string) ConversationHistoryBinding {
	t.Helper()
	b, err := s.ConversationHistoryBinding(testContext, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConversationHistoryFiltersAudienceAgentTenantAndWholeProjectBeforeLimits(t *testing.T) {
	s, _ := testStore(t)
	alpha, beta := historyProject(t, s, "alpha"), historyProject(t, s, "beta")
	group := func(id string) ChannelMessage {
		m := channelInput(id)
		m.ConversationType, m.AgentID, m.AudienceID, m.ConversationID, m.Binding = "2", "assistant", "group-one", "group-one", "group-one-binding"
		return m
	}
	old := group("allowed-old")
	old.SenderID = "alice"
	historyTask(t, s, old, []ProjectRef{alpha})
	denied := []string{}
	for _, scenario := range []string{"project", "mixed-project", "group", "agent", "tenant", "account", "dm"} {
		m, projects := group("denied-"+scenario), []ProjectRef{alpha}
		switch scenario {
		case "project":
			projects = []ProjectRef{beta}
		case "mixed-project":
			projects = []ProjectRef{alpha, beta}
		case "group":
			m.ConversationID = "group-two"
		case "agent":
			m.AgentID = "other-agent"
		case "tenant":
			m.TenantID = "other-tenant"
		case "account":
			m.AccountID = "other-account"
		case "dm":
			m.ConversationType, m.AudienceID = "1", "owner"
		}
		historyTask(t, s, m, projects)
		denied = append(denied, m.ID)
	}
	latest := group("allowed-new")
	latest.SenderID = "bob"
	historyTask(t, s, latest, []ProjectRef{alpha})
	current := historyTask(t, s, group("current"), []ProjectRef{alpha})
	binding := historyBinding(t, s, current.ID)
	result, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 1})
	if err != nil || len(result.Turns) != 1 || result.Turns[0].ReceiptID != latest.ID || result.NextCursor == "" {
		t.Fatal("ineligible rows changed limited search", result, err)
	}
	for _, id := range append(denied, current.ID) {
		if _, err := s.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: "host-history:" + id, Before: 3, After: 3, Limit: 100}); err == nil {
			t.Fatal("guessed reference escaped history grants", id)
		}
	}
	window, err := s.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: "host-history:" + latest.ID, Before: 3, After: 3, Limit: 100})
	if err != nil || len(window.Turns) != 2 || window.Turns[0].AuthorID != "alice" || window.Turns[1].AuthorID != "bob" {
		t.Fatal("neighbors crossed scope or lost group sender attribution", window, err)
	}
	multi := historyTask(t, s, group("current-multi"), []ProjectRef{beta, alpha})
	bindingMulti := historyBinding(t, s, multi.ID)
	read, err := s.ReadConversationHistory(testContext, bindingMulti, ConversationHistoryRead{Reference: "host-history:denied-mixed-project", Limit: 100})
	if err != nil || len(read.Turns) != 1 || !reflect.DeepEqual(read.Turns[0].Projects, []string{"alpha", "beta"}) {
		t.Fatal("whole project grants could not retrieve joint turn", read, err)
	}
}

func TestConversationHistoryUsesExactFormattedReplyAndOmitsAdministrativeSecrets(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("saved")
	m.Input = &ChannelInput{Attachments: []ChannelAttachment{{Kind: "file", Name: "plan.txt", MediaType: "text/plain", Size: 10}},
		Quote: &ChannelQuote{MessageID: "quoted-id", MessageType: "text", Body: "A colleague's deployment suggestion", Title: "context", SummaryOnly: true}}
	saved := historyTask(t, s, m, nil)
	if err := s.CompleteRun(testContext, saved.RunID, "completed", "raw output omitted from channel", "test", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, saved.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	formatted := "## Deployment\n\nUse the saved **formatted** reply."
	if err := s.PrepareChannelReply(testContext, saved.ID, formatted); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, saved.ID, "failed", "secret-transport-error"); err != nil {
		t.Fatal(err)
	}
	historyTask(t, s, channelInput("current"), nil)
	binding := historyBinding(t, s, "current")
	result, err := s.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: "host-history:saved", Limit: 2048})
	if err != nil || len(result.Turns) != 1 {
		t.Fatal(result, err)
	}
	turn := result.Turns[0]
	if turn.Reply.Text != formatted || turn.DeliveryStatus != "failed" || turn.ExecutionStatus != "completed" || turn.Input.Text != m.Text || turn.Quote.Attribution == m.SenderID || !turn.Quote.SummaryOnly {
		t.Fatal("history rewrote reply, delivery or quotation evidence", turn)
	}
	search, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Query: "deployment", Limit: 10})
	if err != nil || len(search.Turns) != 1 {
		t.Fatal("quote/reply search failed", search, err)
	}
	for _, data := range []any{result, search} {
		raw, _ := json.Marshal(data)
		for _, secret := range []string{"private-capability", "private-download-capability", "/private/downloaded", "secret-transport-error", "reply_url", "input_capabilities", "raw output", "runtime_fingerprint", "workspace"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("administrative field leaked %q: %s", secret, raw)
			}
		}
	}
}

func TestConversationHistorySnapshotPaginationRestartAndTextRevision(t *testing.T) {
	s, home := testStore(t)
	for i := range 4 {
		m := channelInput(fmt.Sprintf("old-%d", i))
		m.Text = strings.Repeat("历史", 300)
		historyTask(t, s, m, nil)
	}
	historyTask(t, s, channelInput("current"), nil)
	binding := historyBinding(t, s, "current")
	first, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 2})
	if err != nil || len(first.Turns) != 2 || first.NextCursor == "" || len([]rune(first.Turns[0].Input.Text)) != 240 || !first.Turns[0].Input.Partial {
		t.Fatal(first, err)
	}
	late := channelInput("late")
	late.ReceivedAt = time.Now().Add(-24 * time.Hour)
	historyTask(t, s, late, nil)
	ro, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	second, err := ro.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Turns) != 2 || second.NextCursor != "" || second.Coverage.Snapshot != first.Coverage.Snapshot {
		t.Fatal("new receipts changed query snapshot after restart", second, err)
	}
	for _, turn := range append(first.Turns, second.Turns...) {
		if turn.ReceiptID == "late" || turn.ReceiptID == "current" {
			t.Fatal("current or later receipt was not excluded", turn)
		}
	}
	if _, err := ro.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Query: "changed", Limit: 2, Cursor: first.NextCursor}); err == nil {
		t.Fatal("cursor accepted a changed query")
	}
	anchor := first.Turns[0]
	read, err := ro.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: anchor.Reference, Limit: 80, Offset: 80, ExpectedDigest: anchor.Digest})
	if err != nil || len([]rune(read.Turns[0].Input.Text)) != 80 || read.Turns[0].Input.Offset != 80 {
		t.Fatal("Unicode text continuation failed", read, err)
	}
	if err := s.CompleteRun(testContext, anchor.RunID, "completed", "raw", "test", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: anchor.Reference, Limit: 80, Offset: 80, ExpectedDigest: anchor.Digest}); err == nil {
		t.Fatal("changed execution/reply revision did not invalidate continuation")
	}
	newCurrent := historyTask(t, s, channelInput("after-reset"), nil)
	if got := historyBinding(t, s, newCurrent.ID); got.Fingerprint != binding.Fingerprint {
		t.Fatal("receipt/session reset rotated stable history policy", got, binding)
	}
}

func TestConversationHistorySearchSnippetPreservesLateMatchAndReceiptTimeWindow(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("late-keyword")
	m.Text = strings.Repeat("历史", 300) + " deployment agreement " + strings.Repeat("文", 300)
	m.SentAt = time.Now().Add(-72 * time.Hour)
	m.ReceivedAt = time.Now().Add(-time.Hour)
	historyTask(t, s, m, nil)
	historyTask(t, s, channelInput("current"), nil)
	binding := historyBinding(t, s, "current")
	result, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Query: "Deployment", After: m.ReceivedAt.Add(-time.Minute), Before: m.ReceivedAt.Add(time.Minute), Limit: 1})
	if err != nil || len(result.Turns) != 1 || !strings.Contains(result.Turns[0].Input.Text, "deployment agreement") || result.Turns[0].Input.Offset == 0 || !result.Coverage.Partial {
		t.Fatal("late lexical match or Host receipt time was lost", result, err)
	}
	read, err := s.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: result.Turns[0].Reference, Offset: result.Turns[0].Input.Offset, ExpectedDigest: result.Turns[0].Digest, Limit: 240})
	if err != nil || read.Turns[0].Input.Text != result.Turns[0].Input.Text {
		t.Fatal("matching snippet did not identify an exact readable excerpt", read, err)
	}
}

func TestConversationHistoryControlQueuedAndUnresolvedEligibility(t *testing.T) {
	s, _ := testStore(t)
	for _, text := range []string{"/new", "/help", "/status", "queued business material"} {
		m := channelInput(text)
		m.Text = text
		enqueueChannel(t, s, m)
	}
	historyTask(t, s, channelInput("known"), nil)
	unresolved := channelInput("unresolved")
	unresolved.Profile = "unresolved"
	enqueueChannel(t, s, unresolved)
	unresolved = claimChannel(t, s, unresolved.Profile)
	decision := json.RawMessage(`{"kind":"default","reason":"handoff","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","reason":"unknown"}}}`)
	if err := s.SaveChannelRoutingDecision(testContext, unresolved.ID, decision); err != nil {
		t.Fatal(err)
	}
	run := channelRun(t, s, unresolved)
	if err := s.CreateChannelRun(testContext, run, unresolved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConversationHistoryBinding(testContext, unresolved.ID); err != nil {
		t.Fatal("historical default handoff lost history", err)
	}
	historyTask(t, s, channelInput("current"), nil)
	binding := historyBinding(t, s, "current")
	result, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 20})
	if err != nil || len(result.Turns) != 4 {
		t.Fatal("control/queued/unresolved eligibility", result, err)
	}
	for _, turn := range result.Turns {
		if turn.ReceiptID == "/status" || turn.ReceiptID == "queued business material" {
			t.Fatal("unattributed business material leaked", turn)
		}
	}
	// Route-pinned work that has not started retains its real not_started state.
	queued := channelInput("route-only")
	queued.Profile = queued.ID
	enqueueChannel(t, s, queued)
	queued = claimChannel(t, s, queued.Profile)
	workspace := t.TempDir()
	if err := s.ResolveChannelRoute(testContext, queued.ID, "route-only-binding", "", workspace, queued.Scope, "runtime"); err != nil {
		t.Fatal(err)
	}
	historyTask(t, s, channelInput("current-next"), nil)
	read, err := s.ReadConversationHistory(testContext, historyBinding(t, s, "current-next"), ConversationHistoryRead{Reference: "host-history:route-only", Limit: 100})
	if err != nil || read.Turns[0].ExecutionStatus != "not_started" || read.Turns[0].ReceiptStatus != "claimed" {
		t.Fatal("attributed pre-execution material was fabricated or omitted", read, err)
	}
}

func TestConversationHistoryRevalidatesBindingBoundsDuplicatesAndConcurrentWrites(t *testing.T) {
	s, home := testStore(t)
	old := historyTask(t, s, channelInput("old"), nil)
	current := historyTask(t, s, channelInput("current"), nil)
	binding := historyBinding(t, s, current.ID)
	if inserted, err := s.EnqueueChannelMessage(testContext, channelInput("old"), testReplyURL); err != nil || inserted {
		t.Fatal("duplicate receipt changed history", inserted, err)
	}
	for _, query := range []ConversationHistorySearch{{Limit: 0}, {Limit: 21}, {Limit: 1, Query: strings.Repeat("a", 257)}, {Limit: 1, After: time.Now(), Before: time.Now().Add(-time.Hour)}} {
		if _, err := s.SearchConversationHistory(testContext, binding, query); err == nil {
			t.Fatal("unbounded search accepted", query)
		}
	}
	for _, query := range []ConversationHistoryRead{{Reference: "host-history:old", Limit: 2049}, {Reference: "host-history:old", Limit: 10, Before: 4}, {Reference: "host-history:old", Limit: 10, Offset: 1}} {
		if _, err := s.ReadConversationHistory(testContext, binding, query); err == nil {
			t.Fatal("unbounded read accepted", query)
		}
	}
	ro, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if _, err := ro.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 1}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	if err := s.CompleteRun(testContext, old.RunID, "unknown", "", "test", "internal detail"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	// A launch-time binding is not authorization after persisted scope changes.
	if err := s.transaction(testContext, func(tx *sql.Tx) error {
		m, err := readChannelMessage(testContext, tx, current.ID)
		if err != nil {
			return err
		}
		m.AudienceID = "changed-audience"
		return writeChannelMessage(testContext, tx, m)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 1}); err == nil {
		t.Fatal("modified authority retained old capability")
	}
}
