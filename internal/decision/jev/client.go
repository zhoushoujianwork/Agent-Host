// Package jev implements TypeSafe System One requests through the official
// endpoint or an explicitly configured third-party gateway, with no fallback.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	Endpoint        = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-1.13.0"
	maxBodyBytes    = 4 << 20
	maxRequestBytes = 1 << 20
)

type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type Response struct {
	ID           string            `json:"id,omitempty"`
	Model        string            `json:"model"`
	Answers      map[string]Answer `json:"answers"`
	Usage        Usage             `json:"usage"`
	UsedProvider string            `json:"used_provider,omitempty"`
	UsedPool     string            `json:"used_pool,omitempty"`
}

// ChoiceMismatchError retains the rejected provider response for routing audit.
// It never turns an inconsistent answer into a usable decision.
type ChoiceMismatchError struct {
	Question string
	Response Response
}

func (e *ChoiceMismatchError) Error() string {
	return fmt.Sprintf("question %q: choice is not a maximum-probability option", e.Question)
}

type Client struct {
	apiKey   string
	model    string
	provider string
	endpoint string
	http     *http.Client
}

// New only accepts credentials for the official endpoint; it never consults
// gateway aliases or other providers' environment variables.
func New(apiKey, model string) (*Client, error) {
	if model == "" {
		model = DefaultModel
	}
	return newClient(apiKey, model, "typesafe", Endpoint, "TYPESAFE_API_KEY")
}

// NewCustom selects a third-party System One gateway. The endpoint must be
// explicit HTTPS (HTTP only on loopback) without credentials in the URL.
// It does not fall back to TypeSafe.
func NewCustom(apiKey, model, endpoint string) (*Client, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if model == "" {
		model = DefaultModel
	}
	return newClient(apiKey, model, "custom", endpoint, "JEV_API_KEY")
}

// ValidateEndpoint checks a third-party gateway URL before any credential use.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || endpoint != strings.TrimSpace(endpoint) || u.Host == "" || u.Opaque != "" {
		return errors.New("Jev: custom endpoint must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("Jev: custom endpoint must not contain credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("Jev: custom endpoint must use HTTPS, or HTTP on loopback")
}

