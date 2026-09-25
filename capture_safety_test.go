package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

type panicOnceSender struct{ calls atomic.Int32 }

func (sender *panicOnceSender) Send(context.Context, transport.Request) (transport.Response, error) {
	if sender.calls.Add(1) == 1 {
		panic("synthetic sender failure")
	}
	return transport.Response{StatusCode: http.StatusAccepted}, nil
}

type panicOnceConfigFetcher struct{ calls atomic.Int32 }

func (fetcher *panicOnceConfigFetcher) Fetch(context.Context, RemoteConfigRequest) (RemoteConfigResponse, error) {
	if fetcher.calls.Add(1) == 1 {
		panic("synthetic config failure")
	}
	return RemoteConfigResponse{StatusCode: http.StatusNotModified}, nil
}

func TestBackgroundCallbackPanicsDoNotCrashHost(t *testing.T) {
	if mode := os.Getenv("DEBUGBUNDLE_TEST_PANIC_CALLBACK"); mode != "" {
		if mode == "sender" {
			sender := &panicOnceSender{}
			client := New(Config{ProjectToken: "dbundle_proj_test", Transport: sender, BatchSize: 1000, FlushInterval: time.Hour})
			defer client.Close()
			client.CaptureLog(context.Background(), "retained failure", LevelError, nil)
			_ = client.Flush(context.Background())
			client.sendMu.Lock()
			client.sendMu.Unlock()
			client.mu.Lock()
			retained, inFlight := len(client.buffer), client.inFlightCount
			retainedBytes, inFlightBytes := client.bufferBytes, client.inFlightBytes
			client.mu.Unlock()
			if retained != 1 || inFlight != 0 || retainedBytes == 0 || inFlightBytes != 0 {
				t.Fatalf("panic lost or duplicated ownership: pending=%d/%d inflight=%d/%d", retained, retainedBytes, inFlight, inFlightBytes)
			}
			_ = client.Flush(context.Background())
			client.mu.Lock()
			defer client.mu.Unlock()
			if len(client.buffer) != 0 || sender.calls.Load() != 2 {
				t.Fatal("sender failed to recover and retry retained event")
			}
			return
		}
		fetcher := &panicOnceConfigFetcher{}
		client := New(Config{ProjectToken: "dbundle_proj_test", Transport: &recordingTransport{}, RemoteConfigFetcher: fetcher})
		defer client.Close()
		select {
		case <-client.remoteConfigReady:
		case <-time.After(time.Second):
			t.Fatal("panicking initial config did not release readiness")
		}
		client.mu.Lock()
		retryScheduled := client.remoteConfigTimer != nil
		policy := client.capturePolicy
		client.mu.Unlock()
		if !retryScheduled || policy.CaptureLogs != CaptureLogsError {
			t.Fatal("panicking config failed to keep restrictive policy and schedule retry")
		}
		if err := client.RefreshRemoteConfigNow(context.Background()); err != nil || fetcher.calls.Load() != 2 {
			t.Fatal("config fetch failed to recover after panic")
		}
		return
	}
	for _, mode := range []string{"sender", "config"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackgroundCallbackPanicsDoNotCrashHost$")
			command.Env = append(os.Environ(), "DEBUGBUNDLE_TEST_PANIC_CALLBACK="+mode)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("callback panic escaped its SDK subprocess: %v\n%s", err, output)
			}
		})
	}
}

type heldConfigFetcher struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestHookReplacementIsChargedBeforeNextCallback(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var firstBytes atomic.Int64
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 1000, FlushInterval: time.Hour,
		Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}},
		BeforeSend: func(event EventEnvelope) *EventEnvelope {
			if calls.Add(1) == 2 {
				close(entered)
				<-release
			}
			event.Context = map[string]any{"detail": strings.Repeat("x", 4096)}
			event.EventID = "36beb3e1-bf61-4c3c-94ba-42102a9c26c0"
			encoded, _ := json.Marshal(event)
			firstBytes.CompareAndSwap(0, int64(len(encoded)))
			return &event
		}})
	defer client.Close()
	defer close(release)
	client.CaptureLog(context.Background(), "first event", LevelError, nil)
	client.CaptureLog(context.Background(), "second event", LevelError, nil)
	go client.Flush(context.Background())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second hook did not begin")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if int64(client.inFlightBytes) < firstBytes.Load() {
		t.Fatalf("prepared replacement is uncharged while another hook waits: charged=%d prepared=%d", client.inFlightBytes, firstBytes.Load())
	}
	if client.inFlightBytes+client.bufferBytes > maxPendingBytes {
		t.Fatal("hook replacement exceeded combined byte budget")
	}
}

