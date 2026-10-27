package experience

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOversizedPayloadRetainsEvidenceAndAuditsWholeRejection(t *testing.T) {
	for _, stage := range []string{"source", "decision state"} {
		t.Run(stage, func(t *testing.T) {
			evidence := fixtureEvidence()
			evidence[0].Result = json.RawMessage(`"` + strings.Repeat("x", MaxSourceBytes) + `"`)
			before, _ := json.Marshal(evidence)
			var audit json.RawMessage
			var err error
			if stage == "source" {
				out, extractErr := NewExtractor(runnerFunc(nil), "fixture").Extract(t.Context(), fixtureRun(), evidence, nil)
				audit, err = out.Audit, extractErr
			} else {
				f := &fakeEvaluator{}
				out, assessErr := (&JevJudge{client: f}).Assess(t.Context(), fixtureDraft(), evidence)
				audit, err = out.Audit, assessErr
				if f.calls != 0 {
					t.Fatal("oversized evidence reached provider")
				}
			}
			var limit *LimitError
			if !errors.As(err, &limit) || limit.Stage != stage || limit.ActualBytes <= limit.LimitBytes || limit.Unprocessed != "entire_payload" {
				t.Fatalf("missing explicit limit: %v", err)
			}
			var saved struct {
				Limit *LimitError `json:"limit"`
			}
			if json.Unmarshal(audit, &saved) != nil || !reflect.DeepEqual(saved.Limit, limit) {
				t.Fatalf("missing durable limit: %s", audit)
			}
			after, _ := json.Marshal(evidence)
			if string(before) != string(after) {
				t.Fatal("source evidence mutated")
			}
		})
	}
}

func TestPayloadBoundary(t *testing.T) {
	// A JSON string includes two quote bytes.
	for _, n := range []int{MaxDecisionBytes - 2, MaxDecisionBytes - 1} {
		err := boundedState(strings.Repeat("x", n))
		if (err != nil) != (n+2 > MaxDecisionBytes) {
			t.Fatalf("bytes=%d err=%v", n+2, err)
		}
	}
}
