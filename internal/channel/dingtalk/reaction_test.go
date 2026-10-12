package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const reactionTestToken = "private-platform-token"

func reactionResponse(req *http.Request, status int, raw string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw)), Request: req}
}

func newTestReactor(transport roundTripFunc) *Reactor {
	return &Reactor{Config: testConfig, RobotCode: "fixture-robot", Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: transport}}
}

func TestReactionStagesUseOriginalMessageAndExplicitRobotCode(t *testing.T) {
	stages := []string{"已收到", "处理中", "检索记忆", "AI执行", "Jev判断", "保存记忆", "整理证据", "提取经验", "Jev评估", "保存候选", "发送回复", "已完成", "打叉"}
	var paths, labels []string
	tokenCalls := 0
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "api.dingtalk.com" || req.Header.Get("Content-Type") != "application/json" {
			t.Fatal("reaction request escaped the configured official protocol")
		}
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > replyTimeout {
			t.Fatal("reaction request has no bounded deadline")
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if req.URL.Path == reactionTokenPath {
			tokenCalls++
			if len(body) != 2 || body["appKey"] != testConfig.ClientID || body["appSecret"] != testConfig.ClientSecret || req.Header.Get("x-acs-dingtalk-access-token") != "" {
				t.Fatal("application token request used the wrong credentials or fields")
			}
			return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
		}
		if req.Header.Get("x-acs-dingtalk-access-token") != reactionTestToken || body["robotCode"] != "fixture-robot" || body["openConversationId"] != "cid-original==" || body["openMsgId"] != "msg-original==" || body["emotionType"] != float64(2) || len(body) != 6 {
			t.Fatal("reaction payload lost original platform identifiers or authentication")
		}
		label, ok := body["emotionName"].(string)
		if !ok {
			t.Fatal("reaction label is missing")
		}
		emotion, ok := body["textEmotion"].(map[string]any)
		if !ok || len(emotion) != 4 || emotion["emotionId"] != "2659900" || emotion["emotionName"] != label || emotion["text"] != label || emotion["backgroundId"] != "im_bg_1" {
			t.Fatal("reaction text emotion did not preserve the stage label")
		}
		paths = append(paths, req.URL.Path)
		labels = append(labels, label)
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	for _, label := range stages {
		if err := reactor.AddReaction(context.Background(), "cid-original==", "msg-original==", label); err != nil {
			t.Fatal(err)
		}
		if err := reactor.RemoveReaction(context.Background(), "cid-original==", "msg-original==", label); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 1 || len(paths) != 2*len(stages) {
		t.Fatalf("token cache or operation count: tokens=%d operations=%d", tokenCalls, len(paths))
	}
	for i, path := range paths {
		want := reactionAddPath
		if i%2 != 0 {
			want = reactionDelPath
		}
		if path != want || labels[i] != stages[i/2] {
			t.Fatalf("stage operation %d used wrong path/label", i)
		}
	}
}

func TestReactionRequiresExplicitPlatformAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, raw, outcome string
		status             int
	}{
		{"accepted", `{"success":true}`, Accepted, 200},
		{"rejected", `{"success":false,"message":"private-provider-detail"}`, Failed, 200},
		{"missing success", `{}`, Unknown, 200},
		{"null success", `{"success":null}`, Unknown, 200},
		{"wrong success type", `{"success":"true"}`, Unknown, 200},
		{"malformed", `{"success":`, Unknown, 200},
		{"trailing", `{"success":true}{}`, Unknown, 200},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), Unknown, 200},
		{"client rejection", "private-provider-detail", Failed, 400},
		{"unauthorized", "private-provider-detail", Failed, 401},
		{"forbidden", "private-provider-detail", Failed, 403},
		{"rate limited", "private-provider-detail", Failed, 429},
		{"server error", "private-provider-detail", Unknown, 500},
		{"empty success", "", Unknown, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			body := &closeReader{Reader: strings.NewReader(tc.raw)}
			reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path == reactionTokenPath {
					return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body, Request: req}, nil
			})
			err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中")
			if ReactionOutcome(err) != tc.outcome || (err == nil) != (tc.outcome == Accepted) || calls != 2 || !body.closed {
				t.Fatalf("outcome=%s error=%v calls=%d bodyClosed=%v", ReactionOutcome(err), err, calls, body.closed)
			}
			assertReactionErrorPrivate(t, err)
		})
	}
	if ReactionOutcome(errors.New("arbitrary failure")) != Unknown {
		t.Fatal("unrecognized errors must have unknown reaction outcome")
	}
}

func assertReactionErrorPrivate(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, private := range []string{testConfig.ClientSecret, reactionTestToken, "private-provider-detail", "cid-original", "msg-original", "https:"} {
		if strings.Contains(err.Error(), private) {
			t.Fatal("reaction error disclosed credentials, provider details or identifiers")
		}
	}
}

