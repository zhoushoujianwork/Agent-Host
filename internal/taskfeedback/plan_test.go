package taskfeedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"agent-host/internal/decision/jev"
)

type evaluatorFunc func(context.Context, any, map[string]jev.Question) (jev.Response, error)

func (f evaluatorFunc) Evaluate(c context.Context, v any, q map[string]jev.Question) (jev.Response, error) {
	return f(c, v, q)
}

func fixtureResolver(choice string) Resolver {
	return Resolver{Users: []User{{ID: "alice", DisplayName: "Alice", Aliases: []string{"张三"}, Accounts: []Account{{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_alice"}}}}, Evaluator: evaluatorFunc(func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		confidence := 1.0
		return jev.Response{Answers: map[string]jev.Answer{"demand_owner": {Type: "choice", Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: &confidence}}}, nil
	})}
}

func TestResolveOwnershipAndExplicitPrecedence(t *testing.T) {
	origin := &Origin{Account: Account{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"}, MessageID: "original"}
	for _, tc := range []struct {
		name, choice string
		req          Request
		mode, owner  string
	}{
		{"owner transfer", "user:alice", Request{Text: "请处理张三自己的登录问题", Requester: "owner", Origin: origin, Interpret: true}, "notify", "alice"},
		{"data subject", "self", Request{Text: "查看 Alice 提交的日志", Requester: "owner", Origin: origin, Interpret: true}, "origin", "owner"},
		{"member mention", "unresolved", Request{Text: "Please help", Requester: "alice", Origin: origin}, "origin", "alice"},
		{"CLI explicit", "unresolved", Request{Requester: "owner", RequestFor: "alice", Interpret: true}, "notify", "alice"},
		{"CLI inferred", "user:alice", Request{Text: "Handle Alice own login issue", Requester: "owner", Interpret: true}, "notify", "alice"},
		{"CLI ordinary", "self", Request{Text: "Check service health", Requester: "owner", Interpret: true}, "terminal", "owner"},
		{"ambiguous name", "unresolved", Request{Text: "处理李四的问题", Requester: "owner", Interpret: true}, "needs_recipient", ""},
		{"hallucinated owner", "user:alice", Request{Text: "Check service health", Requester: "owner", Interpret: true}, "needs_recipient", ""},
		{"explicit unknown", "self", Request{Requester: "owner", RequestFor: "missing"}, "needs_recipient", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixtureResolver(tc.choice).Resolve(t.Context(), tc.req)
			if p.Mode != tc.mode || p.RequestFor != tc.owner {
				t.Fatalf("got %+v", p)
			}
		})
	}
}

func TestContinuationAndAccountChanges(t *testing.T) {
	r := fixtureResolver("continue")
	previous := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice"})
	p := r.Resolve(t.Context(), Request{Text: "Continue the remaining checks", Requester: "owner", Previous: &previous, Interpret: true})
	if p.Mode != "notify" || p.NotifyTo != "alice" {
		t.Fatal(p)
	}
	r.Users[0].Accounts[0].SenderID = "ou_changed"
	if r.ValidateTarget(p) == nil {
		t.Fatal("changed account accepted")
	}
	r = fixtureResolver("self")
	r.Users[0].Accounts = append(r.Users[0].Accounts, Account{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "alice"})
	if p := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice"}); p.Mode != "needs_recipient" {
		t.Fatal("ambiguous channels selected silently", p)
	}
	if p := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice", Channel: "feishu"}); p.Mode != "notify" {
		t.Fatal(p)
	}
	r.Evaluator = evaluatorFunc(func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		return jev.Response{}, errors.New("provider down")
	})
	if p := r.Resolve(t.Context(), Request{Requester: "owner", Interpret: true}); p.Mode != "needs_recipient" || p.Reason != "ownership_classification_failed" {
		t.Fatal(p)
	}
}

