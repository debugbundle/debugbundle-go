package debugbundle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPRemoteConfigFetcher(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer project-token" {
			t.Errorf("unexpected authorization %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("If-None-Match") != `"old"` {
			t.Errorf("unexpected headers %#v", request.Header)
		}
		writer.Header().Set("ETag", `"new"`)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"probes_enabled":true}`))
	}))
	defer server.Close()

	fetcher := NewHTTPRemoteConfigFetcher(0)
	response, err := fetcher.Fetch(context.Background(), RemoteConfigRequest{
		URL:          server.URL,
		ProjectToken: "project-token",
		IfNoneMatch:  `"old"`,
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.ETag != `"new"` || string(response.Body) != `{"probes_enabled":true}` {
		t.Fatalf("unexpected response %#v", response)
	}

	if _, err := fetcher.Fetch(context.Background(), RemoteConfigRequest{URL: "://bad"}); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestHTTPRemoteConfigFetcherPropagatesTransportAndReadFailures(t *testing.T) {
	t.Parallel()

	fetcher := NewHTTPRemoteConfigFetcher(time.Second)
	fetcher.client.Transport = remoteRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network failed")
	})
	if _, err := fetcher.Fetch(context.Background(), RemoteConfigRequest{URL: "https://example.invalid"}); err == nil {
		t.Fatal("expected transport error")
	}

	fetcher.client.Transport = remoteRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       remoteFailingReadCloser{},
		}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), RemoteConfigRequest{URL: "https://example.invalid"}); err == nil {
		t.Fatal("expected read error")
	}
}

func TestDefaultRemoteConfigURL(t *testing.T) {
	t.Parallel()

	if actual := defaultRemoteConfigURL("https://api.example.com/v1/events?ignored=yes#fragment"); actual != "https://api.example.com/v1/sdk/config" {
		t.Fatalf("unexpected config URL %q", actual)
	}
	if actual := defaultRemoteConfigURL("://bad"); actual != "://bad/sdk/config" {
		t.Fatalf("unexpected invalid fallback URL %q", actual)
	}
}

func TestParseRemoteConfigAndCapturePolicy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	snapshot, err := parseRemoteConfig([]byte(`{
		"probes_enabled": true,
		"remote_probes_enabled": true,
		"poll_interval_ms": 15000,
		"trigger_token_key": " key ",
		"active_probes": [
			{"id":"active","label_pattern":"checkout.*","service":"api","environment":"prod","expires_at":"2026-07-27T09:00:00Z"},
			{"id":"","label_pattern":"ignored","service":"api","environment":"prod","expires_at":"2026-07-27T09:00:00Z"},
			{"id":"expired","label_pattern":"ignored","service":"api","environment":"prod","expires_at":"2026-07-27T07:00:00Z"},
			{"id":"invalid-date","label_pattern":"ignored","service":"api","environment":"prod","expires_at":"nope"}
		],
		"capture_policy": {
			"preset":"investigative",
			"capture_logs":"info",
			"capture_request_events":"all",
			"capture_breadcrumbs":"always",
			"capture_probe_events":"standalone_when_activated",
			"immediate_client_error_statuses":[429,400,429,399,500],
			"immediate_client_error_path_rules":[
				{"status_code":404,"path_pattern":"/orders/*","methods":["get","GET","post"]}
			]
		}
	}`), time.Minute, now)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if !snapshot.ProbesEnabled || !snapshot.RemoteProbesEnabled || snapshot.PollInterval != 15*time.Second {
		t.Fatalf("unexpected snapshot %#v", snapshot)
	}
	if snapshot.TriggerTokenKey != "key" || len(snapshot.Directives) != 1 || snapshot.Directives[0].ID != "active" {
		t.Fatalf("unexpected directives %#v", snapshot)
	}
	if strings.Join(snapshot.CapturePolicy.ImmediateClientErrorPathRules[0].Methods, ",") != "GET,POST" {
		t.Fatalf("unexpected normalized methods %#v", snapshot.CapturePolicy.ImmediateClientErrorPathRules)
	}
	if len(snapshot.CapturePolicy.ImmediateClientErrorStatuses) != 2 ||
		snapshot.CapturePolicy.ImmediateClientErrorStatuses[0] != 400 ||
		snapshot.CapturePolicy.ImmediateClientErrorStatuses[1] != 429 {
		t.Fatalf("unexpected statuses %#v", snapshot.CapturePolicy.ImmediateClientErrorStatuses)
	}

	fallback, err := parseRemoteConfig([]byte(`{}`), 45*time.Second, now)
	if err != nil {
		t.Fatalf("parse fallback: %v", err)
	}
	if fallback.PollInterval != 45*time.Second || fallback.CapturePolicy.Preset != "balanced" {
		t.Fatalf("unexpected fallback %#v", fallback)
	}
	if _, err := parseRemoteConfig([]byte(`{`), time.Minute, now); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestParseCapturePolicyRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	valid := remoteCapturePolicyPayload{
		Preset:               "balanced",
		CaptureLogs:          "warning",
		CaptureRequestEvents: "failures_only",
		CaptureBreadcrumbs:   "exception_only",
		CaptureProbeEvents:   "buffer_only",
	}
	mutations := []func(*remoteCapturePolicyPayload){
		func(value *remoteCapturePolicyPayload) { value.CaptureLogs = "verbose" },
		func(value *remoteCapturePolicyPayload) { value.CaptureRequestEvents = "sometimes" },
		func(value *remoteCapturePolicyPayload) { value.CaptureBreadcrumbs = "" },
		func(value *remoteCapturePolicyPayload) { value.CaptureProbeEvents = "always" },
		func(value *remoteCapturePolicyPayload) {
			value.ImmediateClientErrorPathRules = []immediateClientErrorPathRulePayload{{StatusCode: 399, PathPattern: "/bad"}}
		},
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if _, err := parseCapturePolicy(&candidate); err == nil {
			t.Errorf("mutation %d should fail", index)
		}
	}
}

func TestImmediateClientErrorPathRuleValidation(t *testing.T) {
	t.Parallel()

	tooMany := make([]immediateClientErrorPathRulePayload, 26)
	if _, err := parseImmediateClientErrorPathRules(tooMany); err == nil {
		t.Fatal("expected too many rules to fail")
	}

	invalid := []immediateClientErrorPathRulePayload{
		{StatusCode: 404, PathPattern: "relative"},
		{StatusCode: 404, PathPattern: "/query?bad"},
		{StatusCode: 404, PathPattern: "/fragment#bad"},
		{StatusCode: 404, PathPattern: "/bad/*/wildcard"},
		{StatusCode: 404, PathPattern: "/" + strings.Repeat("x", 256)},
		{StatusCode: 404, PathPattern: "/orders", Methods: []string{"TRACE"}},
		{StatusCode: 404, PathPattern: "/orders", Methods: make([]string, 8)},
	}
	for index, rule := range invalid {
		if _, err := parseImmediateClientErrorPathRules([]immediateClientErrorPathRulePayload{rule}); err == nil {
			t.Errorf("invalid rule %d should fail: %#v", index, rule)
		}
	}
}

func TestRequestCapturePolicyBranches(t *testing.T) {
	t.Parallel()

	policy := balancedCapturePolicy()
	if shouldCaptureRequestByPolicy(0, "/", "GET", policy) {
		t.Fatal("balanced policy should not capture unknown status")
	}
	policy.CaptureRequestEvents = CaptureRequestEventsAll
	if !shouldCaptureRequestByPolicy(0, "/", "GET", policy) {
		t.Fatal("all policy should capture unknown status")
	}
	policy.CaptureRequestEvents = CaptureRequestEventsOff
	if !shouldCaptureRequestByPolicy(503, "/", "GET", policy) {
		t.Fatal("5xx must be immediate")
	}
	if shouldCaptureRequestByPolicy(200, "/", "GET", policy) {
		t.Fatal("off policy should reject successful request")
	}
	policy.ImmediateClientErrorStatuses = []int{418}
	if !shouldCaptureRequestByPolicy(418, "/", "GET", policy) {
		t.Fatal("configured status should be immediate")
	}
	policy.ImmediateClientErrorPathRules = []ImmediateClientErrorPathRule{{
		StatusCode:  404,
		PathPattern: "/orders/*",
		Methods:     []string{"GET"},
	}}
	if !shouldCaptureRequestByPolicy(404, "https://example.com/orders/123?view=full", " get ", policy) {
		t.Fatal("matching path rule should capture")
	}
	if shouldCaptureRequestByPolicy(404, "/orders/123", "POST", policy) {
		t.Fatal("method mismatch should not capture")
	}
	if matchesImmediateClientErrorPathRule(399, "/", "GET", policy.ImmediateClientErrorPathRules) {
		t.Fatal("non-client error status should not match")
	}
	if actual := normalizeRequestPathForPolicy("%"); actual != "/" {
		t.Fatalf("unexpected invalid path fallback %q", actual)
	}

	policy.Preset = "investigative"
	if !isImmediateRequestIncidentStatus(409, "/", "GET", policy) {
		t.Fatal("investigative 409 should be immediate")
	}
	policy.Preset = "custom"
	if isImmediateRequestIncidentStatus(408, "/", "GET", policy) {
		t.Fatal("custom preset should not inherit balanced statuses")
	}
}

func TestRemoteProbeDirectiveMatches(t *testing.T) {
	t.Parallel()

	now := time.Now()
	directive := RemoteProbeDirective{
		LabelPattern: "checkout.*",
		Service:      "api",
		Environment:  "prod",
		ExpiresAt:    now.Add(time.Minute),
	}
	if !directive.Matches("checkout.tax", "api", "prod", now) || !directive.Matches("checkout", "api", "prod", now) {
		t.Fatal("expected wildcard label match")
	}
	if directive.Matches("checkout.tax", "worker", "prod", now) ||
		directive.Matches("checkout.tax", "api", "dev", now) ||
		directive.Matches("checkout.tax", "api", "prod", now.Add(2*time.Minute)) {
		t.Fatal("expected scope and expiry mismatches")
	}
	directive.LabelPattern = "*"
	directive.Service = "*"
	directive.Environment = "*"
	if !directive.Matches("anything", "worker", "dev", now) {
		t.Fatal("expected global wildcard match")
	}
	directive.LabelPattern = "exact"
	if directive.Matches("other", "worker", "dev", now) {
		t.Fatal("expected exact label mismatch")
	}
}

type remoteRoundTripFunc func(*http.Request) (*http.Response, error)

func (function remoteRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type remoteFailingReadCloser struct{}

func (remoteFailingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (remoteFailingReadCloser) Close() error {
	return nil
}
