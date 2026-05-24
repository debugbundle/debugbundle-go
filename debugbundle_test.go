package debugbundle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/transport"
)

type recordingTransport struct {
	requests []transport.Request
	response transport.Response
	err      error
}

func (transport *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	transport.requests = append(transport.requests, request)
	return transport.response, transport.err
}

type fakeRemoteConfigFetcher struct {
	responses []RemoteConfigResponse
	err       error
	requests  []RemoteConfigRequest
}

func (fetcher *fakeRemoteConfigFetcher) Fetch(_ context.Context, request RemoteConfigRequest) (RemoteConfigResponse, error) {
	fetcher.requests = append(fetcher.requests, request)
	if fetcher.err != nil {
		return RemoteConfigResponse{}, fetcher.err
	}
	if len(fetcher.responses) == 0 {
		return RemoteConfigResponse{StatusCode: http.StatusInternalServerError}, nil
	}
	response := fetcher.responses[0]
	if len(fetcher.responses) > 1 {
		fetcher.responses = fetcher.responses[1:]
	}
	return response, nil
}

func TestCaptureExceptionFlushesRedactedPayload(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{
		ProjectToken: "dbundle_proj_test",
		Environment:  "test",
		Service:      "payments",
		Transport:    recorder,
	})
	ctx := ContextWithTraceID(context.Background(), "trace-123")
	ctx = ContextWithValue(ctx, "authorization", "Bearer secret")
	client.Probe(ctx, "checkout.tax", map[string]any{"authorization": "Bearer probe-secret", "rate": 0.2})
	client.CaptureException(ctx, errors.New("boom"), WithEventContext(map[string]any{"password": "super-secret"}))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected a single batch, got %d", len(recorder.requests))
	}
	if len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one event, got %d", len(recorder.requests[0].Events))
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.SchemaVersion != defaultSchemaVersion {
		t.Fatalf("expected schema version %q, got %q", defaultSchemaVersion, event.SchemaVersion)
	}
	if event.EventID == "" {
		t.Fatalf("expected generated event id")
	}
	if event.Service.Name != "payments" || event.Service.Runtime != "go" || event.Service.Environment != "test" {
		t.Fatalf("unexpected service descriptor %#v", event.Service)
	}
	if event.Correlation["trace_id"] != "trace-123" {
		t.Fatalf("expected trace id to propagate, got %#v", event.Correlation["trace_id"])
	}
	if event.EventType != "backend_exception" {
		t.Fatalf("unexpected event type %q", event.EventType)
	}
	if event.Payload["handled"] != true {
		t.Fatalf("expected handled exception payload, got %#v", event.Payload["handled"])
	}
	probeData, ok := event.Payload["probe_data"].(map[string]any)
	if !ok {
		t.Fatalf("expected default probe flush payload, got %#v", event.Payload["probe_data"])
	}
	if probeData["version"].(float64) != 1 {
		t.Fatalf("expected probe payload version 1, got %#v", probeData["version"])
	}
	items := probeData["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one flushed probe item, got %#v", items)
	}
	probeItem := items[0].(map[string]any)
	if probeItem["label"] != "checkout.tax" {
		t.Fatalf("expected probe label checkout.tax, got %#v", probeItem["label"])
	}
	if probeItem["activation_id"] != nil {
		t.Fatalf("expected always-on probe flush to use nil activation id, got %#v", probeItem["activation_id"])
	}
	if probeItem["timestamp"] == "" {
		t.Fatalf("expected probe timestamp, got %#v", probeItem["timestamp"])
	}
	probeFields := probeItem["data"].(map[string]any)
	if probeFields["authorization"] != "[REDACTED]" {
		t.Fatalf("expected probe data to be redacted, got %#v", probeFields["authorization"])
	}
	if client.Status() != StatusHealthy {
		t.Fatalf("expected healthy status, got %q", client.Status())
	}
}

func TestCaptureExceptionCanDisableProbeFlushOnError(t *testing.T) {
	flushOnError := false
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{
		ProjectToken:      "dbundle_proj_test",
		Environment:       "test",
		Service:           "payments",
		Transport:         recorder,
		ProbeFlushOnError: &flushOnError,
	})
	ctx := context.Background()
	client.Probe(ctx, "checkout.tax", map[string]any{"rate": 0.2})
	client.CaptureException(ctx, errors.New("boom"))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if _, exists := event.Payload["probe_data"]; exists {
		t.Fatalf("expected probe flush payload to stay disabled, got %#v", event.Payload["probe_data"])
	}
}

