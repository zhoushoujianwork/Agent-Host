package dingtalk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	sdk "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/handler"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/utils"
)

type streamClient interface {
	Start(context.Context) error
	Close()
}

const ingestionTimeout = 5 * time.Second

type Receiver struct {
	Config    Config
	Now       func() time.Time
	newClient func(Config, handler.IFrameHandler) streamClient
}

var (
	receiverLock  sync.Mutex
	installLogger sync.Once
	sdkLog        = &safeSDKLogger{}
)

func newSDKClient(cfg Config, callback handler.IFrameHandler) streamClient {
	return sdk.NewStreamClient(
		sdk.WithAppCredential(sdk.NewAppCredentialConfig(cfg.ClientID, cfg.ClientSecret)),
		// The SDK retries disconnected sockets with bounded exponential backoff.
		// Close cancels that lifecycle without touching Host task execution.
		sdk.WithAutoReconnect(true),
		sdk.WithMaxPendingHandlers(16),
		sdk.WithSubscription(utils.SubscriptionTypeKCallback, payload.BotMessageCallbackTopic, callback),
	)
}

// Run acknowledges durable ingestion independently of task execution and IM
// delivery. Transient socket failures recover within the SDK while Host tasks
// continue. Ingestion, observation and unrecognized SDK failures remain fatal.
func (r Receiver) Run(ctx context.Context, handle func(context.Context, Message) error, observe func(kind, message string) error) error {
	if err := ValidateConfig(r.Config); err != nil {
		return err
	}
	if handle == nil || observe == nil {
		return errors.New("DingTalk receiver requires message and observation handlers")
	}
	var observationMu sync.Mutex
	record := observe
	observe = func(kind, message string) error {
		observationMu.Lock()
		defer observationMu.Unlock()
		return record(kind, message)
	}
	// The official SDK uses a process-global logger without per-client state.
	// One active receiver prevents lifecycle failures being assigned to another
	// connection, and logger installation occurs before SDK goroutines exist.
	if !receiverLock.TryLock() {
		return errors.New("a DingTalk receiver is already running in this process")
	}
	defer receiverLock.Unlock()
	installLogger.Do(func() { logger.SetLogger(sdkLog) })
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, 1)
	report := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	transportEvents := make(chan string, 16)
	sdkLog.setReport(report, func(kind string) {
		// SDK socket goroutines must not wait for operational logging. Retain
		// the newest transition if duplicate transport notices fill the buffer.
		select {
		case transportEvents <- kind:
		default:
			select {
			case <-transportEvents:
			default:
			}
			select {
			case transportEvents <- kind:
			default:
			}
		}
	})
	defer sdkLog.setReport(nil, nil)
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	var callbackMu sync.Mutex
	var active sync.WaitGroup
	stopping := false
	callback := func(callbackCtx context.Context, frame *payload.DataFrame) (*payload.DataFrameResponse, error) {
		callbackMu.Lock()
		if stopping {
			callbackMu.Unlock()
			return nil, errors.New("DingTalk receiver is stopping")
		}
		active.Add(1)
		callbackMu.Unlock()
		defer active.Done()
		fail := func(operation string, cause error) (*payload.DataFrameResponse, error) {
			err := &operationError{operation: operation, cause: cause}
			report(err)
			return nil, err
		}
		if frame == nil || frame.GetMessageId() == "" {
			return fail("callback envelope validation", nil)
		}
		message, err := Parse([]byte(frame.Data), r.Config, now())
		if err != nil {
			if !errors.Is(err, ErrRejected) {
				return fail("callback parsing", err)
			}
			if observeErr := observe("rejected", err.Error()); observeErr != nil {
				return fail("callback rejection recording", observeErr)
			}
			return successACK(), nil
		}
		// Use host cancellation as well as the SDK callback's connection context.
		// The callback performs only bounded persistence, never a model call.
		messageCtx, stop := context.WithTimeout(runCtx, ingestionTimeout)
		defer stop()
		if callbackCtx != nil {
			stopConnection := context.AfterFunc(callbackCtx, stop)
			defer stopConnection()
		}
		if err = handle(messageCtx, message); err != nil {
			return fail("message ingestion", err)
		}
		return successACK(), nil
	}
	factory := r.newClient
	if factory == nil {
		factory = newSDKClient
	}
	client := factory(r.Config, callback)
	if client == nil {
		return errors.New("DingTalk Stream client could not be constructed")
	}
	defer func() {
		cancel()
		callbackMu.Lock()
		stopping = true
		callbackMu.Unlock()
		client.Close()
		active.Wait()
	}()
	connectCtx, stopConnect := context.WithTimeout(runCtx, 15*time.Second)
	err := client.Start(connectCtx)
	stopConnect()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &operationError{operation: "Stream connection", cause: err}
	}
	select {
	case err = <-failures:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return recordFailure(observe, err)
	default:
	}
	if err = observe("connected", "DingTalk Stream connection established"); err != nil {
		return &operationError{operation: "connection observation", cause: err}
	}
	recovering := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-failures:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return recordFailure(observe, err)
		case kind := <-transportEvents:
			switch kind {
			case "reconnecting":
				if recovering {
					continue
				}
				recovering = true
				if err := observe(kind, "DingTalk Stream connection interrupted; retrying with bounded backoff"); err != nil {
					return &operationError{operation: "transport recovery observation", cause: err}
				}
			case "reconnected":
				recovering = false
				if err := observe(kind, "DingTalk Stream connection restored"); err != nil {
					return &operationError{operation: "transport recovery observation", cause: err}
				}
			case "transport_error":
				if err := observe(kind, "DingTalk Stream acknowledgement could not be sent; durable receipt and task state are retained"); err != nil {
					return &operationError{operation: "transport error observation", cause: err}
				}
			}
		}
	}
}

