package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	sdk "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/handler"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/utils"
)

type fakeStreamClient struct {
	start  func(context.Context) error
	closes int
}

func (c *fakeStreamClient) Start(ctx context.Context) error { return c.start(ctx) }
func (c *fakeStreamClient) Close()                          { c.closes++ }

func TestReceiverAcknowledgesOnlyHandledOrExplicitlyRejectedMessages(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		unauthorized, failHandle, failObserve bool
	}{
		{"stored", false, false, false}, {"owner rejected", true, false, false}, {"store failed", false, true, false}, {"rejection observation failed", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw := callbackPayload(t, func(data map[string]any) {
				if tc.unauthorized {
					data["senderStaffId"] = "someone-else"
				}
			})
			type callbackResult struct {
				ack *payload.DataFrameResponse
				err error
			}
			outcome := make(chan callbackResult, 1)
			fake := &fakeStreamClient{}
			receiver := Receiver{Config: testConfig, Now: func() time.Time { return testNow }, newClient: func(cfg Config, callback handler.IFrameHandler) streamClient {
				// Exercise the actual official SDK subscription boundary without opening
				// a socket or loading any production credential.
				client := newSDKClient(cfg, callback).(*sdk.StreamClient)
				native, err := client.GetHandler(utils.SubscriptionTypeKCallback, payload.BotMessageCallbackTopic)
				if err != nil {
					t.Fatal(err)
				}
				fake.start = func(context.Context) error {
					go func() {
						ack, err := native(context.Background(), &payload.DataFrame{Headers: payload.DataFrameHeader{"messageId": "transport-1"}, Data: string(raw)})
						outcome <- callbackResult{ack, err}
						if err == nil {
							cancel()
						}
					}()
					return nil
				}
				return fake
			}}
			handled := 0
			rejected := 0
			err := receiver.Run(ctx, func(context.Context, Message) error {
				handled++
				if tc.failHandle {
					return errors.New("private-session-capability")
				}
				return nil
			}, func(kind, message string) error {
				if strings.Contains(message, "private-session-capability") {
					t.Error("observer leaked a credential")
				}
				if kind == "rejected" {
					rejected++
					if tc.failObserve {
						return errors.New("private-session-capability")
					}
				}
				return nil
			})
			result := <-outcome
			if tc.failHandle || tc.failObserve {
				if err == nil || result.err == nil || result.ack != nil || strings.Contains(err.Error(), "private-session-capability") {
					t.Fatalf("failed ingestion acknowledged or leaked: %v %+v", err, result)
				}
				if response := payload.NewErrorDataFrameResponse(result.err); response.Code != 500 || strings.Contains(response.Message, "private-session-capability") {
					t.Fatalf("unsafe SDK failure ACK: %+v", response)
				}
			} else if !errors.Is(err, context.Canceled) || result.err != nil || result.ack == nil || result.ack.Code != 200 || result.ack.Data != `{"response":null}` {
				t.Fatalf("durable handling did not acknowledge: %v %+v", err, result)
			}
			if tc.unauthorized && (handled != 0 || rejected != 1) {
				t.Fatalf("unauthorized input reached host: handled=%d rejected=%d", handled, rejected)
			}
			if !tc.unauthorized && handled != 1 {
				t.Fatalf("authorized message not handled: %d", handled)
			}
			if fake.closes != 1 {
				t.Fatalf("client not closed: %d", fake.closes)
			}
		})
	}
}

func TestReceiverStartupErrorDoesNotReportConnectedAndCloses(t *testing.T) {
	fake := &fakeStreamClient{start: func(context.Context) error { return errors.New(testReplyURL) }}
	receiver := Receiver{Config: testConfig, newClient: func(Config, handler.IFrameHandler) streamClient { return fake }}
	observed := 0
	err := receiver.Run(context.Background(), func(context.Context, Message) error { return nil }, func(string, string) error { observed++; return nil })
	if err == nil || strings.Contains(err.Error(), "private-session-capability") || observed != 0 || fake.closes != 1 {
		t.Fatalf("unsafe startup failure: %v observations=%d closes=%d", err, observed, fake.closes)
	}
}