func TestReactionTokenFailuresPreventMutationAndDoNotInventLifetime(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": ``, "null": `null`, "missing token": `{"expireIn":7200}`,
		"missing expiry": `{"accessToken":"` + reactionTestToken + `"}`,
		"null expiry":    `{"accessToken":"` + reactionTestToken + `","expireIn":null}`,
		"zero expiry":    `{"accessToken":"` + reactionTestToken + `","expireIn":0}`,
		"negative":       `{"accessToken":"` + reactionTestToken + `","expireIn":-1}`,
		"absurd expiry":  `{"accessToken":"` + reactionTestToken + `","expireIn":9223372036854775807}`,
		"float expiry":   `{"accessToken":"` + reactionTestToken + `","expireIn":12.5}`,
		"header token":   `{"accessToken":"unsafe\r\nheader","expireIn":7200}`,
		"control token":  `{"accessToken":"unsafe\ttoken","expireIn":7200}`,
		"padded token":   `{"accessToken":" token ","expireIn":7200}`,
		"oversize token": `{"accessToken":"` + strings.Repeat("s", 4097) + `","expireIn":7200}`,
		"oversize body":  strings.Repeat("s", maxResponseBytes+1),
		"trailing body":  `{"accessToken":"` + reactionTestToken + `","expireIn":7200}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != reactionTokenPath {
					t.Fatal("invalid token allowed a mutation")
				}
				return reactionResponse(req, 200, raw), nil
			})
			err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中")
			if err == nil || ReactionOutcome(err) != Failed || calls != 1 || reactor.token != "" {
				t.Fatalf("invalid token hidden: %v calls=%d", err, calls)
			}
			assertReactionErrorPrivate(t, err)
		})
	}
}

func TestReactionTokenRefreshHonorsExpiryAndCredentialChanges(t *testing.T) {
	now := testNow
	tokenCalls, mutationCalls := 0, 0
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == reactionTokenPath {
			tokenCalls++
			return reactionResponse(req, 200, fmt.Sprintf(`{"accessToken":"token-%d","expireIn":100}`, tokenCalls)), nil
		}
		mutationCalls++
		if req.Header.Get("x-acs-dingtalk-access-token") != fmt.Sprintf("token-%d", tokenCalls) {
			t.Fatal("reaction used a stale token")
		}
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	reactor.Now = func() time.Time { return now }
	for i, advance := range []time.Duration{0, 89 * time.Second, time.Second, 0} {
		now = now.Add(advance)
		if i == 3 {
			reactor.Config.ClientSecret = "new-credential"
		}
		if err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "已收到"); err != nil {
			t.Fatal(err)
		}
		wantCalls := []int{1, 1, 2, 3}[i]
		if tokenCalls != wantCalls {
			t.Fatalf("step %d refreshed token %d times, want %d", i, tokenCalls, wantCalls)
		}
	}
	if mutationCalls != 4 {
		t.Fatal("token refresh replayed a reaction")
	}
}

func TestReactionExpiredTokenRefreshFailureNeverUsesStaleCache(t *testing.T) {
	now := testNow
	tokenCalls, mutations := 0, 0
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == reactionTokenPath {
			tokenCalls++
			if tokenCalls == 1 {
				return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":100}`), nil
			}
			return reactionResponse(req, 500, "private-provider-detail"), nil
		}
		mutations++
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	reactor.Now = func() time.Time { return now }
	if err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "已收到"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(100 * time.Second)
	err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中")
	if err == nil || ReactionOutcome(err) != Failed || tokenCalls != 2 || mutations != 1 || reactor.token != "" {
		t.Fatalf("expired cache substituted after refresh failure: %v tokens=%d mutations=%d", err, tokenCalls, mutations)
	}
}

func TestReactionRefreshCountsTokenRequestElapsedTime(t *testing.T) {
	now := testNow
	calls := 0
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		calls++
		now = now.Add(2 * time.Second)
		return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":1}`), nil
	})
	reactor.Now = func() time.Time { return now }
	if err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中"); err == nil || ReactionOutcome(err) != Failed || calls != 1 {
		t.Fatalf("elapsed token lifetime was ignored: %v calls=%d", err, calls)
	}
}

func TestReactionConcurrentCallsShareOneTokenRefresh(t *testing.T) {
	var tokens, mutations atomic.Int32
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == reactionTokenPath {
			tokens.Add(1)
			return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
		}
		mutations.Add(1)
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中"); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if tokens.Load() != 1 || mutations.Load() != 16 {
		t.Fatalf("concurrent refreshes=%d mutations=%d", tokens.Load(), mutations.Load())
	}
}

func TestReactionCanceledWaiterDoesNotWaitForAnotherTokenRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == reactionTokenPath {
			close(started)
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
		}
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	first := make(chan error, 1)
	go func() {
		first <- reactor.AddReaction(context.Background(), "cid-original", "msg-original", "已收到")
	}()
	<-started
	defer func() {
		close(release)
		if err := <-first; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := make(chan error, 1)
	go func() { waiter <- reactor.AddReaction(ctx, "cid-original", "msg-original", "处理中") }()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) || ReactionOutcome(err) != Failed {
			t.Fatalf("token waiter cancellation was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled reaction waited on another token request")
	}
}