func TestSemanticRecipientRequiresConfidenceAndUniqueName(t *testing.T) {
	r := fixtureResolver("user:alice")
	weak := 0.6
	r.Evaluator = evaluatorFunc(func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		return jev.Response{Answers: map[string]jev.Answer{"demand_owner": {Type: "choice", Choice: "user:alice", Probabilities: map[string]float64{"user:alice": 0.6}, Confidence: &weak}}}, nil
	})
	req := Request{Text: "Handle Alice issue", Requester: "owner", Interpret: true}
	if p := r.Resolve(t.Context(), req); p.Mode != "needs_recipient" || p.Reason != "ownership_decision_not_confident" {
		t.Fatal(p)
	}
	r = fixtureResolver("user:alice")
	r.Users = append(r.Users, User{ID: "another-alice", DisplayName: "Alice"})
	if p := r.Resolve(t.Context(), req); p.Mode != "needs_recipient" || p.Reason != "demand_owner_name_ambiguous" {
		t.Fatal(p)
	}
	r = fixtureResolver("self")
	r.Users = append(r.Users, User{ID: "owner", NotifyChannel: "feishu", Accounts: []Account{{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"}}})
	if p := r.Resolve(t.Context(), Request{Text: "Check my service", Requester: "owner", Interpret: true}); p.Mode != "notify" || p.NotifyTo != "owner" {
		t.Fatal("configured CLI requester lost default notification", p)
	}
}

func TestOtherUsersOnlyKeepsSelfFeedbackLocal(t *testing.T) {
	r := fixtureResolver("self")
	r.Users = append(r.Users, User{ID: "owner", Accounts: []Account{
		{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"},
		{Channel: "dingtalk", AccountID: "bot", TenantID: "corp", SenderID: "owner"},
	}})
	for _, req := range []Request{
		{Text: "Check my service", Requester: "owner", Interpret: true},
		{Requester: "owner", NotifyTo: "owner", Channel: "feishu"},
		{Requester: "owner", RequestFor: "owner"},
	} {
		req.OtherUsersOnly = true
		p := r.Resolve(t.Context(), req)
		if p.Mode != "terminal" || p.NotifyTo != "owner" || p.Target != nil {
			t.Fatal("self feedback escaped local entry", p)
		}
	}
	r = fixtureResolver("user:alice")
	previous := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice"})
	for _, choice := range []string{"user:alice", "continue", "unresolved"} {
		r.Evaluator = fixtureResolver(choice).Evaluator
		p := r.Resolve(t.Context(), Request{Text: "Handle Alice issue", Requester: "owner", Interpret: true, OtherUsersOnly: true, Previous: &previous})
		if choice == "unresolved" {
			if p.Mode != "needs_recipient" || p.Target != nil {
				t.Fatal("unresolved recipient acquired a target", p)
			}
		} else if p.Mode != "notify" || p.NotifyTo != "alice" || p.Target == nil {
			t.Fatal("other recipient lost delivery", p)
		}
	}
}

func TestLargeDirectoryOnlyProvidesMentionedCandidates(t *testing.T) {
	r := fixtureResolver("user:alice")
	for i := range 1000 {
		r.Users = append(r.Users, User{ID: fmt.Sprintf("member-%d", i), DisplayName: fmt.Sprintf("Peer %d", i)})
	}
	inner := r.Evaluator
	r.Evaluator = evaluatorFunc(func(ctx context.Context, input any, questions map[string]jev.Question) (jev.Response, error) {
		if len(questions["demand_owner"].Criteria.(map[string]string)) != 3 || len(input.(map[string]any)["verified_user_directory"].([]map[string]any)) != 1 {
			t.Fatal("irrelevant directory was sent to provider")
		}
		return inner.Evaluate(ctx, input, questions)
	})
	if p := r.Resolve(t.Context(), Request{Text: "Handle Alice issue", Requester: "owner", Interpret: true}); p.Mode != "notify" {
		t.Fatal(p)
	}
}

func TestNotificationChannelFollowsOriginBeforeCLIDefault(t *testing.T) {
	r := fixtureResolver("user:alice")
	r.Users[0].NotifyChannel = "dingtalk"
	r.Users[0].Accounts = append(r.Users[0].Accounts, Account{Channel: "dingtalk", AccountID: "bot", TenantID: "corp", SenderID: "alice-staff"})
	origin := &Origin{Account: Account{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"}, MessageID: "source"}
	for _, override := range []string{"", "dingtalk"} {
		p := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice", Origin: origin, Channel: override})
		if p.Mode != "notify" || p.Target == nil || p.Target.Channel != "feishu" {
			t.Fatalf("source channel overridden: %+v", p)
		}
		p = r.Resolve(t.Context(), Request{Requester: "alice", Origin: origin, Channel: override})
		if p.Mode != "origin" || p.Target == nil || p.Target.Channel != "feishu" {
			t.Fatalf("original reply redirected: %+v", p)
		}
	}
	p := r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice"})
	if p.Mode != "notify" || p.Target.Channel != "dingtalk" {
		t.Fatalf("CLI default ignored: %+v", p)
	}
	p = r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice", Channel: "feishu"})
	if p.Mode != "notify" || p.Target.Channel != "feishu" {
		t.Fatalf("explicit CLI channel ignored: %+v", p)
	}
	r.Users[0].Accounts = r.Users[0].Accounts[1:]
	p = r.Resolve(t.Context(), Request{Requester: "owner", RequestFor: "alice", Origin: origin})
	if p.Mode != "needs_recipient" || p.Target != nil {
		t.Fatalf("missing source-channel account fell back: %+v", p)
	}
}

func TestLiveOwnershipAttribution(t *testing.T) {
	if os.Getenv("AGH_FEEDBACK_LIVE") != "1" {
		t.Skip("explicit live classifier check")
	}
	client, err := jev.NewCustom(os.Getenv("JEV_API_KEY"), os.Getenv("AGH_FEEDBACK_MODEL"), os.Getenv("AGH_FEEDBACK_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	r := fixtureResolver("")
	r.Evaluator = client
	for _, tc := range []struct{ text, want string }{
		{"请处理张三遇到的登录问题，结束后把处理结果反馈给他。", "notify"},
		{"请查看张三提交的日志，告诉我服务是否正常。", "terminal"},
		{"Check Agent-Host service health for me.", "terminal"},
		{"请帮李四处理他的登录问题。", "needs_recipient"},
	} {
		t.Run(tc.want+tc.text, func(t *testing.T) {
			p := r.Resolve(t.Context(), Request{Text: tc.text, Requester: "owner", Interpret: true})
			if p.Mode != tc.want {
				t.Fatalf("got %+v", p)
			}
			var decision jev.Response
			_ = json.Unmarshal(p.Decision, &decision)
			a := decision.Answers["demand_owner"]
			t.Logf("%s => %s (%s); choice=%s probability=%.3f confidence=%v", tc.text, p.Mode, p.RequestFor, a.Choice, a.Probabilities[a.Choice], a.Confidence)
		})
	}
}