func TestReceiverSDKLifecycleFailureTerminatesSafely(t *testing.T) {
	for _, trigger := range []string{"error", "malformed frame", "panic", "fatal", "capacity"} {
		t.Run(trigger, func(t *testing.T) {
			fake := &fakeStreamClient{start: func(context.Context) error {
				switch trigger {
				case "error":
					logger.GetLogger().Errorf("connection read failure %s", testReplyURL)
				case "malformed frame":
					logger.GetLogger().Errorf("connection process decode data frame error: length=[%d] error=[%s]", 42, testReplyURL)
				case "panic":
					logger.GetLogger().Errorf("connection process panic due to unknown reason, error=[%s]", testReplyURL)
				case "fatal":
					logger.GetLogger().Fatalf("private transport %s", testReplyURL)
				case "capacity":
					logger.GetLogger().Warningf("connection handler capacity reached, message is left unacknowledged for retry")
				}
				return nil
			}}
			receiver := Receiver{Config: testConfig, newClient: func(Config, handler.IFrameHandler) streamClient { return fake }}
			var events []string
			err := receiver.Run(context.Background(), func(context.Context, Message) error { return nil }, func(kind, message string) error { events = append(events, kind+":"+message); return nil })
			if err == nil || strings.Contains(err.Error(), "private-session-capability") || len(events) != 1 || !strings.HasPrefix(events[0], "error:") || fake.closes != 1 {
				t.Fatalf("failure hidden: %v events=%v closes=%d", err, events, fake.closes)
			}
		})
	}
}

func TestReceiverKnownTransportFailuresRecoverWithoutCancelingHost(t *testing.T) {
	formats := []string{
		"connection process read message error: messageType=[%d] message=[%s] error=[%s]",
		"connection process is closed",
		"connection write ping message error: error=[%s]",
		"ping time out, connection is closing",
		"StreamClient reconnect error, attempt=[%d] error=[%s]",
		"StreamClient close. error=[%s]",
		"connection processDataFrame send response error: error=[%s]",
		"SendDataFrameResponse error, conn nil or write failed, error=[%s]",
		"StreamClient.OnDisconnect",
	}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fake := &fakeStreamClient{start: func(context.Context) error {
				if format == "StreamClient.OnDisconnect" {
					logger.GetLogger().Debugf(format, testReplyURL)
				} else {
					logger.GetLogger().Errorf(format, testReplyURL)
				}
				return nil
			}}
			receiver := Receiver{Config: testConfig, newClient: func(Config, handler.IFrameHandler) streamClient { return fake }}
			var events []string
			want := "connected,reconnecting,reconnected"
			if strings.Contains(format, "send response") || strings.Contains(format, "SendDataFrameResponse") {
				want = "connected,transport_error"
			}
			err := receiver.Run(ctx, func(context.Context, Message) error {
				t.Fatal("transport recovery replayed a message")
				return nil
			}, func(kind, message string) error {
				if strings.Contains(message, "private-session-capability") {
					t.Error("transport observation leaked SDK arguments")
				}
				events = append(events, kind)
				if kind == "reconnecting" {
					if ctx.Err() != nil {
						t.Error("transport failure canceled the Host context")
					}
					logger.GetLogger().Infof("StreamClient reconnect success")
				} else if kind == "reconnected" {
					cancel()
				} else if kind == "transport_error" {
					if ctx.Err() != nil || strings.Contains(message, "retrying") {
						t.Error("failed ACK canceled Host work or claimed a reconnect")
					}
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || strings.Join(events, ",") != want || fake.closes != 1 {
				t.Fatalf("transport recovery did not stay available: err=%v events=%v closes=%d", err, events, fake.closes)
			}
		})
	}
}

