package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
)

type recordingTransport struct {
	requests []transport.Request
	response transport.Response
	err      error
}

func (transport *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	transport.requests = append(transport.requests, request)
	if transport.response.StatusCode == 0 {
		transport.response.StatusCode = http.StatusAccepted
	}
	return transport.response, transport.err
}

func TestRelayRejectsWrongOrigin(t *testing.T) {
	handler := NewHandler(nil, Options{AllowedOrigins: []string{"https://app.example.test"}})
	request := httptest.NewRequest(http.MethodPost, "https://api.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden response, got %d", response.Code)
	}
}

func TestRelayRejectsUnsupportedContentType(t *testing.T) {
	handler := NewHandler(nil, Options{})
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request response, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestRelayRateLimitDoesNotTrustForwardedForByDefault(t *testing.T) {
	handler := NewHandler(nil, Options{RateLimitPerMinute: 1})
	first := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	first.Header.Set("Content-Type", "application/json")
	first.Header.Set("Origin", "https://app.example.test")
	first.Header.Set("X-Forwarded-For", "198.51.100.10")
	first.RemoteAddr = "203.0.113.1:12345"
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf("expected first relay request to pass, got %d", firstResponse.Code)
	}
	second := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	second.Header.Set("Content-Type", "application/json")
	second.Header.Set("Origin", "https://app.example.test")
	second.Header.Set("X-Forwarded-For", "198.51.100.11")
	second.RemoteAddr = "203.0.113.1:12345"
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second relay request to use remote address for rate limiting, got %d", secondResponse.Code)
	}
}

