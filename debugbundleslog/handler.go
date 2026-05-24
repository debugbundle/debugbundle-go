package debugbundleslog

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"

	debugbundle "github.com/debugbundle/debugbundle-go"
)

type Handler struct {
	client *debugbundle.Client
	next   slog.Handler
	attrs  []groupedAttr
	groups []string
}

type groupedAttr struct {
	attr   slog.Attr
	groups []string
}

func NewHandler(client *debugbundle.Client, next slog.Handler) *Handler {
	return &Handler{client: client, next: next}
}

func (handler *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	if handler.next == nil {
		return true
	}
	return handler.next.Enabled(ctx, level)
}

func (handler *Handler) Handle(ctx context.Context, record slog.Record) error {
	fields := map[string]any{}
	for _, attribute := range handler.attrs {
		insertAttr(fields, attribute.groups, attribute.attr)
	}
	record.Attrs(func(attribute slog.Attr) bool {
		insertAttr(fields, handler.groups, attribute)
		return true
	})
	if record.PC != 0 {
		frames := runtime.CallersFrames([]uintptr{record.PC})
		frame, _ := frames.Next()
		fields["source"] = map[string]any{
			"file": filepath.Base(frame.File),
			"line": frame.Line,
		}
	}
	handler.capture(ctx, record, fields)
	if handler.next == nil {
		return nil
	}
	return handler.next.Handle(ctx, record)
}

func (handler *Handler) capture(ctx context.Context, record slog.Record, fields map[string]any) {
	defer func() {
		_ = recover()
	}()
	if handler.client != nil {
		handler.client.CaptureLog(ctx, record.Message, mapLevel(record.Level), fields)
	}
}

func (handler *Handler) WithAttrs(attributes []slog.Attr) slog.Handler {
	attrs := append([]groupedAttr{}, handler.attrs...)
	for _, attribute := range attributes {
		attrs = append(attrs, groupedAttr{attr: attribute, groups: append([]string{}, handler.groups...)})
	}
	if handler.next == nil {
		return &Handler{client: handler.client, attrs: attrs, groups: append([]string{}, handler.groups...)}
	}
	return &Handler{client: handler.client, next: handler.next.WithAttrs(attributes), attrs: attrs, groups: append([]string{}, handler.groups...)}
}

func (handler *Handler) WithGroup(name string) slog.Handler {
	groups := append([]string{}, handler.groups...)
	if name != "" {
		groups = append(groups, name)
	}
	if handler.next == nil {
		return &Handler{client: handler.client, attrs: append([]groupedAttr{}, handler.attrs...), groups: groups}
	}
	return &Handler{client: handler.client, next: handler.next.WithGroup(name), attrs: append([]groupedAttr{}, handler.attrs...), groups: groups}
}

func mapLevel(level slog.Level) debugbundle.LogLevel {
	switch {
	case level <= slog.LevelDebug:
		return debugbundle.LevelDebug
	case level < slog.LevelWarn:
		return debugbundle.LevelInfo
	case level < slog.LevelError:
		return debugbundle.LevelWarning
	case level < slog.LevelError+4:
		return debugbundle.LevelError
	default:
		return debugbundle.LevelCritical
	}
}

func insertAttr(fields map[string]any, groups []string, attribute slog.Attr) {
	if attribute.Key == "" {
		return
	}
	target := fields
	for _, group := range groups {
		if group == "" {
			continue
		}
		child, ok := target[group].(map[string]any)
		if !ok {
			child = map[string]any{}
			target[group] = child
		}
		target = child
	}
	target[attribute.Key] = valueFromAttr(attribute.Value.Resolve())
}

func valueFromAttr(value slog.Value) any {
	switch value.Kind() {
	case slog.KindAny:
		return value.Any()
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().Milliseconds()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindString:
		return value.String()
	case slog.KindTime:
		return value.Time().Format("2006-01-02T15:04:05.000000000Z07:00")
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindGroup:
		values := map[string]any{}
		for _, attribute := range value.Group() {
			values[attribute.Key] = valueFromAttr(attribute.Value)
		}
		return values
	default:
		return value.String()
	}
}
