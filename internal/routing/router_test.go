package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

type fakeEvaluator struct {
	choices map[string]string
	edit    func(*jev.Response)
	err     error
	calls   int
	state   any
}

func (f *fakeEvaluator) Provider() string { return "test" }
func (f *fakeEvaluator) Endpoint() string { return "https://example.invalid" }
func (f *fakeEvaluator) Model() string    { return "test-model" }
func (f *fakeEvaluator) Evaluate(_ context.Context, s any, questions map[string]jev.Question) (jev.Response, error) {
	f.calls++
	f.state = s
	if f.err != nil {
		return jev.Response{}, f.err
	}
	result := jev.Response{Model: "test-model", Answers: map[string]jev.Answer{}}
	for key, q := range questions {
		criteria := q.Criteria.(map[string]string)
		selected := f.choices[key]
		if selected == "" {
			switch {
			case key == "mode":
				selected = "clarify"
			case key == "association":
				selected = "unknown"
			case key == "project_need":
				selected = "required"
				if f.choices["association"] == "general" {
					selected = "not_required"
				}
			case key == "latest_relation":
				selected = "independent"
			case key == "scope_change":
				selected = "change"
			case key == "primary", strings.HasPrefix(key, "e_"):
				selected = "none"
			default:
				selected = "unrelated"
			}
		}
		probs := map[string]float64{}
		for option := range criteria {
			probs[option] = 0
		}
		probs[selected] = 1
		result.Answers[key] = jev.Answer{Type: "choice", Choice: selected, Probabilities: probs}
	}
	if f.edit != nil {
		f.edit(&result)
	}
	return result, nil
}

func catalog() []state.Project {
	return []state.Project{
		{ID: "alpha", Name: "Billing API", Description: "Payment processing, settlement and invoice generation.", Aliases: []string{"账单", "https://github.com/example/billing"}, Workspace: "/private/alpha-checkout", Scope: "private-alpha-memory"},
		{ID: "beta", Name: "Identity Service", Description: "User authentication, login and account registration.", Aliases: []string{"账户服务"}, Workspace: "/private/beta-checkout", Scope: "private-beta-memory"},
	}
}

func TestExactReferencesSelectOnlyKnownProjects(t *testing.T) {
	for _, tc := range []struct {
		text    string
		ids     []string
		primary string
	}{
		{"Fix alpha", []string{"alpha"}, "alpha"},
		{"Fix project alpha.", []string{"alpha"}, "alpha"},
		{"Fix alpha. Then report the result.", []string{"alpha"}, "alpha"},
		{"Fix alpha...", []string{"alpha"}, "alpha"},
		{"Fix (alpha.)", []string{"alpha"}, "alpha"},
		{"Fix the Billing API tests", []string{"alpha"}, "alpha"},
		{"修改 账单 的测试", []string{"alpha"}, "alpha"},
		{"Inspect https://github.com/example/billing", []string{"alpha"}, "alpha"},
		{"同时修改 beta 和 alpha 的接口", []string{"beta", "alpha"}, ""},
		{"以 alpha 为主，联调 beta 的接口", []string{"alpha", "beta"}, "alpha"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			f := &fakeEvaluator{err: errors.New("must not call provider")}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog()})
			if err != nil || d.Kind != "project" || !reflect.DeepEqual(d.ProjectIDs, tc.ids) || d.PrimaryID != tc.primary || f.calls != 0 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			if len(d.Evidence) != len(tc.ids) {
				t.Fatalf("missing explicit evidence: %+v", d.Evidence)
			}
		})
	}
}

