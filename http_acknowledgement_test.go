package debugbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

func TestCustomRetryHintIsBounded(t *testing.T) {
	for _, response := range []transport.Response{
		{StatusCode: 429, RetryAfter: 24 * time.Hour},
		{StatusCode: 202, RetryAfter: 24 * time.Hour, Body: json.RawMessage(`{"accepted":2,"rejected":0,"errors":[]}`)},
		{StatusCode: 202, RetryAfter: 24 * time.Hour, Body: json.RawMessage(`{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"rate_limited"}]}`)},
	} {
		recorder := &recordingTransport{response: response}
		client := New(Config{ProjectToken: "dbundle_proj_test", Transport: recorder, FlushInterval: time.Hour})
		client.CaptureLog(context.Background(), "retry", LevelError, nil)
		_ = client.Flush(context.Background())
		client.mu.Lock()
		remaining := time.Until(client.retryUntil)
		client.mu.Unlock()
		_ = client.Close()
		if remaining > 5*time.Minute || remaining < 299*time.Second {
			t.Fatalf("unbounded retry delay %s", remaining)
		}
	}
}

func TestBuiltInHTTPRequiresAcknowledgement(t *testing.T) {
	for _, body := range []string{"",
		`{"accepted":null,"rejected":2,"errors":[{"index":0,"reason":"rate_limited"},{"index":1,"reason":"rate_limited"}]}`,
		`{"accepted":2,"rejected":0,"errors":null}`,
		`{"accepted":1,"rejected":1,"errors":[{"index":4294967296,"reason":"rate_limited"}]}`,
		`{"accepted":1,"rejected":1,"errors":{"one":{"index":1,"reason":"rate_limited"}}}`, "<html>proxy</html>", "{}", "[]", "null",
		`{"accepted":1,"rejected":0,"errors":[]}`,
		`{"accepted":0,"rejected":2,"errors":[{"index":0,"reason":"rate_limited"},{"index":0,"reason":"rate_limited"}]}`,
		`{"accepted":1,"rejected":1,"errors":[{"index":2,"reason":"rate_limited"}]}`} {
		t.Run(body, func(t *testing.T) {
			batches := [][]json.RawMessage{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(304)
					return
				}
				var request struct {
					Events []json.RawMessage `json:"events"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				batches = append(batches, request.Events)
				w.Header().Set("Retry-After", "300")
				w.WriteHeader(202)
				if len(batches) == 1 {
					_, _ = w.Write([]byte(body))
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(request.Events), "rejected": 0, "errors": []any{}})
				}
			}))
			defer server.Close()
			client := New(Config{ProjectToken: "dbundle_proj_test", Environment: "production", Endpoint: server.URL,
				BatchSize: 100, FlushInterval: time.Hour, RemoteConfigFetcher: &fakeRemoteConfigFetcher{}})
			defer func() { _ = client.Close() }()
			// Exercise acknowledgement retention after initial discovery has released
			// the mutation lock, rather than racing the separate startup path.
			waitForInitialRemoteConfig(t, client)
			client.CaptureLog(context.Background(), "first", LevelError, nil)
			client.CaptureLog(context.Background(), "second", LevelError, nil)
			_ = client.Flush(context.Background())
			if client.LastEventAt() != nil {
				t.Fatal("invalid HTTP response advanced delivery state")
			}
			_ = client.Flush(context.Background())
			if len(batches) != 1 {
				t.Fatalf("backoff ignored: %d requests", len(batches))
			}
			client.mu.Lock()
			client.retryUntil = time.Time{}
			client.mu.Unlock()
			_ = client.Flush(context.Background())
			if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 2 {
				t.Fatalf("full retained batch not recovered: %#v", batches)
			}
			if string(batches[0][0]) != string(batches[1][0]) || string(batches[0][1]) != string(batches[1][1]) {
				t.Fatalf("full retained batch not recovered: %#v", batches)
			}
		})
	}
}

func TestNativeDeliveryUsesFinalRequestBytesAndRetainsFinalizedTail(t *testing.T) {
	var accepted, largest, hooks atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		for previous := largest.Load(); int64(len(data)) > previous; previous = largest.Load() {
			if largest.CompareAndSwap(previous, int64(len(data))) {
				break
			}
		}
		var payload struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.Unmarshal(data, &payload)
		if len(data) > 262144 {
			writer.WriteHeader(413)
			return
		}
		accepted.Add(int64(len(payload.Events)))
		writer.WriteHeader(202)
		_ = json.NewEncoder(writer).Encode(map[string]any{"accepted": len(payload.Events), "rejected": 0, "errors": []any{}})
	}))
	defer server.Close()
	client := New(Config{ProjectToken: "synthetic", Transport: transport.NewHTTPTransport(server.URL, time.Second), BatchSize: 1000, FlushInterval: time.Hour,
		BeforeSend: func(event EventEnvelope) *EventEnvelope { hooks.Add(1); return &event }})
	defer func() { _ = client.Close() }()
	for index := 0; index < 25; index++ {
		fields := map[string]any{}
		for field := 0; field < 8; field++ {
			fields[fmt.Sprintf("detail_%d", field)] = strings.Repeat("x", 1500)
		}
		client.CaptureLog(context.Background(), fmt.Sprintf("wire fixture %d", index), LevelError, fields)
	}
	// Flush has a finite caller wait while its single sender may still finalize
	// a batch. Allow instrumented serialization to settle before checking delivery.
	deadline := time.Now().Add(15 * time.Second)
	for accepted.Load() < 25 && time.Now().Before(deadline) {
		_ = client.Flush(context.Background())
		time.Sleep(time.Millisecond)
	}
	if largest.Load() > 262144 || accepted.Load() != 25 || hooks.Load() != 25 {
		t.Fatalf("native bytes=%d accepted=%d hooks=%d", largest.Load(), accepted.Load(), hooks.Load())
	}
}
