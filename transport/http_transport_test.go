package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPTransportSendsCanonicalBatchAndReadsAcknowledgement(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer project-token" {
			t.Errorf("unexpected authorization header %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type %q", request.Header.Get("Content-Type"))
		}
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Events) != 1 {
			t.Fatalf("expected one event, got %d", len(body.Events))
		}
		writer.Header().Set("Retry-After", "2.5")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"accepted":1,"rejected":0,"errors":[]}`))
	}))
	defer server.Close()

	client := NewHTTPTransport(server.URL, time.Second)
	response, err := client.Send(context.Background(), Request{
		ProjectToken: "project-token",
		Events:       []json.RawMessage{json.RawMessage(`{"event_type":"log_event"}`)},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if response.StatusCode != http.StatusAccepted || response.RetryAfter != 2500*time.Millisecond {
		t.Fatalf("unexpected response %#v", response)
	}
	if string(response.Body) != `{"accepted":1,"rejected":0,"errors":[]}` {
		t.Fatalf("unexpected response body %q", response.Body)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHTTPTransportHandlesInvalidRequestsAndNetworkFailure(t *testing.T) {
	t.Parallel()

	invalid := NewHTTPTransport("://bad-endpoint", 0)
	if _, err := invalid.Send(context.Background(), Request{}); err == nil {
		t.Fatal("expected invalid endpoint error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	unavailable := NewHTTPTransport(endpoint, 50*time.Millisecond)
	if _, err := unavailable.Send(context.Background(), Request{}); err == nil {
		t.Fatal("expected network failure")
	}
}

func TestHTTPTransportPropagatesResponseReadFailure(t *testing.T) {
	t.Parallel()

	client := NewHTTPTransport("https://example.invalid", time.Second)
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     make(http.Header),
			Body:       failingReadCloser{},
		}, nil
	})
	if _, err := client.Send(context.Background(), Request{}); err == nil {
		t.Fatal("expected response read failure")
	}
}

func TestBoundedRetryAfter(t *testing.T) {
	t.Parallel()

	tests := map[string]time.Duration{
		"":       0,
		"nope":   0,
		"-1":     0,
		"0.5":    500 * time.Millisecond,
		"999999": maxRetryAfter,
	}
	for value, expected := range tests {
		if actual := boundedRetryAfter(value); actual != expected {
			t.Errorf("boundedRetryAfter(%q) = %s, want %s", value, actual, expected)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (failingReadCloser) Close() error {
	return nil
}
