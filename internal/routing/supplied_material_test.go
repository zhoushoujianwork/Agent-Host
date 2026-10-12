package routing

import (
	"context"
	"reflect"
	"testing"

	"agent-host/internal/decision/jev"
)

// These synthetic cases verify routing contracts around supplied material.
// The evaluator's answers are controlled; they do not measure model accuracy.
func TestSuppliedMaterialWithProjectNamesUsesSemanticNoTargetDecision(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"log excerpt", "只分析以下样例日志并说明可能原因，不访问任何实际环境：\n[Billing API] attempt=2 elapsed_ms=750 reason=deadline\n再建议几项可以手工验证的步骤。"},
		{"code snippet", "Explain the exception in this pasted example using only the shown code:\n```python\nservice = 'alpha'\nvalues = [4, 0]\nprint(values[0] / values[1])\n```"},
		{"data table", "仅根据这张表解释失败率变化，不查询服务：\nservice,requests,failures\nIdentity Service,12,3\nIdentity Service,20,2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Text: tc.text, Projects: catalog()}
			if d, exact, err := ExactDecision(req); err != nil || exact {
				t.Fatalf("a project name inside supplied material became an exact target: %+v exact=%v err=%v", d, exact, err)
			}
			f := &fakeEvaluator{choices: map[string]string{
				"mode": "general", "association": "general", "project_need": "not_required",
				"p0": "related", "p1": "related",
			}}
			d, err := New(f).Decide(context.Background(), req)
			if err != nil || f.calls != 1 || d.Kind != "default" || len(d.ProjectIDs) != 0 || d.PrimaryID != "" || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("consistent material-only analysis selected project execution: %+v calls=%d err=%v", d, f.calls, err)
			}
			s, ok := f.state.(routingState)
			if !ok || s.Request != req.Text || len(d.Details) == 0 {
				t.Fatal("semantic classification lost the exact supplied evidence", f.state, d)
			}
		})
	}
}

func TestReadOnlyDeploymentAccessStillRequiresResolvedProject(t *testing.T) {
	for _, tc := range []struct {
		name, text, mode, association, want string
		known                               bool
	}{
		{"known deployment", "不要修改任何文件；请连接 Billing API 的部署环境，收集当前实例日志后诊断错误。", "project", "known", "project", true},
		{"unresolved deployment", "只读连接尚未标明所属项目的部署并获取最新运行日志，帮我检查当前健康状态。", "clarify", "unknown", "clarify", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choices := map[string]string{"mode": tc.mode, "association": tc.association, "project_need": "required"}
			if tc.known {
				choices["p0"], choices["e_p0"], choices["primary"] = "target", "explicit", "p0"
			}
			f := &fakeEvaluator{choices: choices}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog()})
			if err != nil || f.calls != 1 || d.Kind != tc.want {
				t.Fatalf("no-write restriction erased deployment access requirements: %+v calls=%d err=%v", d, f.calls, err)
			}
			if tc.known && (!reflect.DeepEqual(d.ProjectIDs, []string{"alpha"}) || d.PrimaryID != "alpha") || !tc.known && len(d.ProjectIDs) != 0 {
				t.Fatal("deployment target was invented or lost", d)
			}
		})
	}
}

func TestIndependentSuppliedMaterialDoesNotInheritRecentEngineeringTask(t *testing.T) {
	latest := TaskContext{MessageID: "latest-deployment-message", RunID: "latest-deployment-run", Text: "Inspect the current beta deployment", ProjectIDs: []string{"beta"}, PrimaryID: "beta"}
	for _, tc := range []struct {
		name, text, want string
		choices          map[string]string
	}{
		{
			name: "independent pasted log", text: "这是一道独立的样例分析题，请只解释下面内容：\nIdentity Service: queue=5 workers=0\n不要查询任何部署，也不要延续先前任务。", want: "default",
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "latest_relation": "independent", "scope_change": "change", "p1": "related"},
		},
		{
			name: "real deployment followup", text: "Now read that deployment's live health checks; keep the investigation read-only.", want: "project",
			choices: map[string]string{"mode": "continuation", "association": "known", "project_need": "required", "latest_relation": "followup", "scope_change": "preserve", "p1": "target", "e_p1": "recent"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: tc.choices}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog(), Recent: []TaskContext{latest}})
			if err != nil || f.calls != 1 || d.Kind != tc.want {
				t.Fatalf("supplied content and deployment followup were conflated: %+v calls=%d err=%v", d, f.calls, err)
			}
			if tc.want == "default" {
				if len(d.ProjectIDs) != 0 || d.PrimaryID != "" || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
					t.Fatal("independent sample analysis inherited an engineering environment", d)
				}
			} else if !reflect.DeepEqual(d.ProjectIDs, latest.ProjectIDs) || d.PrimaryID != latest.PrimaryID || d.ContinuationMessageID != latest.MessageID || d.ContinuationRunID != latest.RunID {
				t.Fatal("actual followup lost the verified task source", d)
			}
		})
	}
}

func TestSuppliedMaterialDoesNotBypassUncertainProjectNeed(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "p0": "related"}, edit: func(r *jev.Response) {
		answer := r.Answers["project_need"]
		answer.Probabilities = map[string]float64{"not_required": 0.89, "required": 0, "uncertain": 0.11}
		r.Answers["project_need"] = answer
	}}
	d, err := New(f).Decide(context.Background(), Request{Text: "Explain this supplied line: Billing API response_code=503. Use only the example.", Projects: catalog()})
	if err != nil || f.calls != 1 || d.Kind != "clarify" || len(d.ProjectIDs) != 0 {
		t.Fatalf("supplied-material wording bypassed the unchanged reliability requirement: %+v calls=%d err=%v", d, f.calls, err)
	}
}
