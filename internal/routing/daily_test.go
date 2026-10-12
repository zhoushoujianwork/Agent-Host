package routing

import (
	"context"
	"reflect"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

// These cases use synthetic tasks and catalog entries, never collected chats.
func TestDailyConversationKeepsMissingContentOutOfProjectSelection(t *testing.T) {
	for _, projects := range [][]state.Project{nil, catalog()} {
		for _, text := range []string{
			"请展开我引用的那段内容。",
			"帮我给同事写一段回复。",
			"请把引用里的“登录回调构建失败”改写成给同事的说明。",
			"Explain what a repository checkout means in this quoted paragraph.",
		} {
			t.Run(text, func(t *testing.T) {
				f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"}}
				d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: projects})
				if err != nil || d.Kind != "default" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || f.calls != 1 {
					t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
				}
			})
		}
	}
}

func TestDailyNaturalFollowupRetainsExactSourceAndProjectSet(t *testing.T) {
	for _, tc := range []struct {
		name, text, kind string
		latest           TaskContext
	}{
		{"information source", "在文档库也查一下。", "project", TaskContext{MessageID: "source-docs", RunID: "run-docs", Text: "Investigate settlement failures in alpha.", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}},
		{"execution restriction", "先列排查步骤，暂时不要改文件。", "project", TaskContext{MessageID: "source-constraint", RunID: "run-constraint", Text: "Investigate login failures in beta.", ProjectIDs: []string{"beta"}, PrimaryID: "beta"}},
		{"joint projects", "把两个实现的差异整理成表格。", "project", TaskContext{MessageID: "source-joint", RunID: "run-joint", Text: "Compare the implementations of alpha and beta.", ProjectIDs: []string{"alpha", "beta"}}},
		{"joint primary", "先只做只读排查。", "project", TaskContext{MessageID: "source-primary", RunID: "run-primary", Text: "Investigate the alpha integration with beta, primarily in alpha.", ProjectIDs: []string{"alpha", "beta"}, PrimaryID: "alpha"}},
		{"default task", "语气再温和一点。", "default", TaskContext{MessageID: "source-default", RunID: "run-default", Text: "Draft a short meeting reminder."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: dailyFollowupChoices()}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog(), Recent: []TaskContext{tc.latest}})
			if err != nil || d.Kind != tc.kind || !reflect.DeepEqual(d.ProjectIDs, tc.latest.ProjectIDs) || d.PrimaryID != tc.latest.PrimaryID ||
				d.ContinuationMessageID != tc.latest.MessageID || d.ContinuationRunID != tc.latest.RunID || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			if len(d.Evidence) != 1 || d.Evidence[0].Kind != "continuation" || d.Evidence[0].MessageID != tc.latest.MessageID || d.Evidence[0].RunID != tc.latest.RunID {
				t.Fatalf("missing exact source evidence: %+v", d.Evidence)
			}
			if got := f.state.(routingState).Request; got != tc.text {
				t.Fatalf("follow-up text was rewritten: %q", got)
			}
		})
	}
}

func TestDailyFollowupRequiresIndependentAgreementAndThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, key, choice, other, want string
		probability                    float64
	}{
		{"threshold accepted", "latest_relation", "followup", "uncertain", "project", 0.9},
		{"uncertain continuity", "latest_relation", "uncertain", "followup", "clarify", 1},
		{"new topic contradicts continuation", "latest_relation", "independent", "followup", "clarify", 1},
		{"continuity below threshold", "latest_relation", "followup", "uncertain", "clarify", 0.89},
		{"scope threshold accepted", "scope_change", "preserve", "uncertain", "project", 0.9},
		{"scope below threshold", "scope_change", "preserve", "uncertain", "clarify", 0.89},
		{"scope changed", "scope_change", "change", "preserve", "clarify", 1},
		{"scope uncertain", "scope_change", "uncertain", "preserve", "clarify", 1},
		{"mode uncertainty does not erase scope", "mode", "continuation", "general", "project", 0.66},
		{"ownership uncertainty does not erase scope", "association", "known", "general", "project", 0.65},
		{"unknown ownership", "association", "unknown", "known", "clarify", 1},
		{"unknown ownership category below threshold", "association", "unknown", "known", "clarify", 0.85},
		{"external lookup retains prior subject", "association", "general", "known", "project", 1},
		{"no local tool needed retains prior subject", "project_need", "not_required", "required", "project", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: dailyFollowupChoices(), edit: func(r *jev.Response) {
				dailySetAnswer(r, tc.key, tc.choice, tc.other, tc.probability)
			}}
			d, err := New(f).Decide(context.Background(), Request{Text: "从设计文档里找一下原因。", Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
			if err != nil || d.Kind != tc.want {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
			if tc.want == "clarify" && (len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "") {
				t.Fatalf("unreliable continuation retained execution context: %+v", d)
			}
		})
	}
}