func TestExplicitProjectWithExecutionConstraintsAvoidsSemanticRouting(t *testing.T) {
	projects := append(catalog(), state.Project{ID: "dt-alpha", Name: "DingTalk alpha"})
	for _, tc := range []struct {
		text string
		id   string
	}{
		{"dt-alpha：使用 Read 读取 README.md，报告本项目规则标记。这个原生会话的临时标记是 DT_ONLY_A_20261010，不要保存为记忆。", "dt-alpha"},
		{"alpha 读取 README，不要修改文件", "alpha"},
		{"alpha 读取 README 不要修改文件", "alpha"},
		{"读取 alpha 的 README，不要保存记忆。", "alpha"},
		{"Read alpha README; do not modify files", "alpha"},
		{"Read alpha README; don't remember this", "alpha"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			f := &fakeEvaluator{err: errors.New("ordinary execution constraints must not require the provider")}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: projects})
			if err != nil || d.Kind != "project" || d.PrimaryID != tc.id || f.calls != 0 || len(d.ProjectIDs) != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestExecutionConstraintDoesNotHideRoutingExclusionOrUnknownTarget(t *testing.T) {
	for _, text := range []string{
		"不要改 alpha，改 beta，不要修改文件",
		"不要修改文件 alpha，改 beta",
		"Integrate alpha with gamma; do not modify files",
		"Integrate alpha with https://github.com/acme/gamma; do not modify files",
		"参考 alpha 的实现，不要保存为记忆",
		"比较 alpha 和 beta 以及 gamma，不要修改文件。",
		"Compare alpha and beta and gamma; do not modify files",
		"不要比较 alpha 和 beta",
		"参考 alpha，对比 beta",
	} {
		t.Run(text, func(t *testing.T) {
			_, ok, err := ExactDecision(Request{Text: text, Projects: catalog()})
			if err != nil || ok {
				t.Fatalf("ambiguous project target bypassed classification: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestExplicitProjectSessionRequestOverridesLatestJointRoute(t *testing.T) {
	projects := []state.Project{{ID: "dt-alpha", Name: "dt-alpha"}, {ID: "dt-beta", Name: "dt-beta"}}
	recent := []TaskContext{{MessageID: "latest-joint-message", RunID: "latest-joint-run", Text: "比较 dt-alpha 和 dt-beta 的 README.md", ProjectIDs: []string{"dt-alpha", "dt-beta"}}}
	for _, text := range []string{
		"dt-alpha：报告本原生会话先前收到的 DT_ONLY_A 临时标记，不要读取文件。",
		"dt-alpha：报告本原生会话先前收到的临时标记，不要读取任何文件。",
		"dt-alpha: report the earlier marker in this native session; do not read files.",
	} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{err: errors.New("explicit project selection must not depend on semantic probability")}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: projects, Recent: recent})
			if err != nil || d.Kind != "project" || d.PrimaryID != "dt-alpha" || !reflect.DeepEqual(d.ProjectIDs, []string{"dt-alpha"}) || f.calls != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("explicit project inherited latest joint route: decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestDWSExplicitTaskSequenceDoesNotDependOnSemanticProvider(t *testing.T) {
	projects := []state.Project{{ID: "dt-alpha", Name: "dt-alpha"}, {ID: "dt-beta", Name: "dt-beta"}}
	f := &fakeEvaluator{err: errors.New("explicit platform tasks must not require a classifier")}
	var recent []TaskContext
	for _, tc := range []struct {
		label, text, primary string
		ids                  []string
	}{
		{"a", "dt-alpha：使用 Read 读取 README.md，报告本项目规则标记。这个原生会话的临时标记是 DT_ONLY_A_20261010，不要保存为记忆。", "dt-alpha", []string{"dt-alpha"}},
		{"b", "dt-beta：使用 Read 读取 README.md，报告本项目规则标记。", "dt-beta", []string{"dt-beta"}},
		{"auto-joint", "比较 dt-alpha 和 dt-beta 的 README.md，报告各自项目规则标记，不要修改文件。", "", []string{"dt-alpha", "dt-beta"}},
		{"return-a", "dt-alpha：报告本原生会话先前收到的 DT_ONLY_A 临时标记，不要读取文件。", "dt-alpha", []string{"dt-alpha"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: projects, Recent: recent})
			if err != nil || d.Kind != "project" || d.PrimaryID != tc.primary || !reflect.DeepEqual(d.ProjectIDs, tc.ids) || f.calls != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("explicit task requires uncertain classification: decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			recent = []TaskContext{{MessageID: tc.label, RunID: "run-" + tc.label, Text: tc.text, ProjectIDs: d.ProjectIDs, PrimaryID: d.PrimaryID}}
		})
	}
}

func TestGeneralConceptDoesNotSelectProjectByCommonWord(t *testing.T) {
	projects := []state.Project{{ID: "api", Name: "API"}}
	for _, text := range []string{"什么是 API？", "What is an API?", "请解释一下 API"} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: projects})
			if err != nil || d.Kind != "default" || f.calls != 1 {
				t.Fatalf("concept question selected a common-word project: decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
	d, err := New(nil).Decide(context.Background(), Request{Text: "api：修复登录测试", Projects: projects})
	if err != nil || d.Kind != "project" || d.PrimaryID != "api" {
		t.Fatalf("explicit API project lost its target: %+v err=%v", d, err)
	}
}

func TestSemanticPurposeUsesBoundedDescriptorsAndIndependentAnswers(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "purpose"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "支付完成后账单金额不一致，检查结算计算。", Projects: catalog()})
	if err != nil || d.Kind != "project" || d.PrimaryID != "alpha" || f.calls != 1 {
		t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
	}
	encoded, err := json.Marshal(f.state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/private/") || strings.Contains(string(encoded), "private-alpha-memory") {
		t.Fatalf("classifier received directory or memory scope: %s", encoded)
	}
	if len(d.Details) == 0 || len(d.Evidence) != 1 || d.Evidence[0].Kind != "purpose" {
		t.Fatalf("semantic routing evidence absent: %+v", d)
	}
}

func TestNegationExcludesAnExplicitProject(t *testing.T) {
	for _, text := range []string{"不要改 alpha，改 beta 的登录逻辑", "Do not edit alpha; edit beta instead", "不修改 alpha，修改 beta"} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p1", "p0": "excluded", "p1": "target", "e_p1": "explicit"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog()})
			if err != nil || d.Kind != "project" || d.PrimaryID != "beta" || !reflect.DeepEqual(d.ProjectIDs, []string{"beta"}) || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestNumericCertaintyCannotOverrideAnExplicitExclusion(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "explicit", "p1": "target", "e_p1": "explicit"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "不要改 alpha，改 beta", Projects: catalog()})
	if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 {
		t.Fatalf("model certainty overrode exclusion: %+v err=%v", d, err)
	}
}

func TestMentionedReferenceIsNotAnExecutionTarget(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p1", "p0": "unrelated", "p1": "target", "e_p1": "explicit"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "参考 alpha 的命名，修改 beta 的登录", Projects: catalog()})
	if err != nil || !reflect.DeepEqual(d.ProjectIDs, []string{"beta"}) || f.calls != 1 {
		t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
	}
}

func TestWordBoundariesPreventProjectSubstringSelection(t *testing.T) {
	for _, text := range []string{"Explain alphabet", "Explain alpha-tools", "Explain superalpha", "Explain alpha.api", "Explain alpha..api", "Explain alpha./api", "Explain https://github.com/example/billing.tools", "Explain https://github.com/example/billing/other"} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog()})
			if err != nil || d.Kind != "default" || f.calls != 1 {
				t.Fatalf("substring selected a project: decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestUnknownNamedProjectNeverUsesPartialKnownMatch(t *testing.T) {
	for _, text := range []string{"Change alpha and project delta", "同时修改 alpha 和 delta 的接口", "修复 delta 项目", "Integrate alpha with gamma", "Migrate alpha to gamma", "Integrate alpha with https://github.com/acme/gamma", "修改 alpha 以及 delta"} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "unknown", "primary": "p0", "p0": "target", "e_p0": "explicit"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog()})
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestExactDecisionDoesNotRequireSemanticProviderForManualPins(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
		id   string
	}{
		{"Fix beta", true, "beta"},
		{"修一下登录问题", false, ""},
		{"Integrate alpha with unknown", false, ""},
		{"参考 alpha 的例子", false, ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			d, ok, err := ExactDecision(Request{Text: tc.text, Projects: catalog()})
			if err != nil || ok != tc.ok || d.PrimaryID != tc.id {
				t.Fatalf("decision=%+v ok=%v err=%v", d, ok, err)
			}
		})
	}
}

