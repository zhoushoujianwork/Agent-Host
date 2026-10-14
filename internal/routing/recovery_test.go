package routing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"agent-host/internal/decision/jev"
)

func TestRoutingChoiceMismatchRecovery(t *testing.T) {
	for _, scenario := range []string{"recover", "persistent", "http", "malformed", "retry-http"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			var first json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var input struct {
					State     json.RawMessage         `json:"state"`
					Questions map[string]jev.Question `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				if calls == 1 {
					first = input.State
				} else if !reflect.DeepEqual(first, input.State) {
					t.Error("recovery changed task context")
				}
				if scenario == "http" || scenario == "retry-http" && calls == 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if scenario == "malformed" {
					w.Write([]byte(`{"bad":true}`))
					return
				}
				response := jev.Response{Model: "test-model", Answers: map[string]jev.Answer{}}
				confidence := 1.0
				for key, q := range input.Questions {
					criteria := q.Criteria.(map[string]any)
					selected := "unrelated"
					switch key {
					case "mode", "association":
						selected = "general"
					case "project_need":
						selected = "not_required"
					case "latest_relation":
						selected = "independent"
					case "scope_change":
						selected = "change"
					case "primary":
						selected = "none"
					default:
						if _, ok := criteria["none"]; ok {
							selected = "none"
						}
					}
					probabilities := map[string]float64{}
					for option := range criteria {
						probabilities[option] = 0
					}
					probabilities[selected] = 1
					response.Answers[key] = jev.Answer{Type: "choice", Choice: selected, Probabilities: probabilities, Confidence: &confidence}
				}
				if calls == 1 || scenario == "persistent" {
					answer := response.Answers["scope_change"]
					answer.Choice = "preserve"
					response.Answers["scope_change"] = answer
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client, err := jev.NewCustom("test-secret", "test-model", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			d, err := New(client).Decide(context.Background(), Request{Text: "Search the registry before creating an image.", Projects: catalog(), Recent: []TaskContext{{Text: "Do we have a Go base image?", MessageID: "prior", RunID: "prior-run"}}})
			expectedCalls := 2
			if scenario == "http" || scenario == "malformed" {
				expectedCalls = 1
			}
			if calls != expectedCalls {
				t.Fatalf("calls=%d want=%d", calls, expectedCalls)
			}
			if scenario != "recover" {
				var evaluation *EvaluationError
				if !errors.As(err, &evaluation) || evaluation.Attempts != expectedCalls || d.Kind != "" {
					t.Fatalf("invalid response produced decision: %+v err=%v", d, err)
				}
				return
			}
			if err != nil || d.Kind != "default" {
				t.Fatalf("recovery failed: %+v err=%v", d, err)
			}
			var audit struct {
				RejectedResponse jev.Response `json:"rejected_response"`
				Response         jev.Response `json:"response"`
			}
			if err := json.Unmarshal(d.Details, &audit); err != nil {
				t.Fatal(err)
			}
			if audit.RejectedResponse.Answers["scope_change"].Choice != "preserve" || audit.Response.Answers["scope_change"].Choice != "change" {
				t.Fatal("recovery lost original or accepted response")
			}
		})
	}
}
