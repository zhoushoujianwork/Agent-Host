package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestHistoricalForwardedMarkdownQuoteReachesFreshFollowup(t *testing.T) {
	for _, mode := range []string{"failed recovery", "model change"} {
		t.Run(mode, func(t *testing.T) {
			f := newProjectFixture(t)
			const body = "你知道如何操作 cbs cli + s3-proxy 操作对象存储吗？\n先解释命令用途。"
			const title = "Forwarded object-storage discussion"
			const followup = "再试试"
			const unverifiedOutput = "The old native operation uploaded the object."
			var memoryInputs []string
			f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
				memoryInputs = append(memoryInputs, input)
				return memory.Decision{Kind: "none", Model: "fixture-decision"}, nil
			})
			if mode == "failed recovery" {
				f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
					err := errors.New("native model stream ended unexpectedly")
					return harness.Result{Status: "failed", Text: unverifiedOutput, Error: err.Error(), Model: req.Model}, err
				}
			}
			original := inputCallback(t, f.serviceFixture, "forwarded-question", "markdown", "", map[string]string{"title": title, "text": body}, nil)
			prior := f.process(t, original)
			priorRun := f.run(t, prior)
			if priorRun.Input != "Forwarded Markdown message" || priorRun.Source == nil || priorRun.Source.Input == nil || priorRun.Source.Input.Quote == nil || priorRun.Source.Input.Quote.Body != body || priorRun.Source.Input.Quote.Title != title {
				t.Fatal("forwarded material lost its separate source attribution", priorRun)
			}
			f.runner.run = nil
			if mode == "model change" {
				f.host.Config.Model = "fixture-model-after-change"
				f.policy = "fixture-model-policy-after-change"
			}
			input := inputCallback(t, f.serviceFixture, "retry-forwarded-question", "text", followup, nil, map[string]any{
				"msgId": "bot-reply", "msgType": "text", "content": map[string]string{"text": "Agent-Host 回复"},
			})
			current := f.process(t, input)
			currentRun := f.run(t, current)
			requests := f.runner.snapshot()
			if current.Status != "completed" || len(requests) != 2 || requests[1].Resume || current.SessionID == prior.SessionID {
				t.Fatal("follow-up did not execute once in a fresh native session", current, requests)
			}
			session, err := f.service.Store.Session(context.Background(), current.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "failed recovery" && (priorRun.ExecutionStatus != "failed" || session.PreviousSessionID != prior.SessionID || session.RecoveryRunID != priorRun.ID) {
				t.Fatal("fixture did not exercise linked failure recovery", priorRun.ExecutionStatus, session)
			}
			if mode == "model change" && (priorRun.ExecutionStatus != "completed" || requests[1].Model != "fixture-model-after-change" || session.PreviousSessionID != "" || session.RecoveryRunID != "") {
				t.Fatal("fixture did not exercise an independent model handoff", priorRun.ExecutionStatus, session)
			}
			if currentRun.Input != followup || currentRun.Source == nil || currentRun.Source.MessageID != input.ID || currentRun.Source.Input == nil || currentRun.Source.Input.Quote == nil || currentRun.Source.Input.Quote.MessageID != "bot-reply" || currentRun.Source.Input.Quote.Body != "Agent-Host 回复" {
				t.Fatal("historical quote replaced the current request or quote", currentRun)
			}
			wantMemoryInputs := []string{followup}
			if mode == "model change" {
				wantMemoryInputs = []string{"Forwarded Markdown message", followup}
			}
			if !reflect.DeepEqual(memoryInputs, wantMemoryInputs) {
				t.Fatal("historical forwarded author text became current sender memory", memoryInputs)
			}
			encodedQuote, err := json.Marshal(priorRun.Source.Input.Quote)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"quote":` + string(encodedQuote), prior.MessageID, followup, "Agent-Host 回复"} {
				if !strings.Contains(requests[1].Prompt, want) {
					t.Errorf("fresh follow-up lost historical associated quote evidence %q", want)
				}
			}
			if len(requests[1].MCPServers) != 0 || strings.Contains(requests[1].Prompt, unverifiedOutput) || strings.Contains(requests[1].Prompt, "session=cap-") {
				t.Fatal("historical quote handoff granted tools or exposed outcome/capabilities", requests[1])
			}
			if !strings.Contains(requests[1].HostInstructions, "associated quote bodies, titles and source message IDs") || !strings.Contains(requests[1].HostInstructions, "not authenticated as the sender") {
				t.Fatal("native execution did not receive quote-review and attribution guidance", requests[1].HostInstructions)
			}
			if got := f.run(t, prior); !reflect.DeepEqual(got, priorRun) {
				t.Fatal("follow-up rewrote the original execution source", got, priorRun)
			}
			f.receive(t, original)
			f.receive(t, input)
			if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) || len(f.runner.snapshot()) != 2 {
				t.Fatal("associated quote context replayed an earlier request", err)
			}
			next := f.process(t, f.message(t, "continue-forwarded-question", "继续解释。", "text", "owner-dm"))
			requests = f.runner.snapshot()
			if len(requests) != 3 || !requests[2].Resume || next.SessionID != current.SessionID || strings.Contains(requests[2].Prompt, title) {
				t.Fatal("historical quote was reinjected after native continuation resumed", next, requests)
			}
		})
	}
}

func TestHistoricalSummaryQuotePreservesSourceFlagsWithoutAttachmentGrants(t *testing.T) {
	for _, mode := range []string{"failed recovery", "model change"} {
		t.Run(mode, func(t *testing.T) {
			f := newProjectFixture(t)
			f.host.Config.ToolsDisabled = false
			download := &inputDownloader{data: []byte("prior private attachment bytes"), mime: "text/plain"}
			f.service.Downloader, f.service.MediaRoot = download, filepath.Join(f.home, "media")
			if mode == "failed recovery" {
				f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
					err := errors.New("native connection closed")
					return harness.Result{Status: "failed", Error: err.Error(), Model: req.Model}, err
				}
			}
			original := inputCallback(t, f.serviceFixture, "attached-quoted-question", "file", "", map[string]string{"fileName": "prior-report.txt", "downloadCode": "PRIOR_ATTACHMENT_SECRET"}, map[string]any{
				"msgId": "original-discussion-id", "msgType": "chatRecord", "content": map[string]string{
					"title": strings.Repeat("题", 270), "summary": "CBS object-storage discussion: " + strings.Repeat("问", 4010), "downloadCode": "QUOTED_MEDIA_SECRET",
				},
			})
			prior := f.process(t, original)
			priorRun := f.run(t, prior)
			if prior.Input == nil || prior.Input.Quote == nil || !prior.Input.Quote.SummaryOnly || !prior.Input.Quote.Truncated || len(prior.Input.Attachments) != 1 || prior.Input.Attachments[0].Path == "" {
				t.Fatal("fixture did not preserve bounded summary and private attachment evidence", prior)
			}
			f.runner.run = nil
			if mode == "model change" {
				f.host.Config.Model = "fixture-model-after-change"
				f.policy = "fixture-model-policy-after-change"
			}
			current := f.process(t, f.message(t, "retry-quoted-summary", "继续", "text", "owner-dm"))
			requests := f.runner.snapshot()
			if current.Status != "completed" || len(requests) != 2 || requests[1].Resume || current.SessionID == prior.SessionID {
				t.Fatal("quoted summary did not enter a fresh native follow-up", current, requests)
			}
			session, err := f.service.Store.Session(context.Background(), current.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "failed recovery" && (priorRun.ExecutionStatus != "failed" || session.PreviousSessionID != prior.SessionID || session.RecoveryRunID != priorRun.ID) {
				t.Fatal("summary fixture did not exercise linked failure recovery", priorRun.ExecutionStatus, session)
			}
			if mode == "model change" && (priorRun.ExecutionStatus != "completed" || requests[1].Model != "fixture-model-after-change" || session.PreviousSessionID != "" || session.RecoveryRunID != "") {
				t.Fatal("summary fixture did not exercise an independent model handoff", priorRun.ExecutionStatus, session)
			}
			quote := *prior.Input.Quote
			quote.Body = string([]rune(quote.Body)[:4000])
			quote.Title = string([]rune(quote.Title)[:256])
			encodedQuote, err := json.Marshal(quote)
			if err != nil || !strings.Contains(requests[1].Prompt, `"quote":`+string(encodedQuote)) {
				t.Fatal("handoff lost historical quote ID, body, title, or preview flags", err)
			}
			attachment := prior.Input.Attachments[0]
			for _, unwanted := range []string{attachment.Path, filepath.Dir(attachment.Path), attachment.SHA256, "PRIOR_ATTACHMENT_SECRET", "QUOTED_MEDIA_SECRET", "session=cap-"} {
				if strings.Contains(requests[1].Prompt, unwanted) {
					t.Errorf("historical quote handoff exposed attachment authority or capabilities %q", unwanted)
				}
			}
			if len(download.calls) != 1 || len(requests[1].Images) != 0 || len(requests[1].AdditionalDirectories) != 0 || len(requests[1].MCPServers) != 0 {
				t.Fatal("historical quote fetched media or added attachment/history access", download.calls, requests[1])
			}
			if got := f.run(t, prior); !reflect.DeepEqual(got, priorRun) {
				t.Fatal("quote truncation rewrote saved source evidence", got, priorRun)
			}
			currentRun := f.run(t, current)
			if currentRun.Input != "继续" || currentRun.Source == nil || currentRun.Source.MessageID != current.MessageID || currentRun.Source.Input != nil {
				t.Fatal("historical quote became the current message material", currentRun)
			}
		})
	}
}