func TestHookGrowthDropsOverflowWithoutRestoringPrivateOriginals(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 1000, FlushInterval: time.Hour,
		RequestTimeout: time.Minute, Transport: recorder, BeforeSend: func(event EventEnvelope) *EventEnvelope {
			event.Payload["message"] = "app redacted"
			event.Context = map[string]any{}
			// JSON escaping expands these bounded strings without an expensive workload fixture.
			for index := 0; index < 16; index++ {
				event.Context[fmt.Sprintf("detail_%d", index)] = strings.Repeat("\x00", 1024)
			}
			return &event
		}})
	defer client.Close()
	for index := 0; index < 100; index++ {
		client.CaptureLog(context.Background(), fmt.Sprintf("private tenant detail %d", index), LevelError, nil)
	}
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 1 {
		t.Fatalf("expected one bounded send, got %d", len(recorder.requests))
	}
	events := recorder.requests[0].Events
	if len(events) == 0 || len(events) >= 100 {
		t.Fatalf("expected overflow to withhold only excess replacements, got %d", len(events))
	}
	bytes := 0
	for _, encoded := range events {
		bytes += len(encoded)
		var event EventEnvelope
		if err := json.Unmarshal(encoded, &event); err != nil || event.Payload["message"] != "app redacted" {
			t.Fatal("overflow bypassed valid application redaction")
		}
	}
	if bytes > maxPendingBytes {
		t.Fatalf("over-budget wire batch: %d", bytes)
	}
}

type hostileError struct {
	entered chan struct{}
	release chan struct{}
}

type countedError struct{ calls atomic.Int64 }

func (errorValue *countedError) Error() string {
	errorValue.calls.Add(1)
	return "error must not be rendered when no capacity exists"
}

func TestAllErrorFullQueueRejectsBeforeExceptionRendering(t *testing.T) {
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 20_000,
		FlushInterval: time.Hour, Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}})
	defer client.Close()
	for index := 0; index < maxPendingEvents; index++ {
		client.CaptureLog(context.Background(), fmt.Sprintf("unique error %d", index), LevelError, nil)
	}
	client.mu.Lock()
	retained := len(client.buffer)
	client.mu.Unlock()
	if retained != maxPendingEvents {
		t.Fatalf("expected a full error queue, got %d", retained)
	}
	errorValue := &countedError{}
	started := time.Now()
	for index := 0; index < 10_000; index++ {
		client.CaptureException(context.Background(), errorValue)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("full all-error queue materially delayed 10,000 exception callers")
	}
	if errorValue.calls.Load() != 0 {
		t.Fatal("full all-error queue rendered an application error before dropping it")
	}
}

func TestFullWarningQueueRetainsRequestFailureAndException(t *testing.T) {
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 20_000,
		FlushInterval: time.Hour, Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}})
	defer client.Close()
	for index := 0; index < maxPendingEvents; index++ {
		client.CaptureLog(context.Background(), fmt.Sprintf("unique warning %d", index), LevelWarning, nil)
	}
	client.CaptureRequest(context.Background(), httptest.NewRequest("GET", "https://example.invalid/failed", nil),
		ResponseInfo{StatusCode: 503})
	client.CaptureException(context.Background(), errors.New("priority exception"))
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.buffer) != maxPendingEvents {
		t.Fatalf("expected bounded queue, got %d", len(client.buffer))
	}
	var sawRequest, sawException bool
	for _, queued := range client.buffer {
		var event EventEnvelope
		if err := json.Unmarshal(queued.encoded, &event); err != nil {
			t.Fatal(err)
		}
		sawRequest = sawRequest || event.EventType == "request_event"
		sawException = sawException || event.EventType == "backend_exception"
	}
	if !sawRequest || !sawException {
		t.Fatalf("queue lost priority incidents: request=%t exception=%t", sawRequest, sawException)
	}
}

