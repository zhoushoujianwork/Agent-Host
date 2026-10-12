package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func recentRequestTask(t *testing.T, s *Store, message ChannelMessage, projects []ProjectRef, model string) ChannelMessage {
	t.Helper()
	message = historyTask(t, s, message, projects)
	partition := s.WithPartition(message.AgentID, message.AudienceID)
	session, err := partition.Session(testContext, message.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := partition.Run(testContext, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	session.Model, run.RequestedModel = model, model
	recoveryPayload(t, s, "sessions", session.ID, session)
	recoveryPayload(t, s, "runs", run.ID, run)
	return message
}

func TestChannelRecentRequestsCarriesInboundQuestionAcrossModelChange(t *testing.T) {
	s, _ := testStore(t)
	prior := channelInput("original-question")
	prior.Text = "Explain the CBS configuration from our project documentation."
	prior = recentRequestTask(t, s, prior, nil, "opus")
	if err := s.CompleteRun(testContext, prior.RunID, "failed", "private-native-output", "opus", "private-native-error"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, prior.ID, "failed", "native failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, prior.ID, "private-saved-reply"); err != nil {
		t.Fatal(err)
	}
	current := channelInput("retry")
	current.Text = "再试试"
	current = recentRequestTask(t, s, current, nil, "sonnet")
	requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 1 || requests[0].MessageID != prior.MessageID || requests[0].SenderID != prior.SenderID || requests[0].Input != prior.Text || requests[0].InputTruncated {
		t.Fatal("fresh model session lost the previous authored question", requests, err)
	}
	raw, _ := json.Marshal(requests)
	for _, secret := range []string{"private-native-output", "private-native-error", "private-saved-reply", "private-capability", "native_session_id", "attachments", "delivery_status"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("request handoff exposed prior execution or transport data", secret)
		}
	}
	run, err := s.Run(testContext, current.RunID)
	if err != nil || run.Input != "再试试" || run.RequestedModel != "sonnet" {
		t.Fatal("recent requests changed the current task or model", run, err)
	}
}

func TestChannelRecentRequestsCarriesQuoteAsSeparateSourceMaterial(t *testing.T) {
	s, _ := testStore(t)
	prior := channelInput("quoted-question")
	prior.Text = "Explain this quoted discussion."
	prior.Input = &ChannelInput{Quote: &ChannelQuote{MessageID: "original-discussion", MessageType: "chatRecord", Body: "A colleague proposed using s3-proxy.", Title: "CBS discussion", SummaryOnly: true, Truncated: true}}
	prior = recentRequestTask(t, s, prior, nil, "old-model")
	if err := s.CompleteRun(testContext, prior.RunID, "completed", "native-output-not-reference", "old-model", ""); err != nil {
		t.Fatal(err)
	}
	current := recentRequestTask(t, s, channelInput("followup"), nil, "new-model")
	requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 1 || requests[0].Quote == nil {
		t.Fatal("fresh-session handoff lost the associated quotation", requests, err)
	}
	got := requests[0].Quote
	if requests[0].Input != prior.Text || got.MessageID != prior.Input.Quote.MessageID || got.Body != prior.Input.Quote.Body || got.Title != prior.Input.Quote.Title || !got.SummaryOnly || !got.Truncated {
		t.Fatal("quoted material became the sender's own request or lost provenance", requests)
	}
	raw, _ := json.Marshal(requests)
	if strings.Contains(string(raw), "native-output-not-reference") {
		t.Fatal("a quoted reference enabled unrelated native-output retrieval")
	}
}

func TestChannelRecentRequestsFiltersSourceAndProjectBeforeCandidateLimit(t *testing.T) {
	s, _ := testStore(t)
	alpha, beta := historyProject(t, s, "alpha"), historyProject(t, s, "beta")
	group := func(id string) ChannelMessage {
		m := channelInput(id)
		m.ConversationType, m.AgentID, m.AudienceID, m.ConversationID, m.Binding = "2", "assistant", "group-one", "group-one", "group-one-binding"
		return m
	}
	allowed := group("allowed")
	allowed.SenderID = "alice"
	allowed.Text = "Inspect project alpha's CBS settings."
	allowed = recentRequestTask(t, s, allowed, []ProjectRef{alpha}, "opus")
	for _, boundary := range []string{"account", "tenant", "conversation", "agent", "audience", "private", "project", "default"} {
		for i := 0; i < 9; i++ {
			message := group(fmt.Sprintf("denied-%s-%d", boundary, i))
			projects := []ProjectRef{alpha}
			switch boundary {
			case "account":
				message.AccountID = "other-account"
			case "tenant":
				message.TenantID = "other-tenant"
			case "conversation":
				message.ConversationID = "other-group"
			case "agent":
				message.AgentID = "other-agent"
			case "audience":
				message.AudienceID = "other-audience"
			case "private":
				message.ConversationType = "1"
			case "project":
				projects = []ProjectRef{beta}
			case "default":
				projects = nil
			}
			recentRequestTask(t, s, message, projects, "opus")
		}
	}
	current := group("current")
	current.SenderID = "bob"
	current = recentRequestTask(t, s, current, []ProjectRef{alpha}, "sonnet")
	requests, err := s.WithPartition(current.AgentID, current.AudienceID).ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 1 || requests[0].MessageID != allowed.MessageID || requests[0].SenderID != "alice" {
		t.Fatal("fresh request handoff crossed source/project boundaries or filtered after its limit", requests, err)
	}
}

func TestChannelRecentRequestsExcludesMixedProjectAuthority(t *testing.T) {
	s, _ := testStore(t)
	alpha, beta := historyProject(t, s, "alpha"), historyProject(t, s, "beta")
	prior := recentRequestTask(t, s, channelInput("mixed"), []ProjectRef{alpha}, "opus")
	priorRun, err := s.Run(testContext, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Model a validly linked receipt/run whose wider project set shares the same
	// primary project and scope; the exact set must still be excluded.
	prior.Projects, priorRun.Projects = []ProjectRef{alpha, beta}, []ProjectRef{alpha, beta}
	recoveryPayload(t, s, "channel_messages", prior.ID, prior)
	recoveryPayload(t, s, "runs", priorRun.ID, priorRun)
	current := recentRequestTask(t, s, channelInput("current"), []ProjectRef{alpha}, "sonnet")
	requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 0 {
		t.Fatal("request with wider project authority entered fresh context", requests, err)
	}
}

func TestChannelRecentRequestsBoundsTextCountAndReceiptSnapshot(t *testing.T) {
	s, _ := testStore(t)
	for i := 0; i < 10; i++ {
		message := channelInput(fmt.Sprintf("earlier-%d", i))
		message.Text = strings.Repeat("问", 4100)
		recentRequestTask(t, s, message, nil, "opus")
	}
	current := recentRequestTask(t, s, channelInput("current"), nil, "sonnet")
	recentRequestTask(t, s, channelInput("later"), nil, "sonnet")
	requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 3 || requests[0].MessageID != "native-earlier-9" || requests[2].MessageID != "native-earlier-7" {
		t.Fatal("request handoff was not bounded to preceding requests", requests, err)
	}
	for _, request := range requests {
		if !request.InputTruncated || utf8.RuneCountInString(request.Input) != 4000 || !utf8.ValidString(request.Input) {
			t.Fatal("bounded request text broke Unicode or omitted truncation", request)
		}
	}
}

func TestChannelRecentRequestsRetainsHistoricalDefaultScope(t *testing.T) {
	s, _ := testStore(t)
	prior := recentRequestTask(t, s, channelInput("prior"), nil, "opus")
	current := recentRequestTask(t, s, channelInput("current"), nil, "sonnet")
	run, err := s.Run(testContext, current.RunID)
	if err != nil {
		t.Fatal(err)
	}
	current.RoutingDecision = json.RawMessage(`{"kind":"default","reason":"native handoff","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","reason":"project unclear"}}}`)
	run.MemoryAttribution = "unresolved"
	recoveryPayload(t, s, "channel_messages", current.ID, current)
	recoveryPayload(t, s, "runs", run.ID, run)
	if _, err := s.ConversationHistoryBinding(testContext, current.ID); err != nil {
		t.Fatal("historical default handoff lost same-scope history", err)
	}
	requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID)
	if err != nil || len(requests) != 1 || requests[0].MessageID != prior.MessageID {
		t.Fatal("unresolved task could not review its same-scope previous request", requests, err)
	}
}

func TestChannelRecentRequestsRejectsForgedSourcesAndResumedTasks(t *testing.T) {
	for _, mode := range []string{"prior-source", "current-receipt", "resumed", "completed"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			prior := recentRequestTask(t, s, channelInput("prior"), nil, "opus")
			current := recentRequestTask(t, s, channelInput("current"), nil, "sonnet")
			switch mode {
			case "prior-source":
				run, err := s.Run(testContext, prior.RunID)
				if err != nil {
					t.Fatal(err)
				}
				run.Source.SenderID = "forged-author"
				recoveryPayload(t, s, "runs", run.ID, run)
			case "current-receipt":
				current.ID = prior.ID
			case "resumed":
				run, err := s.Run(testContext, current.RunID)
				if err != nil {
					t.Fatal(err)
				}
				run.ID, run.Source = "another-run", nil
				if err := s.CreateRun(testContext, run); err != nil {
					t.Fatal(err)
				}
			case "completed":
				if err := s.CompleteRun(testContext, current.RunID, "completed", "answer", "sonnet", ""); err != nil {
					t.Fatal(err)
				}
			}
			if requests, err := s.ChannelRecentRequests(testContext, current.SessionID, current.ID); err == nil || requests != nil {
				t.Fatal("request handoff accepted forged evidence or a nonfresh invocation", requests, err)
			}
		})
	}
}
