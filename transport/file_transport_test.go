package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("random unavailable")
}

func TestFileTransportWritesSecureEventBatch(t *testing.T) {
	directory := t.TempDir()
	fileTransport, err := NewFileTransport(directory)
	if err != nil {
		t.Fatalf("new file transport: %v", err)
	}
	response, err := fileTransport.Send(context.Background(), Request{
		ProjectToken: "dbundle_proj_test",
		Events:       []json.RawMessage{json.RawMessage(`{"event_type":"log_event","service":"payments"}`)},
	})
	if err != nil {
		t.Fatalf("send file transport: %v", err)
	}
	if response.StatusCode != 202 {
		t.Fatalf("expected accepted status, got %d", response.StatusCode)
	}
	if response.WrittenFilePath == "" {
		t.Fatalf("expected written file path in transport response")
	}
	matches, err := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one event file, got %d", len(matches))
	}
	if !strings.HasSuffix(filepath.Base(matches[0]), "-payments.events.json") {
		t.Fatalf("expected service-specific filename, got %q", filepath.Base(matches[0]))
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var written []map[string]any
	if err := json.Unmarshal(content, &written); err != nil {
		t.Fatalf("unmarshal event array: %v", err)
	}
	if len(written) != 1 || written[0]["event_type"] != "log_event" {
		t.Fatalf("expected raw event array payload, got %#v", written)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions, got %#o", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("expected 0700 directory permissions, got %#o", directoryInfo.Mode().Perm())
	}
}

func TestFileTransportFailsClosedWhenRandomUnavailable(t *testing.T) {
	directory := t.TempDir()
	fileTransport, err := NewFileTransport(directory)
	if err != nil {
		t.Fatalf("new file transport: %v", err)
	}
	previousReader := randomReader
	randomReader = failingReader{}
	t.Cleanup(func() { randomReader = previousReader })
	_, err = fileTransport.Send(context.Background(), Request{
		ProjectToken: "dbundle_proj_test",
		Events:       []json.RawMessage{json.RawMessage(`{"event_type":"log_event","service":"payments"}`)},
	})
	if err == nil {
		t.Fatalf("expected random failure to abort file write")
	}
	matches, err := filepath.Glob(filepath.Join(directory, "*"))
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no event or temp files after random failure, got %#v", matches)
	}
}

func TestFileTransportValidationAndServiceInference(t *testing.T) {
	if _, err := NewFileTransport(" "); err == nil {
		t.Fatal("expected blank root rejection")
	}
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("file"), 0o600); err != nil {
		t.Fatalf("create file fixture: %v", err)
	}
	if _, err := NewFileTransport(filePath); err == nil {
		t.Fatal("expected file root rejection")
	}
	if err := ensureDirectoryMode(filePath); err == nil {
		t.Fatal("expected direct file mode validation to reject non-directories")
	}
	linkPath := filepath.Join(t.TempDir(), "events-link")
	if err := os.Symlink(t.TempDir(), linkPath); err != nil {
		t.Fatalf("create symlink fixture: %v", err)
	}
	if _, err := NewFileTransport(linkPath); err == nil {
		t.Fatal("expected symlink root rejection")
	}
	if err := ensureDirectoryMode(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing directory rejection")
	}

	cases := []struct {
		name     string
		events   []json.RawMessage
		expected string
	}{
		{name: "empty", expected: "service"},
		{name: "invalid json", events: []json.RawMessage{json.RawMessage(`{`)}, expected: "service"},
		{name: "string service", events: []json.RawMessage{json.RawMessage(`{"service":" checkout api "}`)}, expected: "checkout-api"},
		{name: "object service", events: []json.RawMessage{json.RawMessage(`{"service":{"name":"orders/api"}}`)}, expected: "orders-api"},
		{name: "missing service name", events: []json.RawMessage{json.RawMessage(`{"service":{}}`)}, expected: "service"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := inferServiceName(testCase.events); actual != testCase.expected {
				t.Fatalf("inferServiceName=%q, expected %q", actual, testCase.expected)
			}
		})
	}
	if sanitizeServiceName(" -- ") != "service" || sanitizeServiceName("a---b") != "a-b" {
		t.Fatal("unexpected service-name sanitization")
	}
}

func TestFileTransportHonorsCancelledContext(t *testing.T) {
	fileTransport, err := NewFileTransport(t.TempDir())
	if err != nil {
		t.Fatalf("new file transport: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fileTransport.Send(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if _, err := fileTransport.Send(context.Background(), Request{
		Events: []json.RawMessage{json.RawMessage(`{`)},
	}); err == nil {
		t.Fatal("expected invalid raw JSON to fail before file creation")
	}
}
