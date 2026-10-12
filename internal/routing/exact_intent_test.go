package routing

import (
	"context"
	"testing"
)

func TestExactReferencesNeedProjectExecutionSyntax(t *testing.T) {
	for _, text := range []string{
		"把 alpha 这个词翻译成中文",
		"统计字符串 alpha beta 中英文字母的数量",
		"Translate alpha to Chinese",
		"Count letters in alpha and beta",
		"Read the word alpha aloud",
		"Read alpha aloud",
		"alpha read aloud",
		"Compare alpha and beta as Greek letters",
		"Fix alpha spelling",
		"Fix alpha. Then translate beta to Chinese",
		"alpha: count letters in beta",
		"Fix the spelling of the word alpha",
		"请转告同事 alpha 已经准备好了",
		"Write a poem about alpha and beta",
		"alpha 这个名字是什么意思",
	} {
		t.Run(text, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general"}}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog()})
			if err != nil || f.calls != 1 || d.Kind != "default" || len(d.ProjectIDs) != 0 {
				t.Fatalf("content reference bypassed semantic routing: decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestProjectExecutionSyntaxRetainsDirectSelections(t *testing.T) {
	for _, text := range []string{
		"alpha",
		"alpha and beta",
		"alpha、beta",
		"Billing API",
		"Please fix alpha",
		"Fix project alpha.",
		"Fix (alpha.)",
		"Check alpha startup",
		"Check alpha again.",
		"Compare alpha and beta",
		"同时修改 beta 和 alpha 的接口",
		"以 alpha 为主，联调 beta 的接口",
		"在 alpha 读取 README",
		"In alpha, run tests",
		"alpha 读取 README，不要修改文件",
		"alpha：报告本原生会话的临时标记",
		"继续 alpha 的测试",
	} {
		t.Run(text, func(t *testing.T) {
			decision, ok, err := ExactDecision(Request{Text: text, Projects: catalog()})
			if err != nil || !ok || decision.Kind != "project" {
				t.Fatalf("explicit engineering task required a provider: decision=%+v ok=%v err=%v", decision, ok, err)
			}
		})
	}
}

func TestContinuedComparisonAndIntegrationRetainExactProjectSet(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"Continue comparing alpha and beta", true},
		{"Continue integrating alpha with beta", true},
		{"Continue comparing alpha and beta as words", false},
		{"Continue integrating alpha and beta into this poem", false},
		{"Continue comparing alpha and gamma", false},
		{"Continue comparing alpha and beta at archive.example.invalid", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			d, ok, err := ExactDecision(Request{Text: tc.text, Projects: catalog()})
			if err != nil || ok != tc.ok {
				t.Fatalf("decision=%+v ok=%v want=%v err=%v", d, ok, tc.ok, err)
			}
			if ok && (d.Kind != "project" || len(d.ProjectIDs) != 2 || d.ProjectIDs[0] != "alpha" || d.ProjectIDs[1] != "beta" || d.PrimaryID != "") {
				t.Fatalf("continued joint task lost its full neutral project set: %+v", d)
			}
		})
	}
}

func TestColloquialEngineeringLookupRequiresLeadingEngineeringObject(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"看下 alpha 校验 API 的参数", true},
		{"看一下 alpha 健康接口的响应格式", true},
		{"查下 alpha 审计 API 的配置", true},
		{"查一下 alpha 批量接口的错误码", true},
		{"查看 alpha 双向校验 API 的参数", true},
		{"Check alpha API requirements", true},
		{"把 alpha 这个 API 名称翻译成中文", false},
		{"看下 alpha 这段文字应该如何翻译为 API", false},
		{"看下 alpha 这段话的字数以及 API 字样", false},
		{"看下 alpha 校验 API 是否使用 edge.example.invalid", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			d, ok, err := ExactDecision(Request{Text: tc.text, Projects: catalog()})
			if err != nil || ok != tc.ok {
				t.Fatalf("decision=%+v ok=%v want=%v err=%v", d, ok, tc.ok, err)
			}
			if ok && (d.Kind != "project" || len(d.ProjectIDs) != 1 || d.PrimaryID != "alpha") {
				t.Fatalf("engineering lookup did not retain its explicit project: %+v", d)
			}
		})
	}
}