func TestReceiverSDKReconnectsAndHandlesNewMessage(t *testing.T) {
	for _, serverDisconnect := range []bool{false, true} {
		name := "socket closed"
		if serverDisconnect {
			name = "server disconnect command"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := callbackPayload(t, nil)
			second := callbackPayload(t, func(data map[string]any) { data["msgId"] = "message-2" })
			var connections atomic.Int32
			secondACK := make(chan struct{})
			restored := make(chan struct{})
			upgrader := websocket.Upgrader{}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == utils.GetConnectionEndpointAPIUrl {
					_ = json.NewEncoder(w).Encode(map[string]string{"endpoint": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws", "ticket": "private-session-capability"})
					return
				}
				conn, err := upgrader.Upgrade(w, req, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				n := connections.Add(1)
				raw, id := first, "transport-1"
				if n == 2 {
					raw, id = second, "transport-2"
				}
				frame := payload.DataFrame{Type: utils.SubscriptionTypeKCallback, Headers: payload.DataFrameHeader{"messageId": id, "topic": payload.BotMessageCallbackTopic}, Data: string(raw)}
				if err := conn.WriteJSON(frame); err != nil {
					t.Errorf("write callback: %v", err)
					return
				}
				var ack payload.DataFrameResponse
				if err := conn.ReadJSON(&ack); err != nil {
					t.Errorf("read callback acknowledgement: %v", err)
					return
				}
				if ack.Code != 200 || ack.GetHeader("messageId") != id {
					t.Errorf("callback was not acknowledged: %+v", ack)
					return
				}
				if n == 1 {
					if serverDisconnect {
						disconnect := payload.DataFrame{Type: utils.SubscriptionTypeKSystem, Headers: payload.DataFrameHeader{"messageId": "disconnect-1", "topic": "disconnect"}, Data: "{}"}
						if err := conn.WriteJSON(disconnect); err != nil {
							t.Errorf("write server disconnect: %v", err)
							return
						}
						if err := conn.ReadJSON(&ack); err != nil || ack.GetHeader("messageId") != "disconnect-1" || ack.Code != 200 {
							t.Errorf("server disconnect was not acknowledged before closing: %+v %v", ack, err)
						}
					}
					return
				}
				if n == 2 {
					close(secondACK)
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			receiver := Receiver{Config: testConfig, Now: func() time.Time { return testNow }, newClient: func(cfg Config, callback handler.IFrameHandler) streamClient {
				client := newSDKClient(cfg, callback).(*sdk.StreamClient)
				if !client.AutoReconnect {
					t.Error("SDK reconnect is disabled")
				}
				sdk.WithOpenApiHost(server.URL)(client)
				sdk.WithKeepAlive(0)(client)
				return client
			}}
			go func() {
				select {
				case <-secondACK:
				case <-ctx.Done():
					return
				}
				select {
				case <-restored:
					cancel()
				case <-ctx.Done():
				}
			}()
			var handled atomic.Int32
			var events []string
			start := time.Now()
			err := receiver.Run(ctx, func(_ context.Context, message Message) error {
				count := handled.Add(1)
				if count == 1 && message.ID != "message-1" || count == 2 && message.ID != "message-2" || count > 2 {
					t.Errorf("recovery replayed or changed callback order: count=%d message=%s", count, message.ID)
				}
				return nil
			}, func(kind, message string) error {
				if strings.Contains(message, "private-session-capability") {
					t.Error("recovery logging disclosed session credentials")
				}
				events = append(events, kind)
				if kind == "reconnected" {
					close(restored)
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || connections.Load() != 2 || handled.Load() != 2 || strings.Join(events, ",") != "connected,reconnecting,reconnected" {
				t.Fatalf("SDK reconnect failed: err=%v connections=%d handled=%d events=%v", err, connections.Load(), handled.Load(), events)
			}
			if time.Since(start) < 3*time.Second {
				t.Fatal("reconnect skipped the SDK backoff")
			}
		})
	}
}

func TestReceiverSDKReconnectBackoffStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	upgrader := websocket.Upgrader{}
	var opens atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == utils.GetConnectionEndpointAPIUrl {
			opens.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"endpoint": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws", "ticket": "private-session-capability"})
			return
		}
		if conn, err := upgrader.Upgrade(w, req, nil); err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	var client *sdk.StreamClient
	receiver := Receiver{Config: testConfig, newClient: func(cfg Config, callback handler.IFrameHandler) streamClient {
		client = newSDKClient(cfg, callback).(*sdk.StreamClient)
		sdk.WithOpenApiHost(server.URL)(client)
		return client
	}}
	start := time.Now()
	err := receiver.Run(ctx, func(context.Context, Message) error { return nil }, func(kind, _ string) error {
		if kind == "reconnecting" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || time.Since(start) >= time.Second || opens.Load() != 1 {
		t.Fatalf("reconnect did not cancel promptly: %v opens=%d duration=%s", err, opens.Load(), time.Since(start))
	}
}

func TestReceiverObserverFailureIsFatal(t *testing.T) {
	fake := &fakeStreamClient{start: func(context.Context) error { return nil }}
	receiver := Receiver{Config: testConfig, newClient: func(Config, handler.IFrameHandler) streamClient { return fake }}
	cause := errors.New("observation store failed")
	err := receiver.Run(context.Background(), func(context.Context, Message) error { return nil }, func(string, string) error { return cause })
	if !errors.Is(err, cause) || fake.closes != 1 {
		t.Fatalf("observation failure hidden: %v", err)
	}
}

func TestReceiverCancellationWaitsForInFlightIngestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	finished := make(chan struct{})
	raw := callbackPayload(t, nil)
	fake := &fakeStreamClient{}
	receiver := Receiver{Config: testConfig, Now: func() time.Time { return testNow }, newClient: func(_ Config, callback handler.IFrameHandler) streamClient {
		fake.start = func(context.Context) error {
			go func() {
				_, _ = callback(context.Background(), &payload.DataFrame{Headers: payload.DataFrameHeader{"messageId": "transport-1"}, Data: string(raw)})
			}()
			return nil
		}
		return fake
	}}
	go func() { <-started; cancel() }()
	err := receiver.Run(ctx, func(ctx context.Context, _ Message) error {
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > ingestionTimeout {
			t.Error("ingestion must have a bounded deadline")
		}
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}, func(string, string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not surfaced: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("receiver returned with an active callback")
	}
	if fake.closes != 1 {
		t.Fatal("receiver did not close the stream")
	}
}
