package debugbundlezerolog

import (
	"bytes"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/rs/zerolog"
)

func TestZerologWriterSafePathsAndLevelHelpers(t *testing.T) {
	writer := NewWriter(nil, nil)
	if written, err := writer.Write([]byte("{\"message\":\"safe\"}\n")); err != nil || written == 0 {
		t.Fatalf("expected nil-client writer to preserve write semantics, written=%d err=%v", written, err)
	}
	writer.(*Writer).capture(zerolog.InfoLevel, nil)
	writer.(*Writer).capture(zerolog.InfoLevel, []byte("not-json"))

	buffer := &bytes.Buffer{}
	wrapped := NewWriter(nil, buffer)
	if _, err := wrapped.WriteLevel(zerolog.InfoLevel, []byte("{\"msg\":\"next\"}\n")); err != nil {
		t.Fatalf("expected io.Writer adapter path to succeed: %v", err)
	}
	if buffer.Len() == 0 {
		t.Fatal("expected wrapped writer to receive bytes")
	}

	levels := map[zerolog.Level]debugbundle.LogLevel{
		zerolog.TraceLevel: debugbundle.LevelDebug,
		zerolog.DebugLevel: debugbundle.LevelDebug,
		zerolog.InfoLevel:  debugbundle.LevelInfo,
		zerolog.WarnLevel:  debugbundle.LevelWarning,
		zerolog.ErrorLevel: debugbundle.LevelError,
		zerolog.FatalLevel: debugbundle.LevelCritical,
		zerolog.PanicLevel: debugbundle.LevelCritical,
		zerolog.NoLevel:    debugbundle.LevelWarning,
	}
	for input, expected := range levels {
		encoded := any(nil)
		if input == zerolog.NoLevel {
			encoded = "warn"
		}
		if actual := mapLevel(input, encoded); actual != expected {
			t.Fatalf("mapLevel(%d)=%q, expected %q", input, actual, expected)
		}
	}
	for encoded, expected := range map[string]debugbundle.LogLevel{
		"trace": debugbundle.LevelDebug,
		"debug": debugbundle.LevelDebug,
		"warn":  debugbundle.LevelWarning,
		"error": debugbundle.LevelError,
		"fatal": debugbundle.LevelCritical,
		"panic": debugbundle.LevelCritical,
		"":      debugbundle.LevelInfo,
	} {
		if actual := parseEncodedLevel(encoded); actual != expected {
			t.Fatalf("parseEncodedLevel(%q)=%q, expected %q", encoded, actual, expected)
		}
	}
	if stringValue(1) != "" || firstNonEmpty("", " value ") != " value " || firstNonEmpty("", "") != "" {
		t.Fatal("unexpected zerolog string helper behavior")
	}
}
