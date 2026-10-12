package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agent-host/internal/event"
)

type recoveryFixture struct {
	store          *Store
	previous       Session
	session        Session
	priorRun       Run
	currentRun     Run
	priorReceipt   ChannelMessage
	currentReceipt ChannelMessage
}

func newRecoveryFixture(t *testing.T, configure func(*ChannelMessage)) recoveryFixture {
	t.Helper()
	store, _ := testStore(t)
	prior := channelInput("previous")
	prior.Text = "Explain the CBS configuration from the project documentation."
	if configure != nil {
		configure(&prior)
	}
	routingDecision := prior.RoutingDecision
	enqueueChannel(t, store, prior)
	prior = claimChannel(t, store, prior.Profile)
	if len(routingDecision) != 0 {
		if err := store.SaveChannelRoutingDecision(testContext, prior.ID, routingDecision); err != nil {
			t.Fatal(err)
		}
	}
	partition := store.WithPartition(prior.AgentID, prior.AudienceID)
	priorRun := channelRun(t, partition, prior)
	if err := partition.CreateChannelRun(testContext, priorRun, prior.ID); err != nil {
		t.Fatal(err)
	}
	if err := partition.CompleteRun(testContext, priorRun.ID, "failed", "private-native-output", "test", "private-native-error"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishChannelMessage(testContext, prior.ID, "failed", "native connection failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareChannelReply(testContext, prior.ID, "The previous task failed; no answer was delivered."); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishChannelReply(testContext, prior.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	previous, err := partition.Session(testContext, priorRun.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	session := previous
	session.ID, session.NativeSessionID, session.CreatedAt = "recovered", "recovered-native", time.Now().UTC()
	if err := partition.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverChannelSession(testContext, prior.Binding, previous.ID, session.ID, priorRun.ID); err != nil {
		t.Fatal(err)
	}
	current := prior
	current.ID, current.MessageID, current.Text, current.ReceivedAt = "current", "native-current", "再试试", time.Now().UTC()
	current.SessionID, current.RunID, current.Status, current.ReplyText, current.ReplyStatus = "", "", "", "", ""
	enqueueChannel(t, store, current)
	current = claimChannel(t, store, current.Profile)
	if len(routingDecision) != 0 {
		if err := store.SaveChannelRoutingDecision(testContext, current.ID, routingDecision); err != nil {
			t.Fatal(err)
		}
	}
	currentRun := Run{ID: "current-run", SessionID: session.ID, AgentID: session.AgentID, AudienceID: session.AudienceID,
		Scope: session.Scope, Workspace: session.Workspace, ProjectID: session.ProjectID, ProjectName: session.ProjectName,
		RuntimeFingerprint: session.RuntimeFingerprint, Projects: session.Projects, AdditionalDirectories: session.AdditionalDirectories,
		Input: current.Text, NativeSessionID: session.NativeSessionID, RequestedModel: session.Model}
	if err := partition.CreateChannelRun(testContext, currentRun, current.ID); err != nil {
		t.Fatal(err)
	}
	priorRun, err = partition.Run(testContext, priorRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentRun, err = partition.Run(testContext, currentRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior, err = store.ChannelMessage(testContext, prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = store.ChannelMessage(testContext, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, err = partition.Session(testContext, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return recoveryFixture{partition, previous, session, priorRun, currentRun, prior, current}
}

func recoveryPayload(t *testing.T, s *Store, table, id string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(testContext, "UPDATE "+table+" SET payload=? WHERE id=?", raw, id); err != nil {
		t.Fatal(err)
	}
}

func recoverAgain(t *testing.T, f recoveryFixture, assembled bool) recoveryFixture {
	t.Helper()
	if assembled {
		e := event.Event{RunID: f.currentRun.ID, Type: "conversation.recovery.assembled", Phase: "execute", Component: "assembly.conversation", Status: "completed"}
		if err := f.store.AppendEvent(testContext, &e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.CompleteRun(testContext, f.currentRun.ID, "failed", "retry-native-output", "test", "retry-native-error"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishChannelMessage(testContext, f.currentReceipt.ID, "failed", "retry execution failed"); err != nil {
		t.Fatal(err)
	}
	session := f.session
	session.ID, session.NativeSessionID = session.ID+"-again", session.NativeSessionID+"-again"
	session.PreviousSessionID, session.RecoveryRunID, session.CreatedAt = "", "", time.Now().UTC()
	if err := f.store.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecoverChannelSession(testContext, f.currentReceipt.Binding, f.session.ID, session.ID, f.currentRun.ID); err != nil {
		t.Fatal(err)
	}
	next := f.currentReceipt
	next.ID, next.MessageID, next.Text, next.ReceivedAt = next.ID+"-again", next.MessageID+"-again", "再试试", time.Now().UTC()
	enqueueChannel(t, f.store, next)
	next = claimChannel(t, f.store, next.Profile)
	nextRun := f.currentRun
	nextRun.ID, nextRun.SessionID, nextRun.NativeSessionID, nextRun.Input, nextRun.Source = nextRun.ID+"-again", session.ID, session.NativeSessionID, next.Text, nil
	if err := f.store.CreateChannelRun(testContext, nextRun, next.ID); err != nil {
		t.Fatal(err)
	}
	session, err := f.store.Session(testContext, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	nextRun, err = f.store.Run(testContext, nextRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.store.ChannelMessage(testContext, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	priorRun, err := f.store.Run(testContext, f.currentRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := f.store.ChannelMessage(testContext, f.currentReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return recoveryFixture{f.store, f.session, session, priorRun, nextRun, prior, next}
}

func TestChannelRecoveryContextPreservesOnlyBoundedReceiptEvidence(t *testing.T) {
	f := newRecoveryFixture(t, func(m *ChannelMessage) { m.Text = strings.Repeat("中", 4100) })
	f.priorReceipt.ReplyText = strings.Repeat("文", 2100)
	recoveryPayload(t, f.store, "channel_messages", f.priorReceipt.ID, f.priorReceipt)
	context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID)
	if err != nil || context == nil {
		t.Fatal(context, err)
	}
	if context.SessionID != f.previous.ID || context.RunID != f.priorRun.ID || context.MessageID != f.priorReceipt.MessageID || context.SenderID != f.priorReceipt.SenderID || context.ExecutionStatus != "failed" || context.MemoryStatus != "skipped" || context.DeliveryStatus != "accepted" {
		t.Fatal("recovery evidence lost provenance or separate outcomes", context)
	}
	if utf8.RuneCountInString(context.Input) != 4000 || utf8.RuneCountInString(context.Reply) != 2000 || !context.InputTruncated || !context.ReplyTruncated || !utf8.ValidString(context.Input) || !utf8.ValidString(context.Reply) {
		t.Fatal("recovery evidence bounds broke Unicode or omitted truncation", context)
	}
	raw, _ := json.Marshal(context)
	for _, secret := range []string{"private-native-output", "private-native-error", "private-capability", "native_session_id", "attachments", "events"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("recovery bridge exposed non-receipt data", secret)
		}
	}
	current, err := f.store.Run(testContext, f.currentRun.ID)
	if err != nil || current.Input != "再试试" || current.Source.MessageID != f.currentReceipt.MessageID {
		t.Fatal("recovery bridge rewrote the current request", current, err)
	}
}

func TestChannelRecoveryContextCarriesBoundedQuoteWithoutChangingReceipt(t *testing.T) {
	quote := &ChannelQuote{MessageID: "quoted-discussion", MessageType: "chatRecord", Body: strings.Repeat("引", 4100), Title: strings.Repeat("题", 300), SummaryOnly: true}
	f := newRecoveryFixture(t, func(m *ChannelMessage) { m.Input = &ChannelInput{Quote: quote} })
	before, err := f.store.ChannelMessage(testContext, f.priorReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID)
	if err != nil || context == nil || context.Quote == nil {
		t.Fatal("recovery lost the earlier quote", context, err)
	}
	got := context.Quote
	if got.MessageID != quote.MessageID || got.MessageType != quote.MessageType || !got.SummaryOnly || !got.Truncated || utf8.RuneCountInString(got.Body) != 4000 || utf8.RuneCountInString(got.Title) != 256 {
		t.Fatal("historical quote lost provenance, preview flags or bounds", got)
	}
	got.Body = "caller-modified projection"
	after, err := f.store.ChannelMessage(testContext, f.priorReceipt.ID)
	if err != nil || after.Input.Quote.Body != before.Input.Quote.Body || after.Input.Quote.Title != before.Input.Quote.Title || after.Input.Quote.Truncated != before.Input.Quote.Truncated {
		t.Fatal("quote projection rewrote the immutable receipt", after, err)
	}
}

func TestChannelRecoveryContextRejectsCrossConversationAndRuntimeLinks(t *testing.T) {
	for _, boundary := range []string{"channel", "account", "tenant", "conversation", "conversation-type", "agent", "audience", "scope", "cwd", "project", "projects", "directories", "runtime", "model", "run-session", "native-session", "requested-model", "source-message", "source-sender", "indexed-outcome", "incomplete-link"} {
		t.Run(boundary, func(t *testing.T) {
			f := newRecoveryFixture(t, nil)
			switch boundary {
			case "channel", "account", "tenant", "conversation", "conversation-type":
				switch boundary {
				case "channel":
					f.currentReceipt.Channel, f.currentRun.Source.Channel = "other-channel", "other-channel"
				case "account":
					f.currentReceipt.AccountID, f.currentRun.Source.AccountID = "other-app", "other-app"
				case "tenant":
					f.currentReceipt.TenantID, f.currentRun.Source.TenantID = "other-tenant", "other-tenant"
				case "conversation":
					f.currentReceipt.ConversationID, f.currentRun.Source.ConversationID = "other-chat", "other-chat"
				case "conversation-type":
					f.currentReceipt.ConversationType, f.currentRun.Source.ConversationType = "2", "2"
				}
				recoveryPayload(t, f.store, "channel_messages", f.currentReceipt.ID, f.currentReceipt)
				recoveryPayload(t, f.store, "runs", f.currentRun.ID, f.currentRun)
			case "agent", "audience", "scope", "cwd", "project", "projects", "directories", "runtime", "model":
				switch boundary {
				case "agent":
					f.previous.AgentID = "other-agent"
				case "audience":
					f.previous.AudienceID = "other-audience"
				case "scope":
					f.previous.Scope = "other-scope"
				case "cwd":
					f.previous.Workspace = "/other-workspace"
				case "project":
					f.previous.ProjectID = "other-project"
				case "projects":
					f.previous.Projects = []ProjectRef{{ID: "other", Scope: "other", Workspace: "/other"}}
				case "directories":
					f.previous.AdditionalDirectories = []string{"/other"}
				case "runtime":
					f.previous.RuntimeFingerprint = "other-runtime"
				case "model":
					f.previous.Model = "other-model"
				}
				recoveryPayload(t, f.store, "sessions", f.previous.ID, f.previous)
			case "run-session", "native-session", "requested-model", "source-message", "source-sender":
				switch boundary {
				case "run-session":
					f.priorRun.SessionID = "other-session"
				case "native-session":
					f.priorRun.NativeSessionID = "other-native"
				case "requested-model":
					f.priorRun.RequestedModel = "other-model"
				case "source-message":
					f.priorRun.Source.MessageID = "other-source"
				case "source-sender":
					f.priorRun.Source.SenderID = "other-sender"
				}
				recoveryPayload(t, f.store, "runs", f.priorRun.ID, f.priorRun)
			case "indexed-outcome":
				if _, err := f.store.db.ExecContext(testContext, "UPDATE runs SET execution_status='completed' WHERE id=?", f.priorRun.ID); err != nil {
					t.Fatal(err)
				}
			case "incomplete-link":
				f.session.RecoveryRunID = ""
				recoveryPayload(t, f.store, "sessions", f.session.ID, f.session)
			}
			if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err == nil || context != nil {
				t.Fatal("recovery context accepted invalid boundary", context, err)
			}
		})
	}
}

func TestChannelRecoveryContextRetainsGroupAuthorAndLegacyDMType(t *testing.T) {
	for _, group := range []bool{false, true} {
		f := newRecoveryFixture(t, func(m *ChannelMessage) {
			if group {
				m.ConversationType, m.AgentID, m.AudienceID, m.ConversationID = "2", "assistant", "group-one", "group-one"
				m.SenderID = "alice"
			}
		})
		if group {
			f.currentReceipt.SenderID, f.currentRun.Source.SenderID = "bob", "bob"
		} else {
			f.currentReceipt.ConversationType, f.currentRun.Source.ConversationType = "1", "1"
		}
		recoveryPayload(t, f.store, "channel_messages", f.currentReceipt.ID, f.currentReceipt)
		recoveryPayload(t, f.store, "runs", f.currentRun.ID, f.currentRun)
		context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID)
		if err != nil || context == nil || context.SenderID != f.priorReceipt.SenderID {
			t.Fatal("compatible conversation lost original sender", context, err)
		}
	}
}

func TestChannelRecoveryContextRejectsGroupToPrivateTypeChange(t *testing.T) {
	f := newRecoveryFixture(t, func(m *ChannelMessage) {
		m.ConversationType, m.AgentID, m.AudienceID, m.ConversationID = "2", "assistant", "group-one", "group-one"
	})
	f.currentReceipt.ConversationType, f.currentRun.Source.ConversationType = "1", "1"
	recoveryPayload(t, f.store, "channel_messages", f.currentReceipt.ID, f.currentReceipt)
	recoveryPayload(t, f.store, "runs", f.currentRun.ID, f.currentRun)
	if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err == nil || context != nil {
		t.Fatal("valid but different conversation type crossed the recovery boundary", context, err)
	}
}

func TestChannelRecoveryContextRetainsHistoricalDefaultScope(t *testing.T) {
	f := newRecoveryFixture(t, func(m *ChannelMessage) {
		m.RoutingDecision = json.RawMessage(`{"kind":"default","reason":"native handoff","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","reason":"project unclear"}}}`)
	})
	if _, err := f.store.ConversationHistoryBinding(testContext, f.currentReceipt.ID); err != nil {
		t.Fatal("historical default handoff lost same-scope history", err)
	}
	if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err != nil || context == nil || context.Input != f.priorReceipt.Text {
		t.Fatal("direct linked evidence unavailable without broader history", context, err)
	}
}

func TestChannelRecoveryContextAvailableAfterOldBinaryAndFailedAssembly(t *testing.T) {
	for _, previousAttempt := range []string{"old-binary", "old-text-only-assembly", "failed-assembly", "completed-assembly"} {
		t.Run(previousAttempt, func(t *testing.T) {
			f := newRecoveryFixture(t, nil)
			if previousAttempt != "old-binary" {
				e := event.Event{RunID: f.currentRun.ID, Type: "conversation.recovery.assembled", Phase: "execute", Component: "assembly.conversation", Status: "completed", Data: map[string]any{"quote_context_included": previousAttempt != "old-text-only-assembly"}}
				if err := f.store.AppendEvent(testContext, &e); err != nil {
					t.Fatal(err)
				}
			}
			status := "completed"
			if previousAttempt == "failed-assembly" {
				status = "failed"
			}
			if err := f.store.CompleteRun(testContext, f.currentRun.ID, status, "answer", "test", ""); err != nil {
				t.Fatal(err)
			}
			next := channelInput("next")
			next.Text = "What did I ask earlier?"
			enqueueChannel(t, f.store, next)
			next = claimChannel(t, f.store, next.Profile)
			nextRun := f.currentRun
			nextRun.ID, nextRun.Input, nextRun.Source = "next-run", next.Text, nil
			if err := f.store.CreateChannelRun(testContext, nextRun, next.ID); err != nil {
				t.Fatal(err)
			}
			context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, next.ID)
			if err != nil || (context == nil) != (previousAttempt == "completed-assembly") {
				t.Fatal("recovery evidence consumed without successful native turn", context, err)
			}
		})
	}
}

func TestChannelRecoveryContextIgnoresUnlinkedAndLegacyLocalSessions(t *testing.T) {
	for _, legacyLocal := range []bool{false, true} {
		f := newRecoveryFixture(t, nil)
		if legacyLocal {
			f.priorRun.Source = nil
			recoveryPayload(t, f.store, "runs", f.priorRun.ID, f.priorRun)
		} else {
			f.session.PreviousSessionID, f.session.RecoveryRunID = "", ""
			recoveryPayload(t, f.store, "sessions", f.session.ID, f.session)
		}
		if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err != nil || context != nil {
			t.Fatal("unlinked/local session fabricated channel context", context, err)
		}
	}
}

func TestChannelRecoveryContextKeepsRepeatedFailureSubjectWithValidatedLineage(t *testing.T) {
	for _, assembled := range []bool{false, true} {
		f := newRecoveryFixture(t, nil)
		originalQuestion := f.priorReceipt.Text
		f = recoverAgain(t, f, assembled)
		context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID)
		if err != nil || context == nil || context.Input != "再试试" || (context.Earlier != nil) != assembled {
			t.Fatal("repeated recovery lost the direct request or borrowed unsupplied history", context, err)
		}
		if assembled && (context.Earlier.Input != originalQuestion || context.Earlier.RunID != "run-previous" || context.Earlier.Earlier != nil) {
			t.Fatal("repeated recovery lost the original subject", context)
		}
		if assembled {
			original, err := f.store.Run(testContext, "run-previous")
			if err != nil {
				t.Fatal(err)
			}
			original.Source.ConversationID = "other-conversation"
			recoveryPayload(t, f.store, "runs", original.ID, original)
			if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err == nil || context != nil {
				t.Fatal("earlier linked evidence bypassed conversation validation", context, err)
			}
		}
	}
}

func TestChannelRecoveryContextBoundsRepeatedRecoveryLineage(t *testing.T) {
	f := newRecoveryFixture(t, nil)
	for i := 0; i < 3; i++ {
		f = recoverAgain(t, f, true)
	}
	context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID)
	if err != nil || context == nil || context.Earlier == nil || context.Earlier.Earlier == nil || context.Earlier.Earlier.Earlier != nil || !context.Earlier.Earlier.EarlierTruncated {
		t.Fatal("recovery lineage exceeded its declared three-record bound", context, err)
	}
}

func TestChannelRecoveryContextRejectsCyclicRecoveryLineage(t *testing.T) {
	f := newRecoveryFixture(t, nil)
	f = recoverAgain(t, f, true)
	f.previous.PreviousSessionID = f.session.ID
	f.previous.RecoveryRunID = f.currentRun.ID
	recoveryPayload(t, f.store, "sessions", f.previous.ID, f.previous)
	if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err == nil || context != nil {
		t.Fatal("cyclic recovery evidence was followed", context, err)
	}
}

func TestChannelRecoveryContextRequiresCurrentRunningReceipt(t *testing.T) {
	f := newRecoveryFixture(t, nil)
	if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.priorReceipt.ID); err == nil || context != nil {
		t.Fatal("recovery accepted another session's receipt as the current task", context, err)
	}
	if err := f.store.CompleteRun(testContext, f.currentRun.ID, "completed", "answer", "test", ""); err != nil {
		t.Fatal(err)
	}
	if context, err := f.store.ChannelRecoveryContext(testContext, f.session.ID, f.currentReceipt.ID); err == nil || context != nil {
		t.Fatal("recovery accepted a completed task as the current invocation", context, err)
	}
}