func TestReactionNetworkFailureSanitizesAndNeverRetries(t *testing.T) {
	for _, tokenStage := range []bool{true, false} {
		calls := 0
		privateErr := errors.New("https://private-provider-detail/" + testConfig.ClientSecret + "/" + reactionTestToken)
		reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
			calls++
			if !tokenStage && req.URL.Path == reactionTokenPath {
				return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
			}
			return nil, privateErr
		})
		err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中")
		want, wantCalls := Unknown, 2
		if tokenStage {
			want, wantCalls = Failed, 1
		}
		if err == nil || ReactionOutcome(err) != want || !errors.Is(err, privateErr) || calls != wantCalls {
			t.Fatalf("network failure lost or retried: %v outcome=%s calls=%d", err, ReactionOutcome(err), calls)
		}
		assertReactionErrorPrivate(t, err)
	}
}

func TestReactionRedirectsNeverCarrySecretsToAnotherHost(t *testing.T) {
	for _, tokenStage := range []bool{true, false} {
		calls := 0
		reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Host != "api.dingtalk.com" {
				t.Fatal("reaction followed a redirect with credentials")
			}
			if !tokenStage && req.URL.Path == reactionTokenPath {
				return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
			}
			response := reactionResponse(req, 307, "private-provider-detail")
			response.Header.Set("Location", "https://private-provider-detail/collect")
			return response, nil
		})
		err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "处理中")
		wantCalls := 2
		if tokenStage {
			wantCalls = 1
		}
		if err == nil || ReactionOutcome(err) != Failed || calls != wantCalls {
			t.Fatalf("redirect followed or hidden: %v calls=%d", err, calls)
		}
		assertReactionErrorPrivate(t, err)
	}
}

func TestReactionAuthenticationRejectionInvalidatesForNextOperationOnly(t *testing.T) {
	tokens, mutations := 0, 0
	reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == reactionTokenPath {
			tokens++
			return reactionResponse(req, 200, fmt.Sprintf(`{"accessToken":"token-%d","expireIn":7200}`, tokens)), nil
		}
		mutations++
		if mutations == 1 {
			return reactionResponse(req, 401, "private-provider-detail"), nil
		}
		return reactionResponse(req, 200, `{"success":true}`), nil
	})
	err := reactor.AddReaction(context.Background(), "cid-original", "msg-original", "已收到")
	if err == nil || ReactionOutcome(err) != Failed || tokens != 1 || mutations != 1 {
		t.Fatal("authentication failure was retried inline or hidden")
	}
	if err := reactor.RemoveReaction(context.Background(), "cid-original", "msg-original", "已收到"); err != nil || tokens != 2 || mutations != 2 {
		t.Fatalf("next explicit operation did not refresh token: %v tokens=%d mutations=%d", err, tokens, mutations)
	}
}

func TestReactionRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	for _, tc := range []struct{ robot, conversation, message, label string }{
		{"", "cid-original", "msg-original", "已收到"},
		{"robot", "", "msg-original", "已收到"},
		{"robot", "cid-original", "", "已收到"},
		{"robot", "cid-original", "msg-original", "arbitrary-label"},
		{"robot", "cid-original", "msg-original", "Jev 判断"},
		{"robot", "cid-original", "msg-original", "jev判断"},
		{"robot", "cid-original", "msg-original", "保存候选\n"},
		{"robot", "cid-original", "msg-original", "已收到 "},
		{"robot", "cid-original\r\n", "msg-original", "已收到"},
		{"robot", "cid-original", "\xff", "已收到"},
		{"robot", "cid-original", strings.Repeat("m", maxIDBytes+1), "已收到"},
	} {
		reactor := newTestReactor(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid reaction made HTTP call")
			return nil, nil
		})
		reactor.RobotCode = tc.robot
		err := reactor.AddReaction(context.Background(), tc.conversation, tc.message, tc.label)
		if err == nil || ReactionOutcome(err) != Failed {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	var absent *Reactor
	if err := absent.AddReaction(context.Background(), "cid", "msg", "已收到"); err == nil || ReactionOutcome(err) != Failed {
		t.Fatal("missing reactor not rejected")
	}
}

func TestReactionHonorsCancellationWithoutStartingOrRepeatingMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reactor := newTestReactor(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled reaction made HTTP call")
		return nil, nil
	})
	if err := reactor.AddReaction(ctx, "cid-original", "msg-original", "处理中"); !errors.Is(err, context.Canceled) || ReactionOutcome(err) != Failed {
		t.Fatalf("pre-dispatch cancellation lost: %v", err)
	}
	for _, tokenStage := range []bool{true, false} {
		calls := 0
		reactor := newTestReactor(func(req *http.Request) (*http.Response, error) {
			calls++
			if !tokenStage && req.URL.Path == reactionTokenPath {
				return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := reactor.AddReaction(ctx, "cid-original", "msg-original", "处理中")
		cancel()
		want, wantCalls := Unknown, 2
		if tokenStage {
			want, wantCalls = Failed, 1
		}
		if !errors.Is(err, context.DeadlineExceeded) || ReactionOutcome(err) != want || calls != wantCalls {
			t.Fatalf("request cancellation was lost or retried: %v calls=%d", err, calls)
		}
	}
}