func TestDailyDefaultRequiresIndependentScopeAndContinuityChecks(t *testing.T) {
	for _, tc := range []struct {
		name, key, choice, other, want string
		probability                    float64
	}{
		{"scope threshold accepted", "project_need", "not_required", "uncertain", "default", 0.9},
		{"scope uncertain", "project_need", "uncertain", "not_required", "clarify", 1},
		{"scope required", "project_need", "required", "not_required", "clarify", 1},
		{"scope below threshold", "project_need", "not_required", "uncertain", "clarify", 0.89},
		{"independence threshold accepted", "latest_relation", "independent", "uncertain", "default", 0.9},
		{"independence below threshold", "latest_relation", "independent", "uncertain", "clarify", 0.89},
		{"uncertain relation", "latest_relation", "uncertain", "independent", "clarify", 1},
		{"follow-up contradicts general", "latest_relation", "followup", "independent", "clarify", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "latest_relation": "independent"}, edit: func(r *jev.Response) {
				dailySetAnswer(r, tc.key, tc.choice, tc.other, tc.probability)
			}}
			d, err := New(f).Decide(context.Background(), Request{Text: "帮我整理周末徒步的行李清单。", Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
			if err != nil || d.Kind != tc.want || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestDailyDefaultOwnershipWordingDoesNotOverrideStrongScopeEvidence(t *testing.T) {
	for _, projects := range [][]state.Project{nil, catalog()} {
		for _, tc := range []struct {
			name string
			edit func(*jev.Response)
			want string
		}{
			{"ordinary details leave ownership probability split", nil, "default"},
			{"project need still meets its own threshold", func(r *jev.Response) {
				dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.9)
			}, "default"},
			{"project need below threshold", func(r *jev.Response) {
				dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.89)
			}, "clarify"},
			{"project needed contradicts ownership wording", func(r *jev.Response) {
				dailySetAnswer(r, "project_need", "required", "not_required", 0.99)
			}, "clarify"},
			{"unknown ownership category remains forbidden", func(r *jev.Response) {
				dailySetAnswer(r, "association", "unknown", "general", 0.75)
			}, "clarify"},
			{"known ownership category remains forbidden", func(r *jev.Response) {
				dailySetAnswer(r, "association", "known", "general", 0.75)
			}, "clarify"},
			{"possible project operation remains forbidden", func(r *jev.Response) {
				dailySetAnswer(r, "mode", "general", "project", 0.8)
			}, "clarify"},
			{"possible continuation remains forbidden", func(r *jev.Response) {
				dailySetAnswer(r, "mode", "general", "continuation", 0.8)
			}, "clarify"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"}, edit: func(r *jev.Response) {
					dailySetAnswer(r, "association", "general", "unknown", 0.75)
					dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.99)
					a := r.Answers["mode"]
					a.Probabilities = map[string]float64{"general": 0.67, "clarify": 0.32, "continuation": 0.01, "project": 0}
					r.Answers["mode"] = a
					if tc.edit != nil {
						tc.edit(r)
					}
				}}
				d, err := New(f).Decide(context.Background(), Request{Text: "帮我起草给同事的回复。", Projects: projects})
				if err != nil || d.Kind != tc.want || len(d.ProjectIDs) != 0 || d.PrimaryID != "" || d.ContinuationMessageID != "" {
					t.Fatalf("decision=%+v err=%v", d, err)
				}
			})
		}
	}
}

