package debugbundlehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
)

type recordingTransport struct {
	requests []transport.Request
}

func (recorder *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	recorder.requests = append(recorder.requests, request)
	return transport.Response{StatusCode: http.StatusAccepted}, nil
}

func TestMiddlewareCapturesRequest(t *testing.T) {
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	handler := Middleware(client, Options{RoutePattern: func(*http.Request) string { return "/checkout" }})(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	request := httptest.NewRequest(http.MethodPost, "https://example.test/checkout", nil)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace-456")
	request.Header.Set("X-Request-Id", "request-456")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush middleware capture: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one captured request event")
	}
	var event debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal request event: %v", err)
	}
	if event.Correlation["trace_id"] != "trace-456" {
		t.Fatalf("expected propagated trace id, got %#v", event.Correlation["trace_id"])
	}
	if event.Payload["route_template"] != "/checkout" {
		t.Fatalf("expected route capture, got %#v", event.Payload["route_template"])
	}
	if event.Correlation["request_id"] != "request-456" {
		t.Fatalf("expected propagated request id, got %#v", event.Correlation["request_id"])
	}
	if event.Payload["response_status"].(float64) != http.StatusBadGateway {
		t.Fatalf("expected failure status capture, got %#v", event.Payload["response_status"])
	}
}

func TestMiddlewareCanRecoverPanics(t *testing.T) {
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	handler := Middleware(client, Options{RecoverPanics: true})(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		panic("boom")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected recovery response, got %d", response.Code)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush panic capture: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected panic and request events, got %#v", recorder.requests)
	}
}