func TestRelayRateLimitTrustsForwardedForWhenConfigured(t *testing.T) {
	handler := NewHandler(nil, Options{RateLimitPerMinute: 1, TrustForwardedHeader: true})
	first := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	first.Header.Set("Content-Type", "application/json")
	first.Header.Set("Origin", "https://app.example.test")
	first.Header.Set("X-Forwarded-For", "198.51.100.10")
	first.RemoteAddr = "203.0.113.1:12345"
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf("expected first relay request to pass, got %d", firstResponse.Code)
	}
	second := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[]}`))
	second.Header.Set("Content-Type", "application/json")
	second.Header.Set("Origin", "https://app.example.test")
	second.Header.Set("X-Forwarded-For", "198.51.100.11")
	second.RemoteAddr = "203.0.113.1:12345"
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusAccepted {
		t.Fatalf("expected trusted forwarded IPs to receive separate limits, got %d", secondResponse.Code)
	}
}

func TestRelayAcceptsBrowserBatchAndForcesSDKName(t *testing.T) {
	directory := t.TempDir()
	handler := NewHandler(nil, Options{
		ProjectMode:    debugbundle.ProjectModeLocalOnly,
		LocalEventsDir: directory,
	})
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[{"schema_version":"2026-03-01","event_id":"00000000-0000-4000-8000-000000000501","event_type":"frontend_exception","occurred_at":"2026-05-23T12:00:00Z","sdk_name":"spoofed-browser-sdk","sdk_version":"1.2.3","service":{"name":"checkout-web","runtime":"browser","framework":"react","environment":"production"},"payload":{"message":"boom"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if err != nil {
		t.Fatalf("glob relay files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one relay event file, got %d", len(files))
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read relay file: %v", err)
	}
	var written []map[string]any
	if err := json.Unmarshal(content, &written); err != nil {
		t.Fatalf("unmarshal relay file: %v", err)
	}
	if written[0]["sdk_name"] != browserSDKName {
		t.Fatalf("expected relay to force canonical browser sdk name, got %#v", written[0]["sdk_name"])
	}
	service := written[0]["service"].(map[string]any)
	if service["name"] != "checkout-web" {
		t.Fatalf("expected browser service name to be preserved, got %#v", service["name"])
	}
	if service["environment"] != "production" {
		t.Fatalf("expected browser environment to be preserved, got %#v", service["environment"])
	}
	if service["runtime"] != "browser" {
		t.Fatalf("expected browser runtime to be preserved, got %#v", service["runtime"])
	}
	if service["framework"] != "react" {
		t.Fatalf("expected browser framework to be preserved, got %#v", service["framework"])
	}
}

func TestRelayRejectsEventWithMissingRequiredFields(t *testing.T) {
	handler := NewHandler(nil, Options{})
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[{"event_type":"frontend_exception","sdk_name":"@debugbundle/sdk-browser","sdk_version":"1.2.3","occurred_at":"2026-05-23T12:00:00Z","service":{"name":"checkout-web","environment":"production"},"payload":{"message":"boom"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request response, got %d body=%s", response.Code, response.Body.String())
	}
	var body responseBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal relay response: %v", err)
	}
	if body.Accepted != 0 || body.Rejected != 1 {
		t.Fatalf("expected one rejected event, got accepted=%d rejected=%d", body.Accepted, body.Rejected)
	}
	if len(body.Errors) != 1 || body.Errors[0] != "batch[0]: Invalid browser relay event payload." {
		t.Fatalf("expected canonical invalid-payload error, got %#v", body.Errors)
	}
}

func TestRelayRejectsOversizedBodyWith413(t *testing.T) {
	handler := NewHandler(nil, Options{MaxBodyBytes: 10})
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(strings.Repeat("x", 11)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected payload too large response, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestRelayReportsCanonicalMixedBatchErrors(t *testing.T) {
	handler := NewHandler(nil, Options{})
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/debugbundle/browser", strings.NewReader(`{"batch":[{"schema_version":"2026-03-01","event_id":"00000000-0000-4000-8000-000000000502","event_type":"frontend_exception","occurred_at":"2026-05-23T12:00:00Z","sdk_name":"@debugbundle/sdk-browser","sdk_version":"1.2.3","service":{"name":"checkout-web","environment":"production"},"payload":{"message":"ok"}},{"schema_version":"2026-03-01","event_id":"00000000-0000-4000-8000-000000000503","event_type":"backend_exception","occurred_at":"2026-05-23T12:00:01Z","sdk_name":"@debugbundle/sdk-node","sdk_version":"1.2.3","service":{"name":"checkout-api","environment":"production"},"payload":{"message":"boom"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected mixed batch to return bad request, got %d body=%s", response.Code, response.Body.String())
	}
	var body responseBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal relay response: %v", err)
	}
	if body.Accepted != 1 || body.Rejected != 1 {
		t.Fatalf("expected one accepted and one rejected event, got accepted=%d rejected=%d", body.Accepted, body.Rejected)
	}
	if len(body.Errors) != 1 || body.Errors[0] != "batch[1]: Unsupported browser relay event type backend_exception." {
		t.Fatalf("expected canonical mixed-batch error, got %#v", body.Errors)
	}
}

func TestRelayStripsCredentialsAndWritesLocalBatch(t *testing.T) {
	directory := t.TempDir()
	handler := NewHandler(nil, Options{
		AllowedOrigins: []string{"https://app.example.test"},
		ProjectMode:    debugbundle.ProjectModeLocalOnly,
		LocalEventsDir: directory,
		Service:        "web",
		Environment:    "test",
	})
	body := map[string]any{
		"batch": []map[string]any{
			{
				"schema_version": "2026-03-01",
				"event_id":       "00000000-0000-4000-8000-000000000601",
				"event_type":     "frontend_exception",
				"occurred_at":    "2026-05-23T12:00:00Z",
				"sdk_name":       "@debugbundle/sdk-browser",
				"sdk_version":    "0.1.0",
				"service": map[string]any{
					"name":        "web-app",
					"environment": "production",
				},
				"project_token": "browser-token",
				"payload": map[string]any{
					"message":       "boom",
					"authorization": "Bearer no",
					"headers": map[string]any{
						"authorization": "Bearer still-no",
					},
				},
				"correlation": map[string]any{
					"trace_id":     "trace-1",
					"request_id":   "request-1",
					"session_id":   "session-1",
					"user_id_hash": "user-1",
					"organization": "drop-me",
				},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal relay request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://api.example.test/debugbundle/browser", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if err != nil {
		t.Fatalf("glob relay files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one relay event file, got %d", len(files))
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read relay file: %v", err)
	}
	var written []map[string]any
	if err := json.Unmarshal(content, &written); err != nil {
		t.Fatalf("unmarshal relay file: %v", err)
	}
	payload := written[0]["payload"].(map[string]any)
	if _, exists := payload["authorization"]; exists {
		t.Fatalf("expected authorization to be stripped from payload")
	}
	headers := payload["headers"].(map[string]any)
	if _, exists := headers["authorization"]; exists {
		t.Fatalf("expected authorization header to be stripped from relay payload")
	}
	correlation := written[0]["correlation"].(map[string]any)
	if _, exists := correlation["organization"]; exists {
		t.Fatalf("expected unsupported correlation fields to be stripped")
	}
	service := written[0]["service"].(map[string]any)
	if service["name"] != "web" {
		t.Fatalf("expected relay service override, got %#v", service["name"])
	}
	if service["environment"] != "test" {
		t.Fatalf("expected relay environment override, got %#v", service["environment"])
	}
	if _, exists := written[0]["environment"]; exists {
		t.Fatalf("expected relay to keep environment inside the service object")
	}
}

func TestRelayConnectedDurableWritesSpoolForwardsAndMarksDelivered(t *testing.T) {
	spoolDir := t.TempDir()
	forwarder := &recordingTransport{}
	handler := NewHandler(nil, Options{
		AllowedOrigins: []string{"https://app.example.test"},
		ProjectMode:    debugbundle.ProjectModeConnected,
		ProjectToken:   "dbundle_proj_test",
		Endpoint:       "https://api.example.test/v1/events",
		SpoolDir:       spoolDir,
		DurableWrite:   true,
		Transport:      forwarder,
	})
	body := map[string]any{
		"batch": []map[string]any{{
			"schema_version": "2026-03-01",
			"event_id":       "00000000-0000-4000-8000-000000000602",
			"event_type":     "request_event",
			"occurred_at":    "2026-05-23T12:00:00Z",
			"sdk_name":       "@debugbundle/sdk-browser",
			"sdk_version":    "0.1.0",
			"service": map[string]any{
				"name":        "web-app",
				"environment": "production",
			},
			"payload": map[string]any{"message": "ok"},
		}}}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal relay request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://api.example.test/debugbundle/browser", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	spoolFiles, err := filepath.Glob(filepath.Join(spoolDir, "*.events.json"))
	if err != nil {
		t.Fatalf("glob spool files: %v", err)
	}
	if len(spoolFiles) != 1 {
		t.Fatalf("expected one spool file, got %d", len(spoolFiles))
	}
	if _, err := os.Stat(spoolFiles[0] + ".delivered"); err != nil {
		t.Fatalf("expected delivered marker file: %v", err)
	}
	if len(forwarder.requests) != 1 {
		t.Fatalf("expected one forward request, got %#v", forwarder.requests)
	}
	if forwarder.requests[0].ProjectToken != "dbundle_proj_test" {
		t.Fatalf("expected server-side project token on forward request, got %#v", forwarder.requests[0].ProjectToken)
	}
	var forwarded map[string]any
	if err := json.Unmarshal(forwarder.requests[0].Events[0], &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded event: %v", err)
	}
	service := forwarded["service"].(map[string]any)
	if service["name"] != "web-app" {
		t.Fatalf("expected browser service metadata to be preserved, got %#v", service["name"])
	}
	if service["environment"] != "production" {
		t.Fatalf("expected browser environment metadata to be preserved, got %#v", service["environment"])
	}
}

func TestRelayWithClientKeepsBrowserServiceIdentityByDefault(t *testing.T) {
	forwarder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_test",
		Service:      "api-service",
		Environment:  "production",
		Transport:    forwarder,
	})
	defer func() {
		_ = client.Close()
	}()

	handler := NewHandler(client, Options{
		AllowedOrigins: []string{"https://app.example.test"},
		ProjectMode:    debugbundle.ProjectModeConnected,
		ProjectToken:   "dbundle_proj_test",
		Endpoint:       "https://api.example.test/v1/events",
		Transport:      forwarder,
	})

	request := httptest.NewRequest(http.MethodPost, "https://api.example.test/debugbundle/browser", strings.NewReader(`{"batch":[{"schema_version":"2026-03-01","event_id":"00000000-0000-4000-8000-000000000612","event_type":"frontend_exception","occurred_at":"2026-05-23T12:00:00Z","sdk_name":"@debugbundle/sdk-browser","sdk_version":"0.1.0","service":{"name":"web-app","environment":"test","runtime":"browser"},"payload":{"message":"ok"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	if len(forwarder.requests) != 1 {
		t.Fatalf("expected one forwarded request, got %#v", forwarder.requests)
	}

	var forwarded map[string]any
	if err := json.Unmarshal(forwarder.requests[0].Events[0], &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded event: %v", err)
	}
	service := forwarded["service"].(map[string]any)
	if service["name"] != "web-app" {
		t.Fatalf("expected browser service name to be preserved, got %#v", service["name"])
	}
	if service["environment"] != "test" {
		t.Fatalf("expected browser environment to be preserved, got %#v", service["environment"])
	}
}

func TestRelayConnectedDurableRetainsSpoolWhenForwardFails(t *testing.T) {
	spoolDir := t.TempDir()
	forwarder := &recordingTransport{response: transport.Response{StatusCode: http.StatusInternalServerError}}
	handler := NewHandler(nil, Options{
		AllowedOrigins: []string{"https://app.example.test"},
		ProjectMode:    debugbundle.ProjectModeConnected,
		ProjectToken:   "dbundle_proj_test",
		Endpoint:       "https://api.example.test/v1/events",
		SpoolDir:       spoolDir,
		DurableWrite:   true,
		Transport:      forwarder,
	})
	request := httptest.NewRequest(http.MethodPost, "https://api.example.test/debugbundle/browser", strings.NewReader(`{"batch":[{"schema_version":"2026-03-01","event_id":"00000000-0000-4000-8000-000000000603","event_type":"request_event","occurred_at":"2026-05-23T12:00:00Z","sdk_name":"@debugbundle/sdk-browser","sdk_version":"0.1.0","service":{"name":"web-app","environment":"production"},"payload":{"message":"ok"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected durable relay to accept once spooled, got %d body=%s", response.Code, response.Body.String())
	}
	spoolFiles, err := filepath.Glob(filepath.Join(spoolDir, "*.events.json"))
	if err != nil {
		t.Fatalf("glob spool files: %v", err)
	}
	if len(spoolFiles) != 1 {
		t.Fatalf("expected one retained spool file, got %d", len(spoolFiles))
	}
	if _, err := os.Stat(spoolFiles[0] + ".delivered"); !os.IsNotExist(err) {
		t.Fatalf("expected no delivered marker on forward failure, got %v", err)
	}
	if len(forwarder.requests) != 1 {
		t.Fatalf("expected forward attempt before retention, got %#v", forwarder.requests)
	}
}
