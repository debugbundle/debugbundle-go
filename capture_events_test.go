package debugbundle

import (
	"context"
	"errors"
	"fmt"
	"github.com/debugbundle/debugbundle-go/v3/transport"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeLoggedErrorKeepsCauseInsteadOfGenericLog(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, BatchSize: 1000, FlushInterval: time.Hour})
	defer func() { _ = client.Close() }()
	client.CaptureLog(context.Background(), "operation failed", LevelError,
		map[string]any{"error": fmt.Errorf("synthetic outer: %w", errors.New("synthetic inner"))})
	client.CaptureLog(context.Background(), "ordinary error", LevelError, nil)
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatal("expected one exception and one ordinary log")
	}
	if raw := string(recorder.requests[0].Events[0]); !strings.Contains(raw, `"event_type":"backend_exception"`) || !strings.Contains(raw, "synthetic inner") {
		t.Fatalf("native log lost exception evidence: %s", raw)
	}
}

func TestCaptureEntryPointsRetainContextAndHandleInvalidMetadata(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, BatchSize: 1000, FlushInterval: time.Hour})
	defer func() { _ = client.Close() }()
	client.SetContext("", "ignored")
	client.SetContext("fixture", "retain")
	client.SetContext("drop", "delete")
	client.SetContext("drop", nil)
	client.SetContext("invalid", func() {})
	client.CaptureLog(context.Background(), "", LevelWarning, nil)
	client.CaptureMessage(context.Background(), "")
	client.CaptureMessage(context.Background(), "fixture-message", WithEventContext(map[string]any{"technical": "fixture"}))
	client.CaptureLog(context.Background(), "fixture-log", LevelWarning, map[string]any{"safe": true})
	request := httptest.NewRequest("GET", "https://app.example.test/fixture", nil)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace")
	request.Header.Set("X-Request-Id", "request")
	client.CaptureRequest(context.Background(), request, ResponseInfo{StatusCode: 500})
	client.CaptureRequest(context.Background(), nil, ResponseInfo{})
	client.mu.Lock()
	client.shouldCaptureRequestMetadata(500, "/fixture", "GET")
	client.mu.Unlock()
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 4 {
		t.Fatal("capture entrypoints lost valid data or pressure aggregate")
	}
	for _, raw := range recorder.requests[0].Events {
		if strings.Contains(string(raw), `"invalid"`) || strings.Contains(string(raw), `"drop"`) {
			t.Fatal("invalid/deleted context retained")
		}
	}
}
