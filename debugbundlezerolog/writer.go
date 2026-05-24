package debugbundlezerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/rs/zerolog"
)

type Writer struct {
	client *debugbundle.Client
	next   zerolog.LevelWriter
}

func NewWriter(client *debugbundle.Client, next io.Writer) zerolog.LevelWriter {
	if next == nil {
		return &Writer{client: client}
	}
	levelWriter, ok := next.(zerolog.LevelWriter)
	if !ok {
		levelWriter = zerolog.LevelWriterAdapter{Writer: next}
	}
	return &Writer{client: client, next: levelWriter}
}

func (writer *Writer) Write(payload []byte) (int, error) {
	return writer.WriteLevel(zerolog.NoLevel, payload)
}

func (writer *Writer) WriteLevel(level zerolog.Level, payload []byte) (int, error) {
	if writer.next != nil {
		written, err := writer.next.WriteLevel(level, payload)
		writer.capture(level, payload)
		return written, err
	}
	writer.capture(level, payload)
	return len(payload), nil
}

func (writer *Writer) capture(level zerolog.Level, payload []byte) {
	defer func() {
		_ = recover()
	}()
	if writer.client == nil {
		return
	}
	trimmed := bytes.TrimSpace(append([]byte{}, payload...))
	if len(trimmed) == 0 {
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return
	}
	message := firstNonEmpty(stringValue(decoded["message"]), stringValue(decoded["msg"]))
	if message == "" {
		message = string(trimmed)
	}
	fields := make(map[string]any, len(decoded))
	for key, value := range decoded {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "level", "message", "msg":
			continue
		case "time":
			fields["timestamp"] = value
		default:
			fields[key] = value
		}
	}
	writer.client.CaptureLog(context.Background(), message, mapLevel(level, decoded["level"]), fields)
}

func mapLevel(level zerolog.Level, encodedLevel any) debugbundle.LogLevel {
	switch level {
	case zerolog.TraceLevel, zerolog.DebugLevel:
		return debugbundle.LevelDebug
	case zerolog.InfoLevel:
		return debugbundle.LevelInfo
	case zerolog.WarnLevel:
		return debugbundle.LevelWarning
	case zerolog.ErrorLevel:
		return debugbundle.LevelError
	case zerolog.FatalLevel, zerolog.PanicLevel:
		return debugbundle.LevelCritical
	case zerolog.Disabled, zerolog.NoLevel:
		return parseEncodedLevel(encodedLevel)
	default:
		return parseEncodedLevel(encodedLevel)
	}
}

func parseEncodedLevel(value any) debugbundle.LogLevel {
	switch strings.ToLower(stringValue(value)) {
	case "trace", "debug":
		return debugbundle.LevelDebug
	case "warn", "warning":
		return debugbundle.LevelWarning
	case "error":
		return debugbundle.LevelError
	case "fatal", "panic":
		return debugbundle.LevelCritical
	default:
		return debugbundle.LevelInfo
	}
}

func stringValue(value any) string {
	if cast, ok := value.(string); ok {
		return strings.TrimSpace(cast)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}