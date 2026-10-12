package channel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"agent-host/internal/routing"
)

func TestConversationHelpStatusAndUnsupportedCommandsNeverExecute(t *testing.T) {
	f := newRoutingFixture(t)
	for i, text := range []string{"/help", "/status", "/compact", "/model another", "/unknown-command"} {
		m := f.process(t, f.message(t, fmt.Sprintf("command-%d", i), text, "text", "dm"))
		if m.Status != "completed" || m.RunID != "" || m.SessionID != "" || len(m.Reactions) != 0 || len(f.runner.snapshot()) != 0 {
			t.Fatal("conversation command created execution or reactions", m)
		}
		if text != "/status" && (!strings.Contains(m.ReplyText, "/new") || !strings.Contains(m.ReplyText, "/status") || !strings.Contains(m.ReplyText, "/help")) {
			t.Fatal("unsupported command did not state the supported surface", m.ReplyText)
		}
		for _, hidden := range []string{f.host.Config.Model, f.host.Config.Scope, f.policy, "claude", "/choose", "/routing"} {
			if strings.Contains(m.ReplyText, hidden) {
				t.Fatal("conversation help/status disclosed internals or old controls", m.ReplyText)
			}
		}
	}
	for _, text := range []string{"/help extra", "/status extra", "/new extra"} {
		m := f.process(t, f.message(t, text, text, "text", "dm"))
		if m.Status != "failed" || m.RunID != "" || len(m.Reactions) != 0 {
			t.Fatal("conversation command arguments were treated as a task", m)
		}
	}
}

func TestAbsolutePathTasksKeepRoutingExecutionAndFeedback(t *testing.T) {
	f := newRoutingFixture(t)
	var inputs []string
	f.service.Router = serviceRouter(func(_ context.Context, r routing.Request) (routing.Decision, error) {
		inputs = append(inputs, r.Text)
		return routing.Decision{Kind: "default"}, nil
	})
	instanceDispatch(t, f, map[string]string{})
	for i, text := range []string{"/tmp/example.txt please inspect the file", "/ please list names", "/example.txt please summarize"} {
		m := processAddressedGroup(t, f, fmt.Sprintf("path-%d", i), "alice", "@Helper "+text)
		if m.Status != "completed" || m.RunID == "" || len(m.Reactions) == 0 || inputs[i] != text {
			t.Fatal("absolute path was swallowed by the command parser", m, inputs)
		}
		run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(context.Background(), m.RunID)
		if err != nil || run.Input != m.Text {
			t.Fatal("path task lost raw input evidence", run, err)
		}
	}
}

func TestGroupStatusCountsOnlyRequestersPendingTasksWithoutClaimingExactLimit(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	for i := range 11 {
		m := legacyPending(t, f, groupInput(t, f.serviceFixture, fmt.Sprintf("own-%d", i), "alice", "group", "ambiguous private request"))
		if m.Status != "awaiting" {
			t.Fatal(m)
		}
	}
	legacyPending(t, f, groupInput(t, f.serviceFixture, "other", "bob", "group", "ambiguous other member request"))
	for _, tc := range []struct{ sender, want string }{{"alice", "至少 10 项"}, {"bob", "1 项"}} {
		m := f.process(t, groupInput(t, f.serviceFixture, "status-"+tc.sender, tc.sender, "group", "/status"))
		if m.Status != "completed" || !strings.Contains(m.ReplyText, tc.want) || strings.Contains(m.ReplyText, "private request") || strings.Contains(m.ReplyText, "other member request") {
			t.Fatal("status leaked another member or overstated pending count", m)
		}
	}
}

func TestNumberedCancellationIsOwnedAndDoesNotCancelOtherRequests(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	one := legacyPending(t, f, groupInput(t, f.serviceFixture, "one", "alice", "group", "ambiguous first request"))
	legacyPending(t, f, groupInput(t, f.serviceFixture, "two", "alice", "group", "ambiguous second request"))
	wrong := f.process(t, groupInput(t, f.serviceFixture, "wrong", "bob", "group", "取消 "+choiceFor(one)))
	if wrong.Status != "failed" || f.receipt(t, "one").Status != "awaiting" {
		t.Fatal("another member canceled the original request", wrong)
	}
	for i := range 2 {
		m := f.process(t, groupInput(t, f.serviceFixture, fmt.Sprintf("cancel-%d", i), "alice", "group", "取消 "+choiceFor(one)))
		if m.Status != "completed" || f.receipt(t, "one").Status != "failed" || f.receipt(t, "two").Status != "awaiting" || len(f.runner.snapshot()) != 0 {
			t.Fatal("numbered cancellation replayed or consumed the wrong request", m)
		}
	}
	if _, err := f.service.Store.ChannelSelection(context.Background(), one.Binding); err == nil {
		t.Fatal("natural confirmation changed persistent routing configuration")
	}
}

func TestStatusDoesNotExposeRuntimeDataOrClaimLatestRouteFailureSucceeded(t *testing.T) {
	f := newRoutingFixture(t)
	first := f.process(t, f.message(t, "first", "Inspect alpha", "text", "dm"))
	f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
		return routing.Decision{Kind: "project", ProjectIDs: []string{"missing"}, PrimaryID: "missing"}, nil
	})
	failed := f.process(t, f.message(t, "route-failure", "Inspect missing", "text", "dm"))
	status := f.process(t, f.message(t, "status", "/status", "text", "dm"))
	if failed.Status != "failed" || status.Status != "completed" || !strings.Contains(status.ReplyText, "最近进入执行的任务：已完成") || strings.Contains(status.ReplyText, "最近任务：已完成") {
		t.Fatal("status presented an earlier execution as the latest request result", status)
	}
	for _, hidden := range []string{first.SessionID, first.Workspace, first.RuntimeFingerprint, f.host.Config.Model, first.ID} {
		if hidden != "" && strings.Contains(status.ReplyText, hidden) {
			t.Fatal("status exposed runtime details", status.ReplyText)
		}
	}
}
