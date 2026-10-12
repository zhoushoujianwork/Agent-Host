package routing

import (
	"context"
	"reflect"
	"testing"

	"agent-host/internal/decision/jev"
)

func TestPurposeSelectionSeparatesNonTargetsFromAmbiguousAlternatives(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*jev.Response)
		want string
	}{
		{"unrelated and excluded split", nil, "project"},
		{"excluded and unrelated split", func(r *jev.Response) {
			a := r.Answers["p0"]
			a.Choice = "excluded"
			a.Probabilities = map[string]float64{"unrelated": 0.22, "excluded": 0.77, "related": 0.01, "target": 0}
			r.Answers["p0"] = a
		}, "project"},
		{"non-target mass at threshold", func(r *jev.Response) {
			a := r.Answers["p0"]
			a.Probabilities = map[string]float64{"unrelated": 0.8, "excluded": 0.1, "related": 0.1, "target": 0}
			r.Answers["p0"] = a
		}, "project"},
		{"non-target evidence category is sufficient", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "none", "purpose", 0.75)
		}, "project"},
		{"related probability cannot repair weak non-target mass", func(r *jev.Response) {
			a := r.Answers["p0"]
			a.Probabilities = map[string]float64{"unrelated": 0.77, "excluded": 0.12, "related": 0.11, "target": 0}
			r.Answers["p0"] = a
		}, "clarify"},
		{"related category remains ambiguous", func(r *jev.Response) {
			dailySetAnswer(r, "p0", "related", "unrelated", 0.95)
		}, "clarify"},
		{"purpose evidence on non-target contradicts exclusion", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "purpose", "none", 0.95)
		}, "clarify"},
		{"explicit evidence on non-target contradicts exclusion", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "explicit", "none", 0.95)
		}, "clarify"},
		{"recent evidence on non-target contradicts exclusion", func(r *jev.Response) {
			dailySetAnswer(r, "e_p0", "recent", "none", 0.95)
		}, "clarify"},
		{"selected target still needs threshold", func(r *jev.Response) {
			dailySetAnswer(r, "p1", "target", "related", 0.89)
		}, "clarify"},
		{"selected purpose still needs threshold", func(r *jev.Response) {
			dailySetAnswer(r, "e_p1", "purpose", "none", 0.89)
		}, "clarify"},
		{"known ownership still needs threshold", func(r *jev.Response) {
			dailySetAnswer(r, "association", "known", "unknown", 0.89)
		}, "clarify"},
		{"project need still needs threshold", func(r *jev.Response) {
			dailySetAnswer(r, "project_need", "required", "uncertain", 0.89)
		}, "clarify"},
		{"primary still needs threshold", func(r *jev.Response) {
			dailySetAnswer(r, "primary", "p1", "none", 0.89)
		}, "clarify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEvaluator{choices: map[string]string{
				"mode": "project", "association": "known", "project_need": "required", "primary": "p1", "p1": "target", "e_p1": "purpose",
			}, edit: func(r *jev.Response) {
				a := r.Answers["p0"]
				a.Probabilities = map[string]float64{"unrelated": 0.77, "excluded": 0.22, "related": 0.01, "target": 0}
				r.Answers["p0"] = a
				if tc.edit != nil {
					tc.edit(r)
				}
			}}
			d, err := New(f).Decide(context.Background(), Request{Text: "Fix the authentication and sign-in service implementation in its codebase.", Projects: catalog()})
			if err != nil || d.Kind != tc.want || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			if tc.want == "project" {
				if !reflect.DeepEqual(d.ProjectIDs, []string{"beta"}) || d.PrimaryID != "beta" || len(d.Evidence) != 1 || d.Evidence[0].Kind != "purpose" || d.Evidence[0].ProjectID != "beta" {
					t.Fatalf("purpose match lost or expanded its target: %+v", d)
				}
			} else if len(d.ProjectIDs) != 0 || d.PrimaryID != "" {
				t.Fatalf("ambiguous purpose acquired an execution scope: %+v", d)
			}
		})
	}
}
