package debugbundle

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/transport"
)

func TestPackageFacadeDelegatesToDefaultClient(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := Init(Config{
		ProjectToken: "project-token",
		Service:      "facade-test",
		Environment:  "test",
		Transport:    recorder,
	})
	t.Cleanup(clearDefaultClient)

	ctx := ContextWithTraceID(context.Background(), "trace-123")
	CapturePanics()
	SetContext("component", "facade")
	Probe("checkout.value", 42)
	ProbeLazy("checkout.lazy", func() any { return []string{"a", "b"} })
	CaptureException(errors.New("exception"), ctx)
	CaptureError(errors.New("error"), nil)
	CaptureLog("warning", LevelWarning, ctx)
	CaptureLogWithContext("context warning", LevelWarning, map[string]any{"attempt": 2}, ctx)
	CaptureRequest(
		httptestRequest(t),
		ResponseInfo{StatusCode: http.StatusServiceUnavailable, Duration: 10 * time.Millisecond},
		ctx,
	)
	CaptureMessage("message", WithEventContext(map[string]any{"source": "facade"}))

	func() {
		defer Recover(ctx)
		panic("recovered")
	}()

	done := make(chan struct{})
	Go(ctx, func(context.Context) {
		defer close(done)
		panic("goroutine")
	})
	<-done

	deadline := time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		buffered := len(client.buffer)
		client.mu.Unlock()
		if buffered >= 7 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	//nolint:staticcheck // The facade contract explicitly accepts a nil context.
	if err := Flush(nil); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(recorder.requests) == 0 {
		t.Fatal("expected facade events to reach the configured transport")
	}
	if Status() != StatusHealthy || LastEventAt() == nil {
		t.Fatalf("unexpected facade status=%s lastEventAt=%v", Status(), LastEventAt())
	}
	if Diagnostics() == nil {
		t.Fatal("expected diagnostics snapshot to be non-nil with active client")
	}
	if getDefaultClient() != client {
		t.Fatal("expected initialized client to be the package default")
	}
}

func TestPackageFacadeIsSafeWithoutDefaultClient(t *testing.T) {
	clearDefaultClient()
	t.Cleanup(clearDefaultClient)

	CapturePanics()
	CaptureException(errors.New("ignored"))
	CaptureError(errors.New("ignored"))
	CaptureLog("ignored", LevelError)
	CaptureLogWithContext("ignored", LevelError, nil)
	CaptureRequest(nil, ResponseInfo{})
	CaptureMessage("ignored")
	SetContext("ignored", true)
	Probe("ignored", nil)
	ProbeLazy("ignored", func() any { return nil })
	//nolint:staticcheck // The facade must remain safe when both client and context are absent.
	if err := Flush(nil); err != nil {
		t.Fatalf("flush without client: %v", err)
	}
	if Status() != StatusDisconnected || LastEventAt() != nil || Diagnostics() != nil {
		t.Fatalf("unexpected empty facade state: %s %v %#v", Status(), LastEventAt(), Diagnostics())
	}
	func() {
		defer Recover(context.Background())
		panic("ignored")
	}()
}

func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "https://example.com/orders", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return request
}

func clearDefaultClient() {
	defaultClientMu.Lock()
	defaultClient = nil
	defaultClientMu.Unlock()
}
