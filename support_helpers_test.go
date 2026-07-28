package debugbundle

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"
)

type alwaysFailReader struct{}

func (alwaysFailReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestContextHelpersPreserveValuesAndCorrelation(t *testing.T) {
	//nolint:staticcheck // Nil-context handling is an intentional SDK safety guarantee.
	if values := ContextValues(nil); len(values) != 0 {
		t.Fatalf("expected nil context to be empty, got %#v", values)
	}
	ctx := ContextWithUserHash(context.Background(), "user@example.test")
	ctx = ContextWithRequestID(ctx, "request-1")
	ctx = ContextWithTraceID(ctx, "trace-1")
	values := ContextValues(ctx)
	if values["user_id_hash"] == "user@example.test" || len(values["user_id_hash"].(string)) != 64 {
		t.Fatalf("expected stable SHA-256 user hash, got %#v", values["user_id_hash"])
	}
	if TraceIDFromContext(ctx) != "trace-1" || RequestIDFromContext(ctx) != "request-1" {
		t.Fatalf("expected direct correlation values, got trace=%q request=%q", TraceIDFromContext(ctx), RequestIDFromContext(ctx))
	}
	values["request_id"] = "mutated"
	if RequestIDFromContext(ctx) != "request-1" {
		t.Fatal("expected ContextValues to return an isolated copy")
	}

	fallback := context.WithValue(context.Background(), contextValuesKey, map[string]any{
		"trace_id":   "trace-fallback",
		"request_id": "request-fallback",
	})
	if TraceIDFromContext(fallback) != "trace-fallback" || RequestIDFromContext(fallback) != "request-fallback" {
		t.Fatal("expected correlation lookup to use the shared context map")
	}
	//nolint:staticcheck // Nil-context handling is an intentional SDK safety guarantee.
	if TraceIDFromContext(nil) != "" || RequestIDFromContext(nil) != "" {
		t.Fatal("expected nil correlation contexts to degrade safely")
	}
}

func TestEventPayloadHelpersCoverNilAndStructuredRequests(t *testing.T) {
	nilPayload := requestPayload(nil, ResponseInfo{StatusCode: 503, Duration: 1500 * time.Millisecond})
	if nilPayload["method"] != "UNKNOWN" || nilPayload["response_status"] != 503 || nilPayload["duration_ms"] != int64(1500) {
		t.Fatalf("unexpected nil request payload %#v", nilPayload)
	}
	if emptyRequestPayload()["method"] != "UNKNOWN" || emptyResponsePayload()["status_code"] != 0 {
		t.Fatal("expected canonical empty payload defaults")
	}

	requestURL := &url.URL{Path: "/orders", RawQuery: "single=one&multi=a&multi=b"}
	request := &http.Request{Method: http.MethodPost, URL: requestURL, Header: http.Header{}}
	request.Header.Set("User-Agent", "unit-test")
	request.Header.Set("Authorization", "must-not-copy")
	payload := requestPayload(request, ResponseInfo{
		StatusCode: 201,
		Duration:   9 * time.Millisecond,
		Route:      "/orders",
		Headers:    map[string]string{"Content-Type": "application/json"},
	})
	query := payload["query"].(map[string]any)
	if query["single"] != "one" || len(query["multi"].([]string)) != 2 {
		t.Fatalf("expected scalar and list query preservation, got %#v", query)
	}
	headers := payload["headers"].(map[string]any)
	if headers["user-agent"] != "unit-test" || headers["authorization"] != nil {
		t.Fatalf("expected allowlisted request headers only, got %#v", headers)
	}
	if payload["route_template"] != "/orders" || payload["response_headers"] == nil {
		t.Fatalf("expected optional route and response headers, got %#v", payload)
	}
	if firstNonEmpty("", "  selected  ", "ignored") != "  selected  " || firstNonEmpty("", " ") != "" {
		t.Fatal("expected firstNonEmpty to preserve the first nonblank input")
	}
}

func TestEventIDUsesUUIDShapeWhenEntropyFails(t *testing.T) {
	previousReader := cryptorand.Reader
	cryptorand.Reader = alwaysFailReader{}
	t.Cleanup(func() { cryptorand.Reader = previousReader })

	first := newEventID()
	second := newEventID()
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidPattern.MatchString(first) || !uuidPattern.MatchString(second) || first == second {
		t.Fatalf("expected unique version-4 UUID fallbacks, got %q and %q", first, second)
	}
}

func TestSuppressionTrackerResetsWindowsAndPrunesExpiredState(t *testing.T) {
	tracker := newSuppressionTracker()
	start := time.Unix(1_700_000_000, 0)
	for index := 0; index < 4; index++ {
		captured := tracker.ShouldCapture("same", start.Add(time.Duration(index)*time.Millisecond))
		if captured != (index < 3) {
			t.Fatalf("unexpected suppression decision at %d: %t", index, captured)
		}
	}
	aggregates := tracker.PendingAggregates(start.Add(time.Second))
	if len(aggregates) != 1 || aggregates[0].Suppressed != 1 || aggregates[0].LoopMode {
		t.Fatalf("unexpected suppression aggregate %#v", aggregates)
	}
	if len(tracker.PendingAggregates(start.Add(2*time.Second))) != 0 {
		t.Fatal("expected reported suppression counts to reset")
	}
	if !tracker.ShouldCapture("same", start.Add(suppressionWindow+time.Second)) {
		t.Fatal("expected a new suppression window to deliver again")
	}
	if aggregates := tracker.PendingAggregates(start.Add(resetAfterSilence + 2*time.Minute)); len(aggregates) != 0 {
		t.Fatalf("expected expired suppression state to be pruned, got %#v", aggregates)
	}

	values := []time.Time{start, start.Add(time.Second), start.Add(3 * time.Second)}
	pruned := pruneRecent(values, start.Add(2*time.Second))
	if len(pruned) != 1 || !pruned[0].Equal(start.Add(3*time.Second)) {
		t.Fatalf("unexpected pruned values %#v", pruned)
	}
	if unchanged := pruneRecent(values, start.Add(-time.Second)); len(unchanged) != len(values) {
		t.Fatal("expected no-copy fast path when no values are expired")
	}
}

func TestRuntimeConfigReturnsResolvedImmutableSnapshot(t *testing.T) {
	client := New(Config{
		ProjectToken:   "dbundle_proj_test",
		Environment:    "test",
		Service:        "checkout",
		Endpoint:       "https://ingest.example.test/v1/events",
		ProjectMode:    ProjectModeLocalOnly,
		LocalEventsDir: "./events",
		SpoolDir:       "./spool",
		Transport:      &recordingTransport{},
	})
	config := client.RuntimeConfig()
	if config.ProjectToken != "dbundle_proj_test" || config.Environment != "test" || config.Service != "checkout" {
		t.Fatalf("unexpected runtime config %#v", config)
	}
	if config.ProjectMode != ProjectModeLocalOnly || config.LocalEventsDir == "" || config.SpoolDir == "" {
		t.Fatalf("expected resolved local runtime paths, got %#v", config)
	}
}
