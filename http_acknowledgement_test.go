package debugbundle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			if len(batches) != 2 || string(batches[0][0]) != string(batches[1][0]) || string(batches[0][1]) != string(batches[1][1]) {
				t.Fatalf("full retained batch not recovered: %#v", batches)
			}
		})
	}
}