func TestDailyExplicitDifferentTargetDoesNotInheritPreviousProject(t *testing.T) {
	f := &fakeEvaluator{choices: dailyFollowupChoices()}
	d, err := New(f).Decide(context.Background(), Request{Text: "检查 beta 的测试。", Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
	if err != nil || d.Kind != "project" || !reflect.DeepEqual(d.ProjectIDs, []string{"beta"}) || d.PrimaryID != "beta" || d.ContinuationMessageID != "" || f.calls != 0 {
		t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
	}
}

func TestDailyFollowupCannotInventSourceOrReachPastLatestTask(t *testing.T) {
	for _, tc := range []struct {
		name    string
		recent  []TaskContext
		wantErr bool
	}{
		{"no recent task", nil, false},
		{"source missing", []TaskContext{{Text: "Inspect settlement failures.", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}}, true},
		{"older task is not latest", []TaskContext{{MessageID: "new-general", Text: "Draft a picnic invitation."}, dailyLatest()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choices := dailyFollowupChoices()
			choices["latest_relation"] = "uncertain"
			f := &fakeEvaluator{choices: choices}
			d, err := New(f).Decide(context.Background(), Request{Text: "也从设计文档里查一下。", Projects: catalog(), Recent: tc.recent})
			if tc.wantErr {
				if err == nil || f.calls != 0 {
					t.Fatalf("source validation should precede provider: decision=%+v calls=%d err=%v", d, f.calls, err)
				}
				return
			}
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestDailyFollowupRejectsConflictingScopeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, text, key, choice string
		latest                  TaskContext
	}{
		{"missing source text", "从文档里找原因。", "", "", TaskContext{MessageID: "no-text", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}},
		{"new target category", "从文档里找原因。", "p1", "target", dailyLatest()},
		{"new target purpose evidence", "从文档里找原因。", "e_p1", "purpose", dailyLatest()},
		{"new target recent evidence", "从文档里找原因。", "e_p1", "recent", dailyLatest()},
		{"new target explicit evidence", "从文档里找原因。", "e_p1", "explicit", dailyLatest()},
		{"prior target excluded", "从文档里找原因。", "p0", "excluded", dailyLatest()},
		{"conflicting primary", "从文档里找原因。", "primary", "p1", dailyLatest()},
		{"explicit exclusion", "不要操作 alpha，只看相关设计文档。", "", "", dailyLatest()},
		{"new explicit member despite continuation claim", "参考 beta 的设计文档排查原因。", "", "", dailyLatest()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choices := dailyFollowupChoices()
			if tc.key != "" {
				choices[tc.key] = tc.choice
			}
			f := &fakeEvaluator{choices: choices}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog(), Recent: []TaskContext{tc.latest}})
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("conflicting evidence inherited scope: decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestDailyNonTargetTopicRelevanceDoesNotGrantProjectAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*jev.Response)
		want string
	}{
		{"related topic without execution evidence", func(r *jev.Response) {
			a := r.Answers["p0"]
			a.Choice = "related"
			a.Probabilities = map[string]float64{"related": 0.7, "unrelated": 0.2, "excluded": 0.05, "target": 0.05}
			r.Answers["p0"] = a
		}, "default"},
		{"weak primary none does not grant a primary", func(r *jev.Response) {
			a := r.Answers["primary"]
			a.Probabilities = map[string]float64{"none": 0.55, "p0": 0.4, "p1": 0.05}
			r.Answers["primary"] = a
		}, "default"},
		{"non-target mass below threshold", func(r *jev.Response) {
			a := r.Answers["p0"]
			a.Choice = "related"
			a.Probabilities = map[string]float64{"related": 0.6, "unrelated": 0.2, "excluded": 0.05, "target": 0.15}
			r.Answers["p0"] = a
		}, "clarify"},
		{"affirmative evidence contradicts ordinary scope", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "purpose", "none", 1)
		}, "clarify"},
		{"none category agrees with direct scope checks", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "none", "purpose", 0.89)
		}, "default"},
		{"target category remains forbidden", func(r *jev.Response) {
			dailySetAnswer(r, "p0", "target", "unrelated", 1)
		}, "clarify"},
		{"selected primary remains forbidden", func(r *jev.Response) {
			dailySetAnswer(r, "primary", "p0", "none", 1)
		}, "clarify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"}, edit: tc.edit}
			d, err := New(f).Decide(context.Background(), Request{Text: "解释引用中的支付结算概念。", Projects: catalog()})
			if err != nil || d.Kind != tc.want || len(d.ProjectIDs) != 0 || d.PrimaryID != "" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestDailyProjectSelectionStillRejectsRelatedAlternative(t *testing.T) {
	f := &fakeEvaluator{choices: map[string]string{"mode": "project", "association": "known", "project_need": "required", "primary": "p0", "p0": "target", "e_p0": "purpose", "p1": "related"}}
	d, err := New(f).Decide(context.Background(), Request{Text: "排查支付结算的计算错误。", Projects: catalog()})
	if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 {
		t.Fatalf("related alternative became executable: decision=%+v err=%v", d, err)
	}
}

func TestDailyDefaultModeUncertaintyRequiresReliableIndependentContext(t *testing.T) {
	for _, tc := range []struct {
		name, relation, mode, want string
		recent                     bool
		probability                float64
		projectProbability         float64
	}{
		{"reliable independent scope", "independent", "general", "default", true, 1, 0.01},
		{"independence at threshold", "independent", "general", "default", true, 0.9, 0.01},
		{"independence below threshold", "independent", "general", "clarify", true, 0.89, 0.01},
		{"follow-up is not independent", "followup", "general", "clarify", true, 1, 0.01},
		{"uncertain relation", "uncertain", "general", "clarify", true, 1, 0.01},
		{"no recent source for independent scope", "", "general", "clarify", false, 0, 0.01},
		{"true project probability above boundary", "independent", "general", "clarify", true, 1, 0.11},
		{"project category contradicts default", "independent", "project", "clarify", true, 1, 0.6},
		{"continuation category contradicts default", "independent", "continuation", "clarify", true, 1, 0.01},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "latest_relation": "independent"}, edit: func(r *jev.Response) {
				a := r.Answers["mode"]
				a.Choice = tc.mode
				a.Probabilities = map[string]float64{"general": 0.81, "clarify": 0.08, "continuation": 0.1, "project": 0.01}
				if tc.mode == "project" {
					a.Probabilities = map[string]float64{"general": 0.4, "clarify": 0, "continuation": 0, "project": 0.6}
				} else if tc.mode == "continuation" {
					a.Probabilities = map[string]float64{"general": 0.4, "clarify": 0, "continuation": 0.59, "project": 0.01}
				} else if tc.projectProbability > 0.1 {
					a.Probabilities = map[string]float64{"general": 0.71, "clarify": 0.08, "continuation": 0.1, "project": tc.projectProbability}
				}
				r.Answers["mode"] = a
				if tc.recent {
					other := "uncertain"
					if tc.relation == "uncertain" {
						other = "independent"
					}
					dailySetAnswer(r, "latest_relation", tc.relation, other, tc.probability)
				}
			}}
			req := Request{Text: "帮我读取引用后拟一份回复。", Projects: catalog()}
			if tc.recent {
				req.Recent = []TaskContext{dailyLatest()}
			}
			d, err := New(f).Decide(context.Background(), req)
			if err != nil || d.Kind != tc.want || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestDailyCommunicationAndEngineeringActionsRespectScopeDisagreement(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		choices          map[string]string
	}{
		{"describe a quoted symptom", "把引用中的登录错误总结成问题描述。", "default", map[string]string{"mode": "general", "association": "general", "project_need": "not_required"}},
		{"operate an unowned system", "定位登录错误并修改错误处理。", "clarify", map[string]string{"mode": "clarify", "association": "unknown", "project_need": "required", "p1": "related"}},
		{"general label cannot override needed workspace", "定位登录错误并修改错误处理。", "clarify", map[string]string{"mode": "general", "association": "general", "project_need": "required"}},
		{"general label cannot override unknown owner", "定位登录错误并修改错误处理。", "clarify", map[string]string{"mode": "general", "association": "unknown", "project_need": "not_required"}},
		{"general label cannot override target", "定位登录错误并修改错误处理。", "clarify", map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "p1": "target"}},
		{"general label cannot override affirmative evidence", "定位登录错误并修改错误处理。", "clarify", map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "e_p1": "purpose"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: tc.choices}
			d, err := New(f).Decide(context.Background(), Request{Text: tc.text, Projects: catalog()})
			if err != nil || d.Kind != tc.want || len(d.ProjectIDs) != 0 || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestDailyUnknownProjectCannotUsePartialKnownMatchOrRecentContext(t *testing.T) {
	for _, text := range []string{
		"Integrate alpha with repository gamma.",
		"Compare alpha with https://github.com/example/unregistered-engine.",
	} {
		t.Run(text, func(t *testing.T) {
			choices := dailyFollowupChoices()
			choices["association"] = "unknown"
			choices["p0"], choices["e_p0"] = "target", "explicit"
			f := &fakeEvaluator{choices: choices}
			d, err := New(f).Decide(context.Background(), Request{Text: text, Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
		})
	}
}

func TestDailyNewIndependentAnswersAreMandatory(t *testing.T) {
	for _, missing := range []string{"project_need", "latest_relation", "scope_change"} {
		t.Run(missing, func(t *testing.T) {
			f := &fakeEvaluator{choices: dailyFollowupChoices(), edit: func(r *jev.Response) { delete(r.Answers, missing) }}
			d, err := New(f).Decide(context.Background(), Request{Text: "从文档里找找。", Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
			if err == nil || d.Kind != "" || len(d.ProjectIDs) != 0 {
				t.Fatalf("incomplete classifier response accepted: decision=%+v err=%v", d, err)
			}
		})
	}
}

func dailyFollowupChoices() map[string]string {
	return map[string]string{"mode": "continuation", "association": "known", "project_need": "required", "latest_relation": "followup", "scope_change": "preserve"}
}

func dailyLatest() TaskContext {
	return TaskContext{MessageID: "latest-source", RunID: "latest-run", Text: "Investigate settlement failures in alpha.", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}
}

func dailySetAnswer(r *jev.Response, key, choice, other string, probability float64) {
	a := r.Answers[key]
	a.Choice = choice
	for option := range a.Probabilities {
		a.Probabilities[option] = 0
	}
	a.Probabilities[choice], a.Probabilities[other] = probability, 1-probability
	r.Answers[key] = a
}