func TestDisabledConfigDoesNotCaptureWithProjectToken(t *testing.T) {
	enabled := false
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{
		Enabled:      &enabled,
		ProjectToken: "dbundle_proj_test",
		Transport:    recorder,
	})
	client.CaptureException(context.Background(), errors.New("boom"))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected disabled SDK to emit no transport requests, got %#v", recorder.requests)
	}
	if client.Status() != StatusDisconnected {
		t.Fatalf("expected disabled SDK to stay disconnected, got %q", client.Status())
	}
	if client.LastEventAt() != nil {
		t.Fatalf("expected disabled SDK to have no last event timestamp")
	}
}

func TestDuplicateSuppressionEmitsAggregate(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	for index := 0; index < 6; index++ {
		client.CaptureException(context.Background(), errors.New("repeat"))
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(recorder.requests))
	}
	if len(recorder.requests[0].Events) != 4 {
		t.Fatalf("expected first three events plus aggregate, got %d", len(recorder.requests[0].Events))
	}
	var aggregate EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[3], &aggregate); err != nil {
		t.Fatalf("unmarshal aggregate: %v", err)
	}
	if aggregate.EventType != "error_suppressed" {
		t.Fatalf("expected suppression aggregate, got %q", aggregate.EventType)
	}
	if aggregate.Payload["suppressed_count"].(float64) != 3 {
		t.Fatalf("expected 3 suppressed events, got %#v", aggregate.Payload["suppressed_count"])
	}
}

func TestRetryableFlushWithoutRetryAfterUsesDefaultBackoff(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusInternalServerError}}
	client := New(Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	client.CaptureMessage(context.Background(), "retry later")
	beforeFlush := time.Now()
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	client.mu.Lock()
	retryUntil := client.retryUntil
	buffered := len(client.buffer)
	status := client.status
	failures := client.failures
	client.mu.Unlock()
	if !retryUntil.After(beforeFlush) {
		t.Fatalf("expected default retry backoff after retryable response, got %v", retryUntil)
	}
	if buffered != 1 {
		t.Fatalf("expected retryable event to remain buffered, got %d", buffered)
	}
	if status != StatusDegraded || failures != 1 {
		t.Fatalf("expected degraded status and one failure, got status=%q failures=%d", status, failures)
	}
}

func TestRequestCaptureUsesIncomingHeaders(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	request := httptest.NewRequest(http.MethodPost, "https://example.test/checkout", nil)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace-id")
	request.Header.Set("X-Request-Id", "request-id")
	client.CaptureRequest(context.Background(), request, ResponseInfo{StatusCode: 500, Duration: 1250 * time.Millisecond, Route: "/checkout"})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal request event: %v", err)
	}
	if event.Correlation["trace_id"] != "trace-id" {
		t.Fatalf("expected request trace id, got %#v", event.Correlation["trace_id"])
	}
	if event.Correlation["request_id"] != "request-id" {
		t.Fatalf("expected request correlation id, got %#v", event.Correlation["request_id"])
	}
	if event.Payload["response_status"].(float64) != 500 {
		t.Fatalf("expected response status 500, got %#v", event.Payload["response_status"])
	}
}

func TestProjectModeLocalOnlyDefaultsToFileTransport(t *testing.T) {
	directory := t.TempDir()
	client := New(Config{
		ProjectToken:   "dbundle_proj_test",
		ProjectMode:    ProjectModeLocalOnly,
		LocalEventsDir: directory,
	})
	client.CaptureMessage(context.Background(), "local event")
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if err != nil {
		t.Fatalf("glob local files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one local file, got %d", len(matches))
	}
}

func TestRemoteConfigAppliesCapturePolicyToLogsAndRequests(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{responses: []RemoteConfigResponse{{
		StatusCode: http.StatusOK,
		Body: []byte(`{
			"probes_enabled": true,
			"remote_probes_enabled": true,
			"active_probes": [],
			"poll_interval_ms": 60000,
			"capture_policy": {
				"preset": "balanced",
				"capture_logs": "off",
				"capture_request_events": "all",
				"capture_breadcrumbs": "exception_only",
				"capture_probe_events": "buffer_only",
				"immediate_client_error_statuses": []
			}
		}`),
		ETag: `"cfg-v1"`,
	}}}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})
	client.CaptureLog(context.Background(), "suppressed warning", LevelWarning, nil)
	request := httptest.NewRequest(http.MethodGet, "https://example.test/orders", nil)
	client.CaptureRequest(context.Background(), request, ResponseInfo{StatusCode: 200})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush remote-config filtered events: %v", err)
	}
	if len(fetcher.requests) != 1 {
		t.Fatalf("expected a single init config fetch, got %d", len(fetcher.requests))
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected only the request event to survive policy filtering")
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal remote-config request event: %v", err)
	}
	if event.EventType != "request_event" {
		t.Fatalf("expected request_event after remote config filtering, got %q", event.EventType)
	}
}

