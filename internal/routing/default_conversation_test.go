package routing

import (
	"context"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

func defaultConversationAnswers(r *jev.Response) {
	// Project ownership is clear even when conversational continuity is not.
	dailySetAnswer(r, "mode", "general", "continuation", 0.6)
	dailySetAnswer(r, "association", "general", "known", 0.8)
	dailySetAnswer(r, "latest_relation", "followup", "uncertain", 0.8)
	dailySetAnswer(r, "scope_change", "preserve", "uncertain", 0.7)
}

func TestDefaultConversationDoesNotRequireProjectToResolveFollowup(t *testing.T) {
	for _, projects := range [][]state.Project{nil, catalog()} {
		for _, mode := range []string{"general", "continuation", "clarify"} {
			t.Run(mode, func(t *testing.T) {
				f := &fakeEvaluator{choices: map[string]string{"association": "general", "project_need": "not_required"}, edit: func(r *jev.Response) {
					defaultConversationAnswers(r)
					other := "continuation"
					if mode == other {
						other = "general"
					}
					dailySetAnswer(r, "mode", mode, other, 0.6)
				}}
				req := Request{Text: "Make that greeting shorter.", Projects: projects,
					Recent: []TaskContext{{MessageID: "draft-message", RunID: "draft-run", Text: "Draft a greeting for an event."}}}
				d, err := New(f).Decide(context.Background(), req)
				if err != nil || d.Kind != "default" || len(d.ProjectIDs) != 0 || d.PrimaryID != "" || f.calls != 1 {
					t.Fatalf("ordinary follow-up asked for a project: decision=%+v calls=%d err=%v", d, f.calls, err)
				}
				if d.ContinuationMessageID != "" || d.ContinuationRunID != "" || len(d.Evidence) != 1 || d.Evidence[0].Kind != "general" {
					t.Fatalf("uncertain continuity invented source evidence: %+v", d)
				}
			})
		}
	}
}

func TestDefaultConversationStillRequiresIndependentNoProjectEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*jev.Response)
		latest TaskContext
	}{
		{name: "unknown project", edit: func(r *jev.Response) { dailySetAnswer(r, "association", "unknown", "general", 1) }},
		{name: "known project", edit: func(r *jev.Response) { dailySetAnswer(r, "association", "known", "general", 1) }},
		{name: "project required", edit: func(r *jev.Response) { dailySetAnswer(r, "project_need", "required", "not_required", 1) }},
		{name: "project need uncertain", edit: func(r *jev.Response) { dailySetAnswer(r, "project_need", "uncertain", "not_required", 1) }},
		{name: "no-project evidence below threshold", edit: func(r *jev.Response) { dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.89) }},
		{name: "possible project action", edit: func(r *jev.Response) { dailySetAnswer(r, "mode", "continuation", "project", 0.8) }},
		{name: "selected primary", edit: func(r *jev.Response) { dailySetAnswer(r, "primary", "p0", "none", 1) }},
		{name: "catalog target", edit: func(r *jev.Response) { dailySetAnswer(r, "p0", "target", "unrelated", 1) }},
		{name: "catalog purpose evidence", edit: func(r *jev.Response) { dailySetAnswer(r, "e_p0", "purpose", "none", 1) }},
		{name: "recent project still needs continuity", latest: dailyLatest()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{"association": "general", "project_need": "not_required"}, edit: func(r *jev.Response) {
				defaultConversationAnswers(r)
				if tc.edit != nil {
					tc.edit(r)
				}
			}}
			latest := tc.latest
			if latest.MessageID == "" {
				latest = TaskContext{MessageID: "ordinary-message", Text: "Draft an event greeting."}
			}
			d, err := New(f).Decide(context.Background(), Request{Text: "Revise the previous wording.", Projects: catalog(), Recent: []TaskContext{latest}})
			if err != nil || d.Kind != "clarify" || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
				t.Fatalf("unresolved project evidence bypassed clarification: decision=%+v err=%v", d, err)
			}
		})
	}
}

