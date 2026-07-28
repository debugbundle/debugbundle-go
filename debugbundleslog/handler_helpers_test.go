package debugbundleslog

import (
	"context"
	"log/slog"
	"testing"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
)

func TestSlogHandlerNilDownstreamAndValueConversions(t *testing.T) {
	handler := NewHandler(nil, nil)
	if !handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected nil downstream handler to remain enabled")
	}
	if err := handler.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "safe", 0)); err != nil {
		t.Fatalf("expected nil client/downstream to degrade safely: %v", err)
	}
	grouped := handler.WithAttrs([]slog.Attr{slog.String("tenant", "clinic")}).WithGroup("").WithGroup("http")
	if grouped == nil {
		t.Fatal("expected composable nil-downstream handler")
	}

	levels := map[slog.Level]debugbundle.LogLevel{
		slog.LevelDebug:     debugbundle.LevelDebug,
		slog.LevelInfo:      debugbundle.LevelInfo,
		slog.LevelWarn:      debugbundle.LevelWarning,
		slog.LevelError:     debugbundle.LevelError,
		slog.LevelError + 4: debugbundle.LevelCritical,
	}
	for input, expected := range levels {
		if actual := mapLevel(input); actual != expected {
			t.Fatalf("mapLevel(%d)=%q, expected %q", input, actual, expected)
		}
	}

	timestamp := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
	values := []slog.Value{
		slog.AnyValue(map[string]int{"value": 1}),
		slog.BoolValue(true),
		slog.DurationValue(1500 * time.Millisecond),
		slog.Float64Value(1.5),
		slog.Int64Value(-2),
		slog.StringValue("text"),
		slog.TimeValue(timestamp),
		slog.Uint64Value(3),
		slog.GroupValue(slog.String("nested", "value")),
	}
	for _, value := range values {
		if converted := valueFromAttr(value); converted == nil {
			t.Fatalf("expected conversion for slog kind %s", value.Kind())
		}
	}
	fields := map[string]any{}
	insertAttr(fields, []string{"", "http"}, slog.String("method", "GET"))
	insertAttr(fields, nil, slog.Attr{})
	if fields["http"].(map[string]any)["method"] != "GET" {
		t.Fatalf("expected grouped attr, got %#v", fields)
	}
}