func TestFailedInitRemoteConfigFallsBackToMinimalPolicy(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{err: errors.New("config refresh failed")}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})
	defer func() { _ = client.Close() }()
	client.CaptureLog(context.Background(), "warning blocked", LevelWarning, nil)
	client.CaptureLog(context.Background(), "error kept", LevelError, nil)
	okRequest := httptest.NewRequest(http.MethodGet, "https://example.test/health", nil)
	boomRequest := httptest.NewRequest(http.MethodPost, "https://example.test/checkout", nil)
	client.CaptureRequest(context.Background(), okRequest, ResponseInfo{StatusCode: 200})
	client.CaptureRequest(context.Background(), boomRequest, ResponseInfo{StatusCode: 503})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush minimal fallback events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected minimal fallback to keep one error log and one 5xx request event")
	}
	client.mu.Lock()
	hasRetryTimer := client.remoteConfigTimer != nil
	client.mu.Unlock()
	if !hasRetryTimer {
		t.Fatalf("expected failed init refresh to schedule a retry timer")
	}
	var first EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &first); err != nil {
		t.Fatalf("unmarshal first fallback event: %v", err)
	}
	if first.EventType != "log_event" || first.Payload["message"] != "error kept" {
		t.Fatalf("unexpected first fallback event %#v", first)
	}
}

func TestFailedInitRemoteConfigKeepsAlwaysOnProbeBuffers(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{err: errors.New("config refresh failed")}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})
	defer func() { _ = client.Close() }()
	client.Probe(context.Background(), "checkout.cart", map[string]any{"items": 3})
	client.CaptureException(context.Background(), errors.New("boom"))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush fallback probe event: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one exception event with always-on probe data")
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal fallback probe event: %v", err)
	}
	probeData, ok := event.Payload["probe_data"].(map[string]any)
	if !ok {
		t.Fatalf("expected probe_data after failed remote config, got %#v", event.Payload["probe_data"])
	}
	items := probeData["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one buffered probe after failed config, got %#v", items)
	}
}

func TestFailedRemoteConfigRefreshSchedulesFallbackRetry(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{responses: []RemoteConfigResponse{
		{
			StatusCode: http.StatusOK,
			Body: []byte(`{
				"probes_enabled": true,
				"remote_probes_enabled": true,
				"active_probes": [],
				"poll_interval_ms": 60000,
				"capture_policy": {
					"preset": "balanced",
					"capture_logs": "warning",
					"capture_request_events": "failures_only",
					"capture_breadcrumbs": "exception_only",
					"capture_probe_events": "buffer_only",
					"immediate_client_error_statuses": []
				}
			}`),
			ETag: `"cfg-v1"`,
		},
		{StatusCode: http.StatusInternalServerError},
	}}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})
	defer func() { _ = client.Close() }()

	client.mu.Lock()
	if client.remoteConfigTimer != nil {
		client.remoteConfigTimer.Stop()
		client.remoteConfigTimer = nil
	}
	client.mu.Unlock()

	if err := client.RefreshRemoteConfigNow(context.Background()); err == nil {
		t.Fatalf("expected follow-up refresh failure")
	}
	client.mu.Lock()
	hasRetryTimer := client.remoteConfigTimer != nil
	client.mu.Unlock()
	if !hasRetryTimer {
		t.Fatalf("expected failed refresh to schedule a fallback retry timer")
	}
}

func TestRemoteProbeActivationShipsStandaloneProbeEventAndUsesETag(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{responses: []RemoteConfigResponse{
		{
			StatusCode: http.StatusOK,
			Body: []byte(`{
				"probes_enabled": true,
				"remote_probes_enabled": true,
				"active_probes": [{
					"id": "550e8400-e29b-41d4-a716-446655440000",
					"label_pattern": "checkout.*",
					"service": "checkout-api",
					"environment": "production",
					"expires_at": "2099-11-14T22:13:30Z"
				}],
				"poll_interval_ms": 60000,
				"capture_policy": {
					"preset": "balanced",
					"capture_logs": "warning",
					"capture_request_events": "failures_only",
					"capture_breadcrumbs": "exception_only",
					"capture_probe_events": "standalone_when_activated",
					"immediate_client_error_statuses": []
				}
			}`),
			ETag: `"cfg-v1"`,
		},
		{StatusCode: http.StatusNotModified, ETag: `"cfg-v1"`},
	}}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})
	invocations := 0
	client.ProbeLazy(context.Background(), "checkout.tax", func() any {
		invocations++
		return map[string]any{"tax_rate": 0.2}
	}, WithHeavyProbe())
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush activated probe: %v", err)
	}
	if invocations != 1 {
		t.Fatalf("expected heavy probe to execute once when remotely activated, got %d", invocations)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected a single standalone probe event")
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal probe event: %v", err)
	}
	if event.EventType != "probe_event" {
		t.Fatalf("expected probe_event, got %q", event.EventType)
	}
	if event.Payload["activation_id"] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("unexpected activation id %#v", event.Payload["activation_id"])
	}
	if err := client.RefreshRemoteConfigNow(context.Background()); err != nil {
		t.Fatalf("refresh remote config with etag: %v", err)
	}
	if len(fetcher.requests) != 2 || fetcher.requests[1].IfNoneMatch != `"cfg-v1"` {
		t.Fatalf("expected second refresh to send stored etag, got %#v", fetcher.requests)
	}
}

