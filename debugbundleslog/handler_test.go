package debugbundleslog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
)

type recordingTransport struct {
	requests []transport.Request
}

func (recorder *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	recorder.requests = append(recorder.requests, request)
	return transport.Response{StatusCode: 202}, nil
}

func TestSlogHandlerCapturesStructuredLogs(t *testing.T) {
	buffer := &bytes.Buffer{}
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_test",
		LogLevel:     debugbundle.LevelWarning,
		Transport:    recorder,
	})
	logger := slog.New(NewHandler(client, slog.NewJSONHandler(buffer, nil)))
	logger.Info("skip me", slog.String("request_id", "req-1"))
	logger.Error("capture me", slog.String("request_id", "req-2"), slog.Group("http", slog.Int("status", 500)))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush captured slog events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one captured error log event, got %#v", recorder.requests)
	}
	if buffer.Len() == 0 {
		t.Fatalf("expected wrapped slog handler to preserve downstream logging")
	}
	var event debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal slog event: %v", err)
	}
	if event.EventType != "log_event" {
		t.Fatalf("expected log_event, got %q", event.EventType)
	}
	fields := event.Payload["attributes"].(map[string]any)
	if fields["request_id"] != "req-2" {
		t.Fatalf("expected structured slog field capture, got %#v", fields["request_id"])
	}
	httpFields := fields["http"].(map[string]any)
	if httpFields["status"].(float64) != 500 {
		t.Fatalf("expected nested structured slog fields, got %#v", httpFields["status"])
	}
}

func TestSlogHandlerCapturesBoundAttrsAndGroups(t *testing.T) {
	buffer := &bytes.Buffer{}
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_test",
		LogLevel:     debugbundle.LevelInfo,
		Transport:    recorder,
	})
	logger := slog.New(NewHandler(client, slog.NewJSONHandler(buffer, nil))).With("tenant", "clinic-a").WithGroup("http")
	logger.Error("request failed", slog.Int("status", 503))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush captured slog events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one captured error log event, got %#v", recorder.requests)
	}
	var event debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal slog event: %v", err)
	}
	fields := event.Payload["attributes"].(map[string]any)
	if fields["tenant"] != "clinic-a" {
		t.Fatalf("expected bound slog attr capture, got %#v", fields["tenant"])
	}
	httpFields := fields["http"].(map[string]any)
	if httpFields["status"].(float64) != 503 {
		t.Fatalf("expected grouped slog attr capture, got %#v", httpFields["status"])
	}
	if buffer.Len() == 0 {
		t.Fatalf("expected wrapped slog handler to preserve downstream logging")
	}
}