func TestSharedAliasRequiresDisambiguation(t *testing.T) {
	projects := catalog()
	projects[0].Aliases, projects[1].Aliases = []string{"backend"}, []string{"backend"}
	f := &fakeEvaluator{choices: map[string]string{"p0": "related", "p1": "related"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "Fix backend", Projects: projects})
	if err != nil || d.Kind != "clarify" || len(d.CandidateIDs) != 2 || f.calls != 1 {
		t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
	}
}

func TestUnclearEngineeringDoesNotChooseTheOnlyProjectOrDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choices map[string]string
	}{
		{"unknown", map[string]string{"mode": "clarify", "association": "unknown", "p0": "related"}},
		{"project need contradicts general", map[string]string{"mode": "general", "association": "general", "project_need": "required"}},
		{"unsubstantiated purpose", map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "purpose"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: tc.choices}
			d, err := New(f).Decide(context.Background(), Request{Text: "修一下登录问题", Projects: []state.Project{{ID: "only", Name: "Only"}}})
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestContinuationUsesOnlyTheLatestSourceLinkedTask(t *testing.T) {
	latest := TaskContext{MessageID: "msg-beta", RunID: "run-beta", Text: "Check beta", ProjectIDs: []string{"beta"}, PrimaryID: "beta"}
	older := TaskContext{MessageID: "msg-alpha", Text: "Check alpha", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}
	d, err := New(nil).Decide(context.Background(), Request{Text: "继续刚才的任务", Projects: catalog(), Recent: []TaskContext{latest, older}})
	if err != nil || d.Kind != "project" || d.PrimaryID != "beta" || d.ContinuationMessageID != "msg-beta" || d.ContinuationRunID != "run-beta" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	latest.ProjectIDs, latest.PrimaryID = nil, ""
	d, err = New(nil).Decide(context.Background(), Request{Text: "继续", Projects: catalog(), Recent: []TaskContext{latest}})
	if err != nil || d.Kind != "default" || d.ContinuationMessageID != "msg-beta" {
		t.Fatalf("default continuation=%+v err=%v", d, err)
	}
}

func TestSemanticContinuationRequiresIndependentRelationAndCompatibleLatestProject(t *testing.T) {
	for _, text := range []string{"继续刚才的测试", "帮我制定旅行计划", "继续 alpha 的测试"} {
		t.Run(text, func(t *testing.T) {
			relation := "followup"
			if text == "帮我制定旅行计划" {
				relation = "independent"
			}
			f := &fakeEvaluator{choices: map[string]string{"mode": "continuation", "association": "known", "latest_relation": relation, "scope_change": "preserve", "p1": "target", "e_p1": "recent"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog(), Recent: []TaskContext{{MessageID: "recent", Text: "Investigate identity tests", ProjectIDs: []string{"beta"}, PrimaryID: "beta"}}})
			if err != nil {
				t.Fatal(err)
			}
			switch text {
			case "继续刚才的测试":
				if d.PrimaryID != "beta" || d.ContinuationMessageID != "recent" {
					t.Fatalf("missing continuation source: %+v", d)
				}
			case "帮我制定旅行计划":
				if d.Kind != "clarify" {
					t.Fatalf("ungrounded continuation: %+v", d)
				}
			default:
				if d.PrimaryID != "alpha" || d.ContinuationMessageID != "" {
					t.Fatalf("new explicit project lost to latest task: %+v", d)
				}
			}
		})
	}
}

func TestGeneralNewTopicDoesNotInheritRecentProject(t *testing.T) {
	for _, text := range []string{"帮我写一个通用检查清单", "写一个通用部署清单", "写一个通用部署检查清单", "Write a general checklist", "Explain deployment", "什么是部署", "请写三条不关联任何工程或已有任务的通用会议检查清单。"} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog(), Recent: []TaskContext{{MessageID: "recent", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}}})
			if err != nil || d.Kind != "default" || d.PrimaryID != "" || d.ContinuationMessageID != "" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestRecordedGeneralResponseRequiresConsistentNoTargetEvidence(t *testing.T) {
	// Preserve the full response from the fifth DWS run. The provider split
	// no-target probability between unrelated and explicitly excluded projects.
	const recorded = `{
		"id": "gen-dec-1791625094-Ubu5DTsUPliNsfblNlkd",
		"model": "typesafe/jev-1.13-20260917",
		"answers": {
			"association": {"type":"choice","choice":"general","probabilities":{"general":1,"known":0,"unknown":0},"confidence":1},
			"e_p0": {"type":"choice","choice":"none","probabilities":{"explicit":0,"none":0.99,"purpose":0,"recent":0.01},"confidence":0.99},
			"e_p1": {"type":"choice","choice":"none","probabilities":{"explicit":0,"none":0.97,"purpose":0,"recent":0.03},"confidence":0.96},
			"mode": {"type":"choice","choice":"general","probabilities":{"clarify":0.03,"continuation":0.02,"general":0.95,"project":0},"confidence":0.93},
			"p0": {"type":"choice","choice":"unrelated","probabilities":{"excluded":0.2,"related":0,"target":0,"unrelated":0.8},"confidence":0.73},
			"p1": {"type":"choice","choice":"unrelated","probabilities":{"excluded":0.13,"related":0,"target":0,"unrelated":0.87},"confidence":0.83},
			"primary": {"type":"choice","choice":"none","probabilities":{"none":1,"p0":0,"p1":0},"confidence":1}
		},
		"usage": {"input_tokens":2713,"output_tokens":306},
		"used_provider": "openrouter",
		"used_pool": "alpha"
	}`
	request := Request{
		Text: "请写三条不关联任何工程或已有任务的通用会议检查清单。",
		Projects: []state.Project{
			{ID: "dt-alpha", Name: "dt-alpha", Description: "Login engineering tests", Aliases: []string{"验证alpha"}},
			{ID: "dt-beta", Name: "dt-beta", Description: "Login engineering tests", Aliases: []string{"验证beta"}},
		},
		Recent: []TaskContext{{MessageID: "latest-joint", RunID: "latest-joint-run", Text: "报告两个项目的规则标记，不要修改文件。", ProjectIDs: []string{"dt-alpha", "dt-beta"}}},
	}
	for _, tc := range []struct {
		name, key, choice, kind string
		probabilities           map[string]float64
	}{
		{name: "complete recorded response", kind: "default"},
		{"excluded is the chosen non-target category", "p0", "excluded", "default", map[string]float64{"excluded": 0.6, "unrelated": 0.4}},
		{"no-target probability meets threshold", "p0", "unrelated", "default", map[string]float64{"unrelated": 0.8, "excluded": 0.1, "related": 0.1}},
		{"topic relation without target evidence", "p0", "unrelated", "default", map[string]float64{"unrelated": 0.65, "excluded": 0.2, "related": 0.15}},
		{"related topic is not execution evidence", "p0", "related", "default", map[string]float64{"related": 0.6, "unrelated": 0.2, "excluded": 0.2}},
		{"actual target contradicts general", "p0", "target", "clarify", map[string]float64{"target": 0.95, "unrelated": 0.05}},
		{"second related topic without execution evidence", "p1", "unrelated", "default", map[string]float64{"unrelated": 0.6, "excluded": 0.2, "related": 0.2}},
		{"explicit evidence contradicts general", "e_p0", "explicit", "clarify", map[string]float64{"explicit": 0.95, "none": 0.05}},
		{"purpose evidence contradicts general", "e_p0", "purpose", "clarify", map[string]float64{"purpose": 0.95, "none": 0.05}},
		{"recent evidence contradicts independent work", "e_p0", "recent", "clarify", map[string]float64{"recent": 0.95, "none": 0.05}},
		{"no affirmative evidence category defers to direct scope checks", "e_p0", "none", "default", map[string]float64{"none": 0.85, "recent": 0.15}},
		{"second project evidence category agrees with non-target scope", "e_p1", "none", "default", map[string]float64{"none": 0.85, "purpose": 0.15}},
		{"general or conversational clarification", "mode", "general", "default", map[string]float64{"general": 0.85, "clarify": 0.15}},
		{"possible project operation", "mode", "general", "clarify", map[string]float64{"general": 0.8, "project": 0.2}},
		{"independent scope resolves broad continuation uncertainty", "mode", "general", "default", map[string]float64{"general": 0.8, "continuation": 0.2}},
		{"general association split does not override no-project need", "association", "general", "default", map[string]float64{"general": 0.85, "unknown": 0.15}},
		{"unknown engineering association", "association", "unknown", "clarify", map[string]float64{"unknown": 0.95, "general": 0.05}},
		{"primary project contradicts general", "primary", "p0", "clarify", map[string]float64{"p0": 0.95, "none": 0.05}},
		{"no project selected despite primary probability split", "primary", "none", "default", map[string]float64{"none": 0.85, "p0": 0.15}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{edit: func(r *jev.Response) {
				// New policy questions have synthetic consistent answers; the saved
				// historical response remains unchanged for its original fields.
				need := jev.Answer{Type: "choice", Choice: "not_required", Probabilities: map[string]float64{"required": 0, "not_required": 1, "uncertain": 0}}
				relation, scopeChange := r.Answers["latest_relation"], r.Answers["scope_change"]
				if err := json.Unmarshal([]byte(recorded), r); err != nil {
					t.Fatal(err)
				}
				r.Answers["project_need"], r.Answers["latest_relation"], r.Answers["scope_change"] = need, relation, scopeChange
				if tc.key != "" {
					a := r.Answers[tc.key]
					a.Choice = tc.choice
					for option := range a.Probabilities {
						a.Probabilities[option] = tc.probabilities[option]
					}
					r.Answers[tc.key] = a
				}
			}}
			d, err := New(f).Decide(context.Background(), request)
			if err != nil || d.Kind != tc.kind || f.calls != 1 || len(d.ProjectIDs) != 0 || d.PrimaryID != "" || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			if !strings.Contains(string(d.Details), "gen-dec-1791625094-Ubu5DTsUPliNsfblNlkd") {
				t.Fatalf("recorded classifier evidence was not retained: %s", d.Details)
			}
		})
	}
}

