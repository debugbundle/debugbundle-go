package debugbundlezerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
	"github.com/rs/zerolog"
)

type recordingTransport struct {
	requests []transport.Request
}

func (recorder *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	recorder.requests = append(recorder.requests, request)
	return transport.Response{StatusCode: 202}, nil
}

func TestZerologWriterCapturesStructuredLogs(t *testing.T) {
	buffer := &bytes.Buffer{}
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_test",
		LogLevel:     debugbundle.LevelWarning,
		Transport:    recorder,
	})
	logger := zerolog.New(NewWriter(client, buffer)).With().Str("logger", "orders").Logger()
	logger.Info().Str("request_id", "req-1").Msg("skip me")
	logger.Error().Str("request_id", "req-2").Dict("http", zerolog.Dict().Int("status", 500)).Msg("capture me")
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush captured zerolog events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one captured error log event, got %#v", recorder.requests)
	}
	if buffer.Len() == 0 {
		t.Fatalf("expected wrapped zerolog writer to preserve downstream logging")
	}
	var event debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal zerolog event: %v", err)
	}
	if event.EventType != "log_event" {
		t.Fatalf("expected log_event, got %q", event.EventType)
	}
	fields := event.Payload["attributes"].(map[string]any)
	if fields["request_id"] != "req-2" {
		t.Fatalf("expected structured zerolog field capture, got %#v", fields["request_id"])
	}
	if fields["logger"] != "orders" {
		t.Fatalf("expected logger field capture, got %#v", fields["logger"])
	}
	httpFields := fields["http"].(map[string]any)
	if httpFields["status"].(float64) != 500 {
		t.Fatalf("expected nested structured zerolog fields, got %#v", httpFields["status"])
	}
}
