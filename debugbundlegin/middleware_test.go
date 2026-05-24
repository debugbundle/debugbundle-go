package debugbundlegin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

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

func TestGinMiddlewareCapturesRouteAndError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{ProjectToken: "dbundle_proj_test", Transport: recorder})
	router := gin.New()
	router.Use(Middleware(client))
	router.GET("/widgets/:id", func(c *gin.Context) {
		_ = c.Error(assertiveError("widget failed"))
		c.Status(http.StatusBadGateway)
	})
	request := httptest.NewRequest(http.MethodGet, "/widgets/123", nil)
	request.Header.Set("X-DebugBundle-Trace-Id", "trace-gin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush gin events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected request and error events, got %#v", recorder.requests)
	}
	var requestEvent debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[1], &requestEvent); err != nil {
		t.Fatalf("unmarshal request event: %v", err)
	}
	if requestEvent.Payload["route_template"] != "/widgets/:id" {
		t.Fatalf("expected gin full path, got %#v", requestEvent.Payload["route_template"])
	}
	if requestEvent.Correlation["trace_id"] != "trace-gin" {
		t.Fatalf("expected propagated trace id, got %#v", requestEvent.Correlation["trace_id"])
	}
}

type assertiveError string

func (message assertiveError) Error() string {
	return string(message)
}
