package debugbundlezap

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type recordingTransport struct {
	requests []transport.Request
}

func (recorder *recordingTransport) Send(_ context.Context, request transport.Request) (transport.Response, error) {
	recorder.requests = append(recorder.requests, request)
	return transport.Response{StatusCode: 202}, nil
}

func TestZapCoreCapturesStructuredLogs(t *testing.T) {
	buffer := &bytes.Buffer{}
	recorder := &recordingTransport{}
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_test",
		LogLevel:     debugbundle.LevelWarning,
		Transport:    recorder,
	})
	encoderConfig := zap.NewProductionEncoderConfig()
	baseCore := zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(buffer), zap.DebugLevel)
	logger := zap.New(NewCore(client, baseCore)).Named("orders")
	logger.Info("skip me", zap.String("request_id", "req-1"))
	logger.Error("capture me", zap.String("request_id", "req-2"), zap.Any("http", map[string]any{"status": 500}))
	if err := logger.Sync(); err != nil && err.Error() != "sync /dev/stderr: invalid argument" {
		t.Fatalf("sync zap logger: %v", err)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("flush captured zap events: %v", err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one captured error log event, got %#v", recorder.requests)
	}
	if buffer.Len() == 0 {
		t.Fatalf("expected wrapped zap core to preserve downstream logging")
	}
	var event debugbundle.EventEnvelope
	if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
		t.Fatalf("unmarshal zap event: %v", err)
	}
	if event.EventType != "log_event" {
		t.Fatalf("expected log_event, got %q", event.EventType)
	}
	fields := event.Payload["attributes"].(map[string]any)
	if fields["request_id"] != "req-2" {
		t.Fatalf("expected structured zap field capture, got %#v", fields["request_id"])
	}
	if fields["logger"] != "orders" {
		t.Fatalf("expected logger name capture, got %#v", fields["logger"])
	}
	httpFields := fields["http"].(map[string]any)
	if httpFields["status"].(float64) != 500 {
		t.Fatalf("expected nested structured zap fields, got %#v", httpFields["status"])
	}
}