func successACK() *payload.DataFrameResponse {
	response := payload.NewSuccessDataFrameResponse()
	response.Message = "OK"
	response.SetData(`{"response":null}`)
	return response
}

func recordFailure(observe func(string, string) error, err error) error {
	if observationErr := observe("error", err.Error()); observationErr != nil {
		return &operationError{operation: "transport failure recording", cause: errors.Join(err, observationErr)}
	}
	return err
}

// safeSDKLogger deliberately ignores all SDK arguments: they can contain app
// secrets, session tickets, message bodies, or credential-bearing URLs. Known
// lifecycle formats become safe recovery events. Unknown errors remain fatal.
type safeSDKLogger struct {
	mu              sync.Mutex
	report          func(error)
	reportTransport func(string)
}

func (l *safeSDKLogger) setReport(report func(error), transport func(string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.report = report
	l.reportTransport = transport
}
func (l *safeSDKLogger) emit(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.report != nil {
		l.report(errors.New(message))
	}
}
func (l *safeSDKLogger) transport(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reportTransport != nil {
		l.reportTransport(kind)
	}
}
func (l *safeSDKLogger) Debugf(format string, _ ...interface{}) {
	if format == "StreamClient.OnDisconnect" {
		l.transport("reconnecting")
	}
}
func (l *safeSDKLogger) Infof(format string, _ ...interface{}) {
	if format == "StreamClient reconnect success" {
		l.transport("reconnected")
	}
}
func (l *safeSDKLogger) Warningf(format string, _ ...interface{}) {
	if strings.Contains(format, "handler capacity reached") {
		l.emit("DingTalk Stream callback capacity was exceeded")
	} else {
		l.emit("DingTalk Stream reported a transport warning")
	}
}
func (l *safeSDKLogger) Errorf(format string, _ ...interface{}) {
	switch format {
	case "connection process read message error: messageType=[%d] message=[%s] error=[%s]",
		"connection process is closed",
		"connection write ping message error: error=[%s]",
		"ping time out, connection is closing",
		"StreamClient reconnect error, attempt=[%d] error=[%s]",
		"StreamClient close. error=[%s]":
		l.transport("reconnecting")
		return
	case "connection processDataFrame send response error: error=[%s]",
		"SendDataFrameResponse error, conn nil or write failed, error=[%s]":
		// A failed ACK write does not itself start the SDK reconnect loop.
		l.transport("transport_error")
		return
	}
	l.emit("DingTalk Stream reported a transport failure")
}
func (l *safeSDKLogger) Fatalf(string, ...interface{}) {
	l.emit("DingTalk Stream reported a fatal transport failure")
}