func TestRateLimitedWarningBatchRestoresExceptionPriority(t *testing.T) {
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 20_000,
		FlushInterval: time.Hour, RequestTimeout: time.Minute,
		Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusTooManyRequests}}})
	defer client.Close()
	for index := 0; index < maxPendingEvents; index++ {
		client.CaptureLog(context.Background(), fmt.Sprintf("unique warning %d", index), LevelWarning, nil)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.CaptureException(context.Background(), errors.New("priority after 429"))
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.buffer) != maxPendingEvents {
		t.Fatalf("expected restored bounded queue, got %d", len(client.buffer))
	}
	for _, queued := range client.buffer {
		var event EventEnvelope
		if err := json.Unmarshal(queued.encoded, &event); err != nil {
			t.Fatal(err)
		}
		if event.EventType == "backend_exception" {
			return
		}
	}
	t.Fatal("429 retry displaced the later exception")
}

func TestCaptureCallersNeverWaitForAnOccupiedStateLock(t *testing.T) {
	client := New(Config{ProjectToken: "dbundle_proj_test",
		Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}})
	defer client.Close()
	client.mu.Lock()
	finished := make(chan struct{}, 3)
	go func() {
		client.CaptureLog(context.Background(), "warning", LevelWarning, nil)
		finished <- struct{}{}
	}()
	go func() {
		client.CaptureRequest(context.Background(), httptest.NewRequest("GET", "https://example.invalid/failure", nil),
			ResponseInfo{StatusCode: 503})
		finished <- struct{}{}
	}()
	go func() {
		client.CaptureException(context.Background(), errors.New("failure"))
		finished <- struct{}{}
	}()
	for index := 0; index < 3; index++ {
		select {
		case <-finished:
		case <-time.After(250 * time.Millisecond):
			client.mu.Unlock()
			t.Fatal("capture caller waited for another SDK owner")
		}
	}
	client.mu.Unlock()
}

func (errorValue *hostileError) Error() string {
	close(errorValue.entered)
	<-errorValue.release
	return "slow application renderer"
}

func TestExceptionRendererCannotHoldTheCaptureCaller(t *testing.T) {
	errorValue := &hostileError{entered: make(chan struct{}), release: make(chan struct{})}
	client := New(Config{ProjectToken: "dbundle_proj_test",
		Transport: &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}})
	defer client.Close()
	finished := make(chan struct{})
	go func() {
		client.CaptureException(context.Background(), errorValue)
		close(finished)
	}()
	select {
	case <-errorValue.entered:
	case <-time.After(2 * time.Second):
		close(errorValue.release)
		t.Fatal("error renderer was not invoked")
	}
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		close(errorValue.release)
		t.Fatal("exception capture waited for the application error renderer")
	}
	close(errorValue.release)
}

func (fetcher *heldConfigFetcher) Fetch(_ context.Context, _ RemoteConfigRequest) (RemoteConfigResponse, error) {
	fetcher.once.Do(func() { close(fetcher.entered) })
	<-fetcher.release
	return RemoteConfigResponse{}, errors.New("synthetic config outage")
}

func TestConstructorDoesNotWaitForRemoteConfiguration(t *testing.T) {
	fetcher := &heldConfigFetcher{entered: make(chan struct{}), release: make(chan struct{})}
	created := make(chan *Client, 1)
	go func() {
		created <- New(Config{
			ProjectToken:        "dbundle_proj_test",
			Transport:           &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}},
			RemoteConfigFetcher: fetcher,
		})
	}()
	select {
	case <-fetcher.entered:
	case <-time.After(2 * time.Second):
		close(fetcher.release)
		t.Fatal("remote configuration was not requested")
	}
	select {
	case client := <-created:
		client.Close()
	case <-time.After(250 * time.Millisecond):
		close(fetcher.release)
		t.Fatal("constructor waited for remote configuration")
	}
	close(fetcher.release)
}

func TestFilteredInfoBurstDoesNotInvokeHookOrRetainEvents(t *testing.T) {
	var hookCalls atomic.Int64
	client := New(Config{
		ProjectToken:  "dbundle_proj_test",
		BatchSize:     20_000,
		FlushInterval: time.Hour,
		Transport:     &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}},
		BeforeSend: func(event EventEnvelope) *EventEnvelope {
			hookCalls.Add(1)
			return &event
		},
	})
	defer client.Close()
	for index := 0; index < 10_000; index++ {
		client.CaptureLog(context.Background(), "filtered", LevelInfo, map[string]any{"index": index})
	}
	client.mu.Lock()
	retained := len(client.buffer)
	client.mu.Unlock()
	if retained != 0 || hookCalls.Load() != 0 {
		t.Fatalf("filtered INFO burst did work: retained=%d hook_calls=%d", retained, hookCalls.Load())
	}
}

