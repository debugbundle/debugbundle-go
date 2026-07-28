package debugbundlezap

import (
	"testing"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestZapCoreNilDownstreamAndLevelHelpers(t *testing.T) {
	core := NewCore(nil, nil)
	if !core.Enabled(zapcore.InfoLevel) {
		t.Fatal("expected nil downstream core to remain enabled")
	}
	withFields := core.With([]zapcore.Field{zap.String("tenant", "clinic")})
	checked := withFields.Check(zapcore.Entry{Level: zapcore.ErrorLevel}, nil)
	if checked == nil {
		t.Fatal("expected enabled core to add itself to a checked entry")
	}
	if err := withFields.Write(zapcore.Entry{Message: "safe"}, nil); err != nil {
		t.Fatalf("expected nil client to degrade safely: %v", err)
	}
	if err := withFields.Sync(); err != nil {
		t.Fatalf("expected nil downstream sync to succeed: %v", err)
	}

	levels := map[zapcore.Level]debugbundle.LogLevel{
		zapcore.DebugLevel:  debugbundle.LevelDebug,
		zapcore.InfoLevel:   debugbundle.LevelInfo,
		zapcore.WarnLevel:   debugbundle.LevelWarning,
		zapcore.ErrorLevel:  debugbundle.LevelError,
		zapcore.DPanicLevel: debugbundle.LevelCritical,
	}
	for input, expected := range levels {
		if actual := mapLevel(input); actual != expected {
			t.Fatalf("mapLevel(%d)=%q, expected %q", input, actual, expected)
		}
	}
	if len(zapFieldsToMap(nil)) != 0 {
		t.Fatal("expected empty zap fields to stay empty")
	}
	fields := zapFieldsToMap([]zapcore.Field{
		zap.String("name", "checkout"),
		zap.Time("at", time.Unix(1, 0)),
	})
	if fields["name"] != "checkout" || fields["at"] == nil {
		t.Fatalf("unexpected encoded zap fields %#v", fields)
	}
}
