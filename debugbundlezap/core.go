package debugbundlezap

import (
	"context"
	"path/filepath"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"go.uber.org/zap/zapcore"
)

type Core struct {
	client *debugbundle.Client
	next   zapcore.Core
	fields []zapcore.Field
}

func NewCore(client *debugbundle.Client, next zapcore.Core) zapcore.Core {
	return &Core{client: client, next: next}
}

func (core *Core) Enabled(level zapcore.Level) bool {
	if core.next == nil {
		return true
	}
	return core.next.Enabled(level)
}

func (core *Core) With(fields []zapcore.Field) zapcore.Core {
	var next zapcore.Core
	if core.next != nil {
		next = core.next.With(fields)
	}
	combined := append(append([]zapcore.Field{}, core.fields...), fields...)
	return &Core{client: core.client, next: next, fields: combined}
}

func (core *Core) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.next == nil {
		if !core.Enabled(entry.Level) {
			return checked
		}
		return checked.AddCore(entry, core)
	}
	checked = core.next.Check(entry, checked)
	if checked == nil {
		return nil
	}
	return checked.AddCore(entry, core)
}

func (core *Core) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	defer func() {
		_ = recover()
	}()
	if core.client == nil {
		return nil
	}
	encodedFields := zapFieldsToMap(append(append([]zapcore.Field{}, core.fields...), fields...))
	if entry.LoggerName != "" {
		encodedFields["logger"] = entry.LoggerName
	}
	if !entry.Time.IsZero() {
		encodedFields["timestamp"] = entry.Time.UTC().Format(time.RFC3339Nano)
	}
	if entry.Caller.Defined {
		encodedFields["source"] = map[string]any{
			"file": filepath.Base(entry.Caller.File),
			"line": entry.Caller.Line,
		}
	}
	if entry.Stack != "" {
		encodedFields["stack"] = entry.Stack
	}
	core.client.CaptureLog(context.Background(), entry.Message, mapLevel(entry.Level), encodedFields)
	return nil
}

func (core *Core) Sync() error {
	if core.next == nil {
		return nil
	}
	return core.next.Sync()
}

func zapFieldsToMap(fields []zapcore.Field) map[string]any {
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(encoder)
	}
	if len(encoder.Fields) == 0 {
		return map[string]any{}
	}
	values := make(map[string]any, len(encoder.Fields))
	for key, value := range encoder.Fields {
		values[key] = value
	}
	return values
}

func mapLevel(level zapcore.Level) debugbundle.LogLevel {
	switch {
	case level <= zapcore.DebugLevel:
		return debugbundle.LevelDebug
	case level < zapcore.WarnLevel:
		return debugbundle.LevelInfo
	case level < zapcore.ErrorLevel:
		return debugbundle.LevelWarning
	case level < zapcore.DPanicLevel:
		return debugbundle.LevelError
	default:
		return debugbundle.LevelCritical
	}
}