func TestUniqueEventBurstHasFiniteRetainedCapacity(t *testing.T) {
	client := New(Config{
		ProjectToken:  "dbundle_proj_test",
		BatchSize:     20_000,
		FlushInterval: time.Hour,
		Transport:     &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}},
	})
	defer client.Close()
	for index := 0; index < 5_000; index++ {
		client.CaptureMessage(context.Background(), fmt.Sprintf("unique warning %d", index))
	}
	client.mu.Lock()
	retained := len(client.buffer)
	client.mu.Unlock()
	if retained > 1_000 {
		t.Fatalf("retained %d events after finite-capacity burst", retained)
	}
}

func TestQueuePressureProducesOneBoundedAggregateAfterTheFullBatch(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 20_000,
		FlushInterval: time.Hour, RequestTimeout: time.Minute, Transport: recorder})
	defer client.Close()
	for index := 0; index < 1_100; index++ {
		client.CaptureMessage(context.Background(), fmt.Sprintf("pressure warning %d", index))
	}
	_ = client.Flush(context.Background())
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 2 || len(recorder.requests[1].Events) != 1 {
		t.Fatalf("expected one aggregate after the full batch, got %#v", recorder.requests)
	}
	var aggregate EventEnvelope
	if err := json.Unmarshal(recorder.requests[1].Events[0], &aggregate); err != nil {
		t.Fatalf("decode pressure aggregate: %v", err)
	}
	if aggregate.EventType != "error_suppressed" || aggregate.Payload["suppressed_count"] != float64(100) {
		t.Fatalf("unexpected pressure aggregate: %#v", aggregate)
	}
}

type heldSender struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	active  atomic.Int64
	peak    atomic.Int64
}

func (sender *heldSender) Send(_ context.Context, _ transport.Request) (transport.Response, error) {
	count := sender.active.Add(1)
	for {
		peak := sender.peak.Load()
		if count <= peak || sender.peak.CompareAndSwap(peak, count) {
			break
		}
	}
	sender.once.Do(func() { close(sender.entered) })
	<-sender.release
	sender.active.Add(-1)
	return transport.Response{StatusCode: http.StatusAccepted}, nil
}

func TestHeldSenderHasOneInFlightBatchAndCannotGrowTheQueue(t *testing.T) {
	sender := &heldSender{entered: make(chan struct{}), release: make(chan struct{})}
	client := New(Config{ProjectToken: "dbundle_proj_test", BatchSize: 1, Transport: sender})
	defer client.Close()
	client.CaptureMessage(context.Background(), "first")
	select {
	case <-sender.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not start")
	}
	for index := 0; index < 1_100; index++ {
		client.CaptureMessage(context.Background(), fmt.Sprintf("unique warning %d", index))
	}
	client.mu.Lock()
	retained := len(client.buffer) + client.inFlightCount
	client.mu.Unlock()
	if retained > maxPendingEvents {
		t.Fatalf("retained %d events while transport stalled", retained)
	}
	finished := make(chan struct{})
	go func() {
		_ = client.Flush(context.Background())
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("concurrent flush waited for a stalled send")
	}
	if sender.peak.Load() != 1 {
		t.Fatalf("transport concurrency reached %d", sender.peak.Load())
	}
	close(sender.release)
}

func TestSuppressionStateHasFiniteCardinalityAndOneOverflowAggregate(t *testing.T) {
	tracker := newSuppressionTracker()
	now := time.Now().UTC()
	for index := 0; index < 5_000; index++ {
		tracker.ShouldCapture(fmt.Sprintf("unique:%d", index), now)
	}
	if len(tracker.states) > 2_048 {
		t.Fatalf("suppression retained %d unique fingerprints", len(tracker.states))
	}
	aggregates := tracker.PendingAggregates(now)
	if len(aggregates) != 1 || aggregates[0].Suppressed != 5_000-2_048 {
		t.Fatalf("expected one bounded overflow report, got %#v", aggregates)
	}
}