func newClient(apiKey, model, provider, endpoint, keyName string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("Jev: %s is required for provider %s", keyName, provider)
	}
	if strings.ContainsAny(apiKey, "\r\n") || apiKey != strings.TrimSpace(apiKey) {
		return nil, errors.New("Jev: invalid API key format")
	}
	if strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n") {
		return nil, errors.New("Jev: invalid model name")
	}
	return &Client{apiKey: apiKey, model: model, provider: provider, endpoint: endpoint, http: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Provider() string { return c.provider }
func (c *Client) Endpoint() string { return c.endpoint }
func (c *Client) Model() string    { return c.model }

func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (Response, error) {
	if c == nil {
		return Response{}, errors.New("Jev: nil client")
	}
	if err := validateInput(state, questions); err != nil {
		return Response{}, fmt.Errorf("Jev request: %w", err)
	}
	// Third-party gateways receive JSON-string instructions: the gateway
	// verified so far rejects the object shape that TypeSafe documents.
	if c.provider == "custom" {
		compatible := make(map[string]Question, len(questions))
		for name, q := range questions {
			if _, ok := q.Instructions.(string); !ok {
				b, _ := json.Marshal(q.Instructions)
				q.Instructions = string(b)
			}
			compatible[name] = q
		}
		questions = compatible
	}
	payload, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{c.model, state, questions})
	if err != nil {
		return Response{}, errors.New("Jev request cannot be encoded as JSON")
	}
	if len(payload) > maxRequestBytes {
		return Response{}, errors.New("Jev request exceeds 1 MiB limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("create Jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		// Do not expose arbitrary transport errors, which may contain headers.
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("Jev request: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return Response{}, errors.New("Jev request timed out")
		}
		return Response{}, errors.New("Jev request transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Error bodies are untrusted and can reflect the request or credentials.
		return Response{}, fmt.Errorf("Jev provider %s returned HTTP %d (%s)", c.provider, res.StatusCode, http.StatusText(res.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil {
		return Response{}, errors.New("read Jev response failed")
	}
	if len(body) > maxBodyBytes {
		return Response{}, errors.New("Jev response exceeds 4 MiB limit")
	}
	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		return Response{}, errors.New("Jev response is not valid typed JSON")
	}
	if err := validateResponse(body, result, questions); err != nil {
		return Response{}, fmt.Errorf("Jev response: %w", err)
	}
	return result, nil
}

func jsonValue(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

func textOrStructure(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x) != ""
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		return false
	}
}

func validateInput(state any, questions map[string]Question) error {
	s, err := jsonValue(state)
	if err != nil || !textOrStructure(s) {
		return errors.New("state must be a nonempty string, object, or array")
	}
	if len(questions) == 0 || len(questions) > 200 {
		return errors.New("between 1 and 200 questions are required")
	}
	for name, q := range questions {
		if strings.TrimSpace(name) == "" {
			return errors.New("question ID must not be empty")
		}
		instructions, err := jsonValue(q.Instructions)
		if err != nil || !textOrStructure(instructions) {
			return fmt.Errorf("question %q has invalid instructions", name)
		}
		criteria, err := jsonValue(q.Criteria)
		if err != nil {
			return fmt.Errorf("question %q has invalid criteria", name)
		}
		switch q.Type {
		case "choice":
			options, ok := criteria.(map[string]any)
			if !ok || len(options) == 0 || len(options) > 255 {
				return fmt.Errorf("question %q requires 1 to 255 choices", name)
			}
			for key, value := range options {
				if key == "" || value != nil && !textOrStructure(value) {
					return fmt.Errorf("question %q has invalid choice criteria", name)
				}
			}
		case "score":
			levels, ok := criteria.([]any)
			if !ok || len(levels) < 2 || len(levels) > 10 {
				return fmt.Errorf("question %q requires 2 to 10 score levels", name)
			}
			for _, level := range levels {
				if !textOrStructure(level) {
					return fmt.Errorf("question %q has an invalid score level", name)
				}
			}
		case "noul":
			if criteria == nil {
				continue
			}
			options, ok := criteria.(map[string]any)
			if !ok {
				return fmt.Errorf("question %q has invalid noul criteria", name)
			}
			for key, value := range options {
				if key != "true" && key != "false" || !textOrStructure(value) {
					return fmt.Errorf("question %q has invalid noul criteria", name)
				}
			}
		default:
			return fmt.Errorf("question %q has an unsupported type", name)
		}
	}
	return nil
}

func bounded(v *float64, high float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= high
}

func validateResponse(body []byte, r Response, questions map[string]Question) error {
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("missing resolved model")
	}
	var wire struct {
		Answers map[string]struct {
			Probabilities map[string]*float64 `json:"probabilities"`
			Legend        map[string]*string  `json:"legend"`
		} `json:"answers"`
		Usage *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return errors.New("invalid usage")
	}
	if wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil || r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0 {
		return errors.New("missing or invalid token usage")
	}
	if len(r.Answers) != len(questions) {
		return errors.New("answer IDs do not match question IDs")
	}
	for name, q := range questions {
		a, ok := r.Answers[name]
		if !ok || a.Type != q.Type {
			return fmt.Errorf("question %q: missing answer or mismatched type", name)
		}
		if q.Type == "noul" {
			if !bounded(a.Noul, 1) || a.Score != nil || a.Choice != "" || a.Confidence != nil || len(a.Probabilities) != 0 || len(a.Legend) != 0 {
				return fmt.Errorf("question %q: invalid noul answer", name)
			}
			continue
		}
		if !bounded(a.Confidence, 1) || a.Noul != nil {
			return fmt.Errorf("question %q: invalid confidence or answer fields", name)
		}
		criteria, _ := jsonValue(q.Criteria)
		keys := make(map[string]bool)
		if q.Type == "choice" {
			for k := range criteria.(map[string]any) {
				keys[k] = true
			}
		} else {
			for i := range criteria.([]any) {
				keys[strconv.Itoa(i)] = true
			}
		}
		if len(a.Probabilities) != len(keys) {
			return fmt.Errorf("question %q: probability keys mismatch", name)
		}
		sum := 0.0
		for key, p := range a.Probabilities {
			if !keys[key] || !bounded(&p, 1) || wire.Answers[name].Probabilities[key] == nil {
				return fmt.Errorf("question %q: invalid probability", name)
			}
			sum += p
		}
		// Some gateways round individual probabilities. Retain the original
		// values, accepting at most 0.02 aggregate rounding error.
		if math.Abs(sum-1) > 0.020000001 {
			return fmt.Errorf("question %q: probabilities must sum to 1 (rounding tolerance 0.02)", name)
		}
		if q.Type == "choice" {
			if !keys[a.Choice] || a.Score != nil || len(a.Legend) != 0 {
				return fmt.Errorf("question %q: invalid choice", name)
			}
			for _, p := range a.Probabilities {
				if p > a.Probabilities[a.Choice]+1e-9 {
					return &ChoiceMismatchError{Question: name, Response: r}
				}
			}
		} else {
			if a.Choice != "" || !bounded(a.Score, float64(len(keys)-1)) || len(a.Legend) != len(keys) {
				return fmt.Errorf("question %q: invalid score or legend", name)
			}
			weighted := 0.0
			for i := range criteria.([]any) {
				key := strconv.Itoa(i)
				if _, ok := a.Legend[key]; !ok || wire.Answers[name].Legend[key] == nil {
					return fmt.Errorf("question %q: legend keys mismatch", name)
				}
				weighted += float64(i) * a.Probabilities[key]
			}
			// Gateway probabilities and score can each be rounded to two
			// decimal places; account for each level's weighted rounding.
			n := float64(len(keys))
			roundingTolerance := 0.005 + 0.005*n*(n-1)/2
			if math.Abs(weighted-*a.Score) > roundingTolerance+1e-9 {
				return fmt.Errorf("question %q: score disagrees with probabilities", name)
			}
		}
	}
	return nil
}
