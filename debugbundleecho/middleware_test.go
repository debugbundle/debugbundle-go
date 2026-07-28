package debugbundleecho

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

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

func TestEchoMiddlewareCapturesRouteAndError(t *testing.T) {
	e := echo.New()
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	e.Use(Middleware(client))
	e.GET("/orders/:id", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusInternalServerError, errors.New("order failed"))
	})
	request := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace-echo")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush echo events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected request and error events, got %#v", recorder.requests)
	}
	var requestEvent debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[1], &requestEvent); err != nil {
		t.Fatalf("unmarshal request event: %v", err)
	}
	if requestEvent.Payload["route_template"] != "/orders/:id" {
		t.Fatalf("expected echo path capture, got %#v", requestEvent.Payload["route_template"])
	}
	if requestEvent.Correlation["trace_id"] != "trace-echo" {
		t.Fatalf("expected propagated trace id, got %#v", requestEvent.Correlation["trace_id"])
	}
}

func TestEchoMiddlewareCapturesAndRethrowsPanics(t *testing.T) {
	e := echo.New()
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	echoContext := e.NewContext(httptest.NewRequest(http.MethodGet, "/panic", nil), httptest.NewRecorder())
	echoContext.SetPath("/panic")
	handler := Middleware(client)(func(echo.Context) error {
		panic("echo panic")
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = handler(echoContext)
	}()
	if recovered != "echo panic" {
		t.Fatalf("expected original panic to be rethrown, got %#v", recovered)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush panic events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected panic exception and request events, got %#v", recorder.requests)
	}
}