func TestOrdinaryConversationAggregateBoundaryKeepsPointNineThreshold(t *testing.T) {
	for _, previous := range []struct {
		name   string
		latest TaskContext
	}{
		{"after project task", dailyLatest()},
		{"after default task", TaskContext{MessageID: "general-task", Text: "Summarize a supplied table."}},
	} {
		for _, tc := range []struct {
			name, want string
			edit       func(*jev.Response)
		}{
			{name: "mode aggregate at decimal threshold", want: "default", edit: func(r *jev.Response) {
				a := r.Answers["mode"]
				a.Probabilities = map[string]float64{"general": 0.83, "continuation": 0.07, "clarify": 0, "project": 0.1}
				r.Answers["mode"] = a
			}},
			{name: "mode aggregate below threshold", want: "clarify", edit: func(r *jev.Response) {
				a := r.Answers["mode"]
				a.Probabilities = map[string]float64{"general": 0.83, "continuation": 0.0699, "clarify": 0, "project": 0.1001}
				r.Answers["mode"] = a
			}},
			{name: "non-target aggregate at decimal threshold", want: "default", edit: func(r *jev.Response) {
				a := r.Answers["p0"]
				a.Choice = "related"
				a.Probabilities = map[string]float64{"related": 0.83, "unrelated": 0.07, "excluded": 0, "target": 0.1}
				r.Answers["p0"] = a
			}},
			{name: "non-target aggregate below threshold", want: "clarify", edit: func(r *jev.Response) {
				a := r.Answers["p0"]
				a.Choice = "related"
				a.Probabilities = map[string]float64{"related": 0.83, "unrelated": 0.0699, "excluded": 0, "target": 0.1001}
				r.Answers["p0"] = a
			}},
			{name: "project need at threshold", want: "default", edit: func(r *jev.Response) {
				dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.9)
			}},
			{name: "project need below threshold", want: "clarify", edit: func(r *jev.Response) {
				dailySetAnswer(r, "project_need", "not_required", "uncertain", 0.8999)
			}},
		} {
			t.Run(previous.name+"/"+tc.name, func(t *testing.T) {
				f := &fakeEvaluator{choices: map[string]string{
					"mode": "general", "association": "general", "project_need": "not_required", "latest_relation": "independent", "scope_change": "change",
				}, edit: tc.edit}
				d, err := New(f).Decide(context.Background(), Request{Text: "Summarize this supplied table without querying any environment: category,count; sample,7.", Projects: catalog(), Recent: []TaskContext{previous.latest}})
				if err != nil || f.calls != 1 || d.Kind != tc.want || len(d.ProjectIDs) != 0 || d.ContinuationMessageID != "" || d.ContinuationRunID != "" {
					t.Fatalf("aggregate boundary changed routing policy: %+v calls=%d err=%v", d, f.calls, err)
				}
			})
		}
	}
}

func TestAggregateRoundingDoesNotRelaxIndependentProjectScopeEvidence(t *testing.T) {
	for _, tc := range []struct {
		probability float64
		want        string
	}{{0.9, "default"}, {0.8999, "clarify"}} {
		f := &fakeEvaluator{choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "latest_relation": "independent", "scope_change": "change"}, edit: func(r *jev.Response) {
			dailySetAnswer(r, "latest_relation", "independent", "uncertain", tc.probability)
		}}
		d, err := New(f).Decide(context.Background(), Request{Text: "Explain only this independent sample: queue=3, workers=0.", Projects: catalog(), Recent: []TaskContext{dailyLatest()}})
		if err != nil || f.calls != 1 || d.Kind != tc.want || d.ContinuationMessageID != "" || len(d.ProjectIDs) != 0 {
			t.Fatalf("independent scope threshold %.4f changed: %+v err=%v", tc.probability, d, err)
		}
	}
}