func TestGreetingDoesNotRequireProjectWhenOnlyConversationModeIsUncertain(t *testing.T) {
	// Production receipt 746bc397af817ae5: the ownership answer was general,
	// but uncertainty between answering and clarifying blocked the greeting.
	for _, projects := range [][]state.Project{nil, catalog()} {
		for _, text := range []string{"hi 能给我干啥不", "你好", "你能帮我做什么？", "What can you help me with?", "帮我写封邮件"} {
			t.Run(fmt.Sprintf("%d/%s", len(projects), text), func(t *testing.T) {
				f := &fakeEvaluator{choices: map[string]string{"association": "general"}, edit: func(r *jev.Response) {
					r.Answers["mode"] = jev.Answer{Type: "choice", Choice: "clarify", Probabilities: map[string]float64{"clarify": 0.54, "continuation": 0.04, "general": 0.42, "project": 0}}
					r.Answers["association"] = jev.Answer{Type: "choice", Choice: "general", Probabilities: map[string]float64{"general": 0.94, "known": 0.01, "unknown": 0.05}}
				}}
				request := Request{Text: text, Projects: projects, Recent: []TaskContext{{MessageID: "previous-greeting", Text: "你好"}}}
				d, err := New(f).Decide(context.Background(), request)
				if err != nil || d.Kind != "default" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || len(d.Details) == 0 {
					t.Fatalf("ordinary conversation was gated on a project: %+v err=%v", d, err)
				}
			})
		}
	}
}

