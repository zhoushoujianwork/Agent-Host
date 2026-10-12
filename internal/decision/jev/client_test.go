package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const customEndpoint = "https://gateway.example.test/v1/systemone"

func testClient(t *testing.T, handler http.HandlerFunc, custom bool) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	var c *Client
	var err error
	if custom {
		c, err = NewCustom("test-secret", "", customEndpoint)
	} else {
		c, err = New("test-secret", "")
	}
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(s.URL)
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != c.endpoint {
			t.Errorf("unexpected endpoint: %s", r.URL)
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		copy.URL = &u
		return http.DefaultTransport.RoundTrip(copy)
	})
	return c
}

var choiceQuestion = map[string]Question{"q": {Type: "choice", Instructions: "Choose.", Criteria: map[string]string{"yes": "Yes", "no": "No"}}}

const choiceJSON = `{"model":"jev-1.13.0","answers":{"q":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":5}}`

func TestProvidersAndStructuredInstructions(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "typesafe", true: "custom"}[custom], func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid request")
				}
				var req struct {
					Model     string              `json:"model"`
					State     any                 `json:"state"`
					Questions map[string]Question `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				_, isString := req.Questions["q"].Instructions.(string)
				if isString != custom {
					t.Errorf("gateway compatibility conversion = %v", isString)
				}
				if req.Model != DefaultModel {
					t.Errorf("model=%s", req.Model)
				}
				io.WriteString(w, choiceJSON)
			}, custom)
			q := map[string]Question{"q": {Type: "choice", Instructions: map[string]any{"question": "Choose.", "context": []string{"a", "b"}}, Criteria: map[string]any{"yes": "Yes", "no": nil}}}
			r, err := c.Evaluate(context.Background(), map[string]string{"statement": "test"}, q)
			if err != nil {
				t.Fatal(err)
			}
			if r.Answers["q"].Choice != "yes" {
				t.Fatal(r)
			}
			if _, ok := q["q"].Instructions.(map[string]any); !ok {
				t.Fatal("caller question mutated")
			}
			wantEndpoint := Endpoint
			if custom {
				wantEndpoint = customEndpoint
			}
			if c.Endpoint() != wantEndpoint {
				t.Fatal(c.Endpoint())
			}
		})
	}
}

func TestCustomEndpointValidation(t *testing.T) {
	for _, endpoint := range []string{customEndpoint, "http://127.0.0.1:8080/v1/systemone", "http://localhost/v1/systemone", "http://[::1]:9/x"} {
		if c, err := NewCustom("test-secret", "jev-latest", endpoint); err != nil || c.Provider() != "custom" || c.Endpoint() != endpoint || c.Model() != "jev-latest" {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"", "gateway.example.test/v1", "http://gateway.example.test/v1", "ftp://gateway.example.test/v1", "https://user:pass@gateway.example.test/v1", "https://gateway.example.test/v1?key=secret", "https://gateway.example.test/v1#x", " https://gateway.example.test/v1"} {
		if _, err := NewCustom("test-secret", "", endpoint); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	if _, err := NewCustom("", "", customEndpoint); err == nil || !strings.Contains(err.Error(), "JEV_API_KEY") {
		t.Fatal(err)
	}
}

func TestAllAnswerPrimitives(t *testing.T) {
	body := `{"model":"jev-1.13.0","answers":{"q":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.8},"n":{"type":"noul","noul":0},"s":{"type":"score","score":1.05,"legend":{"0":"Calm","1":"Frustrated","2":"Angry"},"probabilities":{"0":0,"1":0.95,"2":0.05},"confidence":0.92}},"usage":{"input_tokens":10,"output_tokens":5},"used_provider":"gateway-extra-metadata"}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }, false)
	q := map[string]Question{"q": choiceQuestion["q"], "n": {Type: "noul", Instructions: "Urgent?"}, "s": {Type: "score", Instructions: []string{"Rate frustration"}, Criteria: []string{"Calm", "Frustrated", "Angry"}}}
	if _, err := c.Evaluate(context.Background(), "Message", q); err != nil {
		t.Fatal(err)
	}
}

func TestRejectMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"missing answer":     strings.Replace(choiceJSON, `"q":`, `"other":`, 1),
		"extra answer":       strings.Replace(choiceJSON, `"answers":{`, `"answers":{"extra":{"type":"noul","noul":0.5},`, 1),
		"type mismatch":      strings.Replace(choiceJSON, `"type":"choice"`, `"type":"noul"`, 1),
		"out of set choice":  strings.Replace(choiceJSON, `"choice":"yes"`, `"choice":"maybe"`, 1),
		"nonwinning choice":  strings.Replace(choiceJSON, `"choice":"yes"`, `"choice":"no"`, 1),
		"bad sum":            strings.Replace(choiceJSON, `"yes":0.9`, `"yes":0.5`, 1),
		"out of range":       strings.Replace(choiceJSON, `"confidence":0.8`, `"confidence":1.1`, 1),
		"missing confidence": strings.Replace(choiceJSON, `,"confidence":0.8`, ``, 1),
		"missing usage":      strings.Replace(choiceJSON, `,"usage":{"input_tokens":10,"output_tokens":5}`, ``, 1),
		"null usage":         strings.Replace(choiceJSON, `"input_tokens":10`, `"input_tokens":null`, 1),
		"negative usage":     strings.Replace(choiceJSON, `"input_tokens":10`, `"input_tokens":-1`, 1),
		"null probability":   strings.Replace(strings.Replace(choiceJSON, `"yes":0.9`, `"yes":1`, 1), `"no":0.1`, `"no":null`, 1),
		"missing model":      strings.Replace(choiceJSON, `"model":"jev-1.13.0"`, `"model":""`, 1),
		"malformed JSON":     choiceJSON + `{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }, false)
			if _, err := c.Evaluate(context.Background(), "message", choiceQuestion); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestRoundedProbabilities(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Replace(choiceJSON, `"yes":0.9`, `"yes":0.89`, 1))
	}, true)
	r, err := c.Evaluate(context.Background(), "message", choiceQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if r.Answers["q"].Probabilities["yes"] != 0.89 {
		t.Fatal("probabilities were silently normalized")
	}
}

func TestNoRedirectRetryOrCredentialLeak(t *testing.T) {
	for _, status := range []int{302, 401, 429, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			count := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				w.Header().Set("Location", "https://other.invalid/secret")
				w.WriteHeader(status)
				io.WriteString(w, "reflected test-secret")
			}, true)
			_, err := c.Evaluate(context.Background(), "message", choiceQuestion)
			if err == nil {
				t.Fatal("expected HTTP failure")
			}
			if count != 1 {
				t.Fatalf("requests=%d", count)
			}
			if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "other.invalid") {
				t.Fatal("untrusted error body exposed")
			}
		})
	}
	c, _ := New("test-secret", "")
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-secret") })
	if _, err := c.Evaluate(context.Background(), "message", choiceQuestion); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatal(err)
	}
}

func TestBoundsCancellationAndInvalidInput(t *testing.T) {
	if _, err := New("", ""); err == nil {
		t.Fatal("accepted missing key")
	}
	if _, err := NewCustom("bad\nkey", "", customEndpoint); err == nil {
		t.Fatal("accepted newline key")
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", maxBodyBytes+1)) }, false)
	if _, err := c.Evaluate(context.Background(), "message", choiceQuestion); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Evaluate(ctx, "message", choiceQuestion); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, q := range []Question{{Type: "unknown", Instructions: "?"}, {Type: "choice", Instructions: "?", Criteria: map[string]string{}}, {Type: "score", Instructions: "?", Criteria: []string{"one"}}, {Type: "noul", Instructions: false}, {Type: "noul", Instructions: "?", Criteria: map[string]string{"yes": "bad key"}}} {
		if _, err := c.Evaluate(context.Background(), "message", map[string]Question{"q": q}); err == nil {
			t.Fatal("accepted invalid question")
		}
	}
}

// Synthetic fixtures cover protocol variants without exposing private provider
// experiments or claiming that fixture validation is live gateway acceptance.
func TestSyntheticProtocolResponses(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "protocol-responses.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Synthetic bool `json:"synthetic"`
		Cases     []struct {
			Name    string `json:"name"`
			Request struct {
				Questions map[string]Question `json:"questions"`
			} `json:"request"`
			Response json.RawMessage `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &artifact); err != nil {
		t.Fatal(err)
	}
	if !artifact.Synthetic || len(artifact.Cases) != 9 {
		t.Fatal("expected nine explicitly synthetic protocol cases")
	}
	seen := make(map[string]bool)
	for _, c := range artifact.Cases {
		if c.Name == "" || seen[c.Name] {
			t.Fatal("synthetic protocol cases require unique names")
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			if err := validateInput("Synthetic protocol input", c.Request.Questions); err != nil {
				t.Fatal(err)
			}
			var response Response
			if err := json.Unmarshal(c.Response, &response); err != nil {
				t.Fatal(err)
			}
			if err := validateResponse(c.Response, response, c.Request.Questions); err != nil {
				t.Fatal(err)
			}
		})
	}
}
