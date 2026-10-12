package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/annotation"
	"agent-host/internal/event"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestObjectRecallReachesNativeContextAndDurableAudit(t *testing.T) {
	h, runner := testHost(t)
	sources := map[string]bool{"订单服务的数据库使用 PostgreSQL。": true, "账单服务的数据库使用 MySQL。": true, "密钥每九十天更换一次。": true}
	// Test doubles delimit native execution and admission only; the write index,
	// query analysis, retrieval, assembly and event journal are real components.
	h.Decider = deciderFunc(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		if sources[input] {
			return memory.Decision{Kind: "project_fact", Store: true, Details: json.RawMessage(`{"test_only":true}`)}, nil
		}
		return memory.Decision{Kind: "none"}, nil
	})
	for _, text := range []string{"订单服务的数据库使用 PostgreSQL。", "账单服务的数据库使用 MySQL。", "密钥每九十天更换一次。"} {
		runOK(t, h, newSession(t, h), text)
	}
	for _, tc := range []struct{ query, want, excluded string }{
		{"订单服务 数据库", "订单服务的数据库使用 PostgreSQL。", "账单服务的数据库使用 MySQL。"},
		{"凭据的轮换周期", "密钥每九十天更换一次。", "订单服务的数据库使用 PostgreSQL。"},
	} {
		r := runOK(t, h, newSession(t, h), tc.query)
		if len(r.Context) != 1 || r.Context[0].Text != tc.want {
			t.Fatalf("wrong native context: %+v", r.Context)
		}
		prompt := runner.requests[len(runner.requests)-1].Prompt
		if !strings.Contains(prompt, tc.want) || strings.Contains(prompt, tc.excluded) {
			t.Fatalf("wrong prompt memory: %s", prompt)
		}
		persisted, err := h.Store.Run(t.Context(), r.ID)
		if err != nil {
			t.Fatal(err)
		}
		analyzed, evaluated, dispatched := int64(0), int64(0), int64(0)
		for _, e := range persisted.Events {
			switch e.Type {
			case "memory.query.analyzed":
				analyzed = e.Seq
				if e.Data["annotation_version"] != annotation.Version {
					t.Fatal("missing parser version")
				}
			case "memory.retrieval.evaluated":
				evaluated = e.Seq
				if e.Data["decisions"] == nil {
					t.Fatal("missing applicability audit")
				}
			case "agent.dispatched":
				dispatched = e.Seq
			}
		}
		if analyzed == 0 || evaluated <= analyzed || dispatched <= evaluated {
			t.Fatalf("analysis/selection did not precede native dispatch: %d %d %d", analyzed, evaluated, dispatched)
		}
	}
}

func TestAnnotationEventFailureStopsNativeDispatch(t *testing.T) {
	h, runner := testHost(t)
	failure := errors.New("annotation event sink unavailable")
	h.Sink = func(e event.Event) error {
		if e.Type == "memory.query.analyzed" {
			return failure
		}
		return nil
	}
	r, err := h.Run(t.Context(), newSession(t, h), "订单服务 数据库")
	if !errors.Is(err, failure) || r.ExecutionStatus != "failed" || len(runner.requests) != 0 {
		t.Fatalf("continued after annotation output failure: %+v %v", r, err)
	}
}

func TestObjectAndTopicConstraintsCannotBypassApplicability(t *testing.T) {
	h, _ := testHost(t)
	inputs := map[string]string{
		"订单服务的数据库使用 PostgreSQL。": "project_fact",
		"账单服务的数据库必须使用 MySQL。":    "constraint",
		"密钥每九十天更换一次。":            "constraint",
		"禁止自动推送代码。":              "constraint",
	}
	h.Decider = deciderFunc(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		if kind, ok := inputs[input]; ok {
			return memory.Decision{Store: true, Kind: kind, Details: json.RawMessage(`{"test_only":true}`)}, nil
		}
		return memory.Decision{Kind: "none"}, nil
	})
	for _, text := range []string{"订单服务的数据库使用 PostgreSQL。", "账单服务的数据库必须使用 MySQL。", "密钥每九十天更换一次。", "禁止自动推送代码。"} {
		runOK(t, h, newSession(t, h), text)
	}
	for _, tc := range []struct{ query, want string }{
		{"订单服务 数据库", "订单服务的数据库使用 PostgreSQL。"},
		{"凭据的轮换周期", "密钥每九十天更换一次。"},
	} {
		r := runOK(t, h, newSession(t, h), tc.query)
		got := map[string]bool{}
		for _, m := range r.Context {
			got[m.Text] = true
		}
		if len(got) != 2 || !got[tc.want] || !got["禁止自动推送代码。"] {
			t.Fatalf("constraint bypass or global constraint loss: %+v", r.Context)
		}
	}
}
