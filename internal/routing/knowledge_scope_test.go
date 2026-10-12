package routing

import (
	"context"
	"reflect"
	"testing"

	"agent-host/internal/state"
)

func TestKnowledgeAndOperationalGuidanceKeepTheirActualScope(t *testing.T) {
	projects := []state.Project{
		{ID: "northstar", Name: "Northstar Gateway", Description: "Object storage gateway signing API and bucket requirements."},
		{ID: "cloudctl", Name: "Cloud Inventory CLI", Description: "A CLI for querying public cloud product catalogs and regions."},
	}
	for _, tc := range []struct {
		name, text, want string
		emptyCatalog     bool
		choices          map[string]string
		wantIDs          []string
	}{
		{
			name: "named project read-only API requirements", text: "请解释 northstar 的签名 API 和桶命名要求。", want: "project", wantIDs: []string{"northstar"},
			choices: map[string]string{"mode": "project", "association": "known", "project_need": "required", "p0": "target", "e_p0": "explicit", "primary": "p0"},
		},
		{
			name: "named project read-only configuration", text: "请解释 northstar 的请求签名配置。", want: "project", wantIDs: []string{"northstar"},
			choices: map[string]string{"mode": "project", "association": "known", "project_need": "required", "p0": "target", "e_p0": "explicit", "primary": "p0"},
		},
		{
			name: "unregistered project API needs ownership", text: "请解释 northstar 的签名 API 和桶命名要求。", want: "clarify", emptyCatalog: true,
			choices: map[string]string{"mode": "clarify", "association": "unknown", "project_need": "required"},
		},
		{
			name: "public catalog lookup does not target its CLI repo", text: "请解释 cloudctl 查询到的公开云产品区域和容量列表。", want: "default",
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required", "p1": "related"},
		},
		{
			name: "public product inventory needs no catalog", text: "查询公开云产品支持哪些区域和默认规格。", want: "default", emptyCatalog: true,
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"},
		},
		{
			name: "cluster operation hint with no registered project", text: "你可以先检查 shell alias，切到目标集群执行排查。", want: "clarify", emptyCatalog: true,
			choices: map[string]string{"mode": "clarify", "association": "unknown", "project_need": "required"},
		},
		{
			name: "cluster operation hint cannot pick an unrelated catalog project", text: "你可以先检查 shell alias，切到目标集群执行排查。", want: "clarify",
			choices: map[string]string{"mode": "clarify", "association": "unknown", "project_need": "required"},
		},
		{
			name: "relaying cluster guidance is communication", text: "帮我转告值班同事：可以先检查 shell alias，切到目标集群执行排查。", want: "default", emptyCatalog: true,
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"},
		},
		{
			name: "explaining a quoted cluster instruction is communication", text: "解释这段引用的意思：先检查 shell alias，切到目标集群执行排查。", want: "default",
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "not_required"},
		},
		{
			name: "general task label cannot override cluster environment need", text: "你可以先检查 shell alias，切到目标集群执行排查。", want: "clarify", emptyCatalog: true,
			choices: map[string]string{"mode": "general", "association": "general", "project_need": "required"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Text: tc.text}
			if !tc.emptyCatalog {
				req.Projects = projects
			}
			f := &fakeEvaluator{choices: tc.choices}
			d, err := New(f).Decide(context.Background(), req)
			if err != nil || d.Kind != tc.want || !reflect.DeepEqual(d.ProjectIDs, tc.wantIDs) || f.calls != 1 {
				t.Fatalf("decision=%+v calls=%d err=%v", d, f.calls, err)
			}
			if len(tc.wantIDs) == 1 && d.PrimaryID != tc.wantIDs[0] || len(tc.wantIDs) == 0 && d.PrimaryID != "" {
				t.Fatalf("unexpected primary scope: %+v", d)
			}
		})
	}
}