func TestParallelProjectComparisonUsesNeutralPrimary(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "none", "p0": "target", "e_p0": "explicit", "p1": "target", "e_p1": "explicit"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "比较 \"alpha\" 和 \"beta\" 的接口实现", Projects: catalog()})
	if err != nil || d.Kind != "project" || d.PrimaryID != "" || !reflect.DeepEqual(d.ProjectIDs, []string{"alpha", "beta"}) {
		t.Fatalf("parallel comparison decision=%+v err=%v", d, err)
	}
	d, err = New(nil).Decide(context.Background(), Request{Text: "继续", Projects: catalog(), Recent: []TaskContext{{MessageID: "comparison", ProjectIDs: []string{"alpha", "beta"}}}})
	if err != nil || d.Kind != "project" || d.PrimaryID != "" || len(d.ProjectIDs) != 2 || d.ContinuationMessageID != "comparison" {
		t.Fatalf("parallel continuation decision=%+v err=%v", d, err)
	}
}

func TestModelCannotInventPrimaryForParallelComparison(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "explicit", "p1": "target", "e_p1": "explicit"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "比较 \"alpha\" 和 \"beta\" 的接口实现", Projects: catalog()})
	if err != nil || d.Kind != "project" || d.PrimaryID != "" || len(d.ProjectIDs) != 2 || f.calls != 1 {
		t.Fatalf("model invented a primary project: %+v err=%v", d, err)
	}
}

func TestRoutingRequiresIndependentConsistentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]string)
	}{
		{"unknown ownership", func(c map[string]string) { c["association"] = "unknown" }},
		{"no evidence", func(c map[string]string) { c["e_p0"] = "none" }},
		{"invented explicit evidence", func(c map[string]string) { c["e_p0"] = "explicit" }},
		{"ambiguous alternative", func(c map[string]string) { c["p1"] = "related" }},
		{"wrong primary", func(c map[string]string) { c["primary"] = "p1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choices := map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "purpose"}
			tc.edit(choices)
			d, err := New(&fakeEvaluator{choices: choices}).Decide(context.Background(), Request{Text: "检查支付回调的结算计算", Projects: catalog()})
			if err != nil || d.Kind != "clarify" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestLowReliabilityAsksWithoutExecuting(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "primary": "p0", "p0": "target", "e_p0": "purpose"}, edit: func(r *jev.Response) {
		a := r.Answers["p0"]
		a.Probabilities["target"], a.Probabilities["related"] = 0.6, 0.4
		r.Answers["p0"] = a
	}}
	d, err := New(f).Decide(context.Background(), Request{Text: "检查支付回调", Projects: catalog()})
	if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}

func TestInvalidAndFailedClassifierNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake *fakeEvaluator
	}{
		{"provider failure", &fakeEvaluator{err: errors.New("provider unavailable")}},
		{"missing model", &fakeEvaluator{edit: func(r *jev.Response) { r.Model = "" }}},
		{"missing answer", &fakeEvaluator{edit: func(r *jev.Response) { delete(r.Answers, "mode") }}},
		{"unknown choice", &fakeEvaluator{edit: func(r *jev.Response) { a := r.Answers["primary"]; a.Choice = "p99"; r.Answers["primary"] = a }}},
		{"invalid probability", &fakeEvaluator{edit: func(r *jev.Response) {
			a := r.Answers["mode"]
			a.Probabilities[a.Choice] = math.NaN()
			r.Answers["mode"] = a
		}}},
		{"wrong type", &fakeEvaluator{edit: func(r *jev.Response) { a := r.Answers["mode"]; a.Type = "score"; r.Answers["mode"] = a }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New(tc.fake).Decide(context.Background(), Request{Text: "帮我处理这个问题", Projects: catalog()})
			if err == nil || d.Kind != "" || len(d.ProjectIDs) != 0 {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestCatalogAndContextValidationDoesNotSilentlyTruncate(t *testing.T) {
	tooMany := make([]state.Project, MaxProjects+1)
	for i := range tooMany {
		tooMany[i].ID = fmt.Sprintf("p%d", i)
	}
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"empty input", Request{Text: " "}},
		{"oversized input", Request{Text: strings.Repeat("x", MaxRequestBytes+1)}},
		{"duplicate ID", Request{Text: "task", Projects: []state.Project{{ID: "alpha"}, {ID: "alpha"}}}},
		{"project limit", Request{Text: "task", Projects: tooMany}},
		{"descriptor limit", Request{Text: "task", Projects: []state.Project{{ID: "alpha", Description: strings.Repeat("x", MaxStateBytes)}}}},
		{"missing source", Request{Text: "继续", Projects: catalog(), Recent: []TaskContext{{ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}}}},
		{"unknown recent project", Request{Text: "继续", Projects: catalog(), Recent: []TaskContext{{MessageID: "m", ProjectIDs: []string{"unknown"}, PrimaryID: "unknown"}}}},
		{"wrong recent primary", Request{Text: "继续", Projects: catalog(), Recent: []TaskContext{{MessageID: "m", ProjectIDs: []string{"alpha"}, PrimaryID: "beta"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{}
			d, err := New(f).Decide(context.Background(), tc.req)
			if err == nil || d.Kind != "" || f.calls != 0 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestCancellationPrecedesAnyRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := New(nil).Decide(ctx, Request{Text: "Fix alpha", Projects: catalog()})
	if !errors.Is(err, context.Canceled) || d.Kind != "" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