func TestContextForRequestActivatesTriggerTokenForSingleRequest(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	fetcher := &fakeRemoteConfigFetcher{responses: []RemoteConfigResponse{{
		StatusCode: http.StatusOK,
		Body: []byte(`{
			"probes_enabled": true,
			"remote_probes_enabled": true,
			"active_probes": [],
			"poll_interval_ms": 60000,
			"trigger_token_key": "trigger-key-123",
			"capture_policy": {
				"preset": "balanced",
				"capture_logs": "warning",
				"capture_request_events": "all",
				"capture_breadcrumbs": "local_only",
				"capture_probe_events": "standalone_when_activated",
				"immediate_client_error_statuses": []
			}
		}`),
		ETag: `"cfg-trigger"`,
	}}}
	client := New(Config{
		ProjectToken:        "dbundle_proj_test",
		Environment:         "production",
		Service:             "checkout-api",
		Transport:           recorder,
		RemoteConfigFetcher: fetcher,
	})

	expiredQueryToken := createTriggerToken(t, "trigger-key-123", `{"activation_id":"11111111-1111-4111-8111-111111111111","label_pattern":"checkout.*","service":"checkout-api","environment":"production","trigger_expires_at":"2020-03-14T00:00:00Z"}`)
	validHeaderToken := createTriggerToken(t, "trigger-key-123", `{"activation_id":"22222222-2222-4222-8222-222222222222","label_pattern":"checkout.*","service":"checkout-api","environment":"production","trigger_expires_at":"2036-03-20T00:00:00Z"}`)

	request := httptest.NewRequest(http.MethodGet, "https://example.test/checkout?_debug_probe="+expiredQueryToken, nil)
	request.Header.Set("X-DebugBundle-Probe-Trigger", validHeaderToken)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace-123")
	request.Header.Set("X-Request-Id", "req-123")

	invocations := 0
	client.ProbeLazy(client.ContextForRequest(request), "checkout.tax", func() any {
		invocations++
		return map[string]any{"total": 42}
	}, WithHeavyProbe())
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush request-scoped trigger event: %v", err)
	}
	if invocations != 1 {
		t.Fatalf("expected heavy probe to run exactly once for trigger-bearing request, got %d", invocations)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one standalone probe event, got %#v", recorder.requests)
	}
	var event EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal request-scoped trigger event: %v", err)
	}
	if event.EventType != "probe_event" {
		t.Fatalf("expected probe_event, got %q", event.EventType)
	}
	if event.Payload["activation_id"] != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("expected header token activation id, got %#v", event.Payload["activation_id"])
	}
	if event.Correlation["trace_id"] != "trace-123" {
		t.Fatalf("expected trace correlation, got %#v", event.Correlation["trace_id"])
	}
	if event.Correlation["request_id"] != "req-123" {
		t.Fatalf("expected request correlation, got %#v", event.Correlation["request_id"])
	}

	secondRequest := httptest.NewRequest(http.MethodGet, "https://example.test/checkout", nil)
	secondRequest.Header.Set("X-DebugBundle-Trace-Id", "trace-456")
	secondRequest.Header.Set("X-Request-Id", "req-456")
	client.ProbeLazy(client.ContextForRequest(secondRequest), "checkout.tax", func() any {
		invocations++
		return map[string]any{"total": 43}
	}, WithHeavyProbe())
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush second request trigger event: %v", err)
	}
	if invocations != 1 {
		t.Fatalf("expected trigger scope to reset after the request, got %d invocations", invocations)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected no additional standalone probe batches after scope reset, got %#v", recorder.requests)
	}
}

func createTriggerToken(t *testing.T, key string, payloadJSON string) string {
	t.Helper()
	payloadSegment := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	mac := hmac.New(sha256.New, []byte(key))
	if _, err := mac.Write([]byte(payloadSegment)); err != nil {
		t.Fatalf("sign trigger token: %v", err)
	}
	signatureSegment := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "dbundle_probe_" + payloadSegment + "." + signatureSegment
}
