package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

type CaptureLogsMode string

const (
	CaptureLogsOff     CaptureLogsMode = "off"
	CaptureLogsError   CaptureLogsMode = "error"
	CaptureLogsWarning CaptureLogsMode = "warning"
	CaptureLogsInfo    CaptureLogsMode = "info"
)

type CaptureRequestEventsMode string

const (
	CaptureRequestEventsOff          CaptureRequestEventsMode = "off"
	CaptureRequestEventsFailuresOnly CaptureRequestEventsMode = "failures_only"
	CaptureRequestEventsFiltered     CaptureRequestEventsMode = "filtered"
	CaptureRequestEventsAll          CaptureRequestEventsMode = "all"
)

type CaptureProbeEventsMode string

const (
	CaptureProbeEventsBufferOnly             CaptureProbeEventsMode = "buffer_only"
	CaptureProbeEventsStandaloneWhenActivated CaptureProbeEventsMode = "standalone_when_activated"
)

type CapturePolicy struct {
	Preset                      string
	CaptureLogs                 CaptureLogsMode
	CaptureRequestEvents        CaptureRequestEventsMode
	CaptureBreadcrumbs          string
	CaptureProbeEvents          CaptureProbeEventsMode
	ImmediateClientErrorStatuses []int
}

type RemoteProbeDirective struct {
	ID           string
	LabelPattern string
	Service      string
	Environment  string
	ExpiresAt    time.Time
}

type RemoteConfigSnapshot struct {
	ProbesEnabled       bool
	RemoteProbesEnabled bool
	Directives          []RemoteProbeDirective
	PollInterval        time.Duration
	TriggerTokenKey     string
	CapturePolicy       CapturePolicy
	ETag                string
}

type RemoteConfigRequest struct {
	URL         string
	ProjectToken string
	IfNoneMatch string
	Timeout     time.Duration
}

type RemoteConfigResponse struct {
	StatusCode int
	Body       []byte
	ETag       string
}

type RemoteConfigFetcher interface {
	Fetch(context.Context, RemoteConfigRequest) (RemoteConfigResponse, error)
}

type HTTPRemoteConfigFetcher struct {
	client *http.Client
}

func NewHTTPRemoteConfigFetcher(timeout time.Duration) *HTTPRemoteConfigFetcher {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &HTTPRemoteConfigFetcher{client: &http.Client{Timeout: timeout}}
}

func (fetcher *HTTPRemoteConfigFetcher) Fetch(ctx context.Context, request RemoteConfigRequest) (RemoteConfigResponse, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL, nil)
	if err != nil {
		return RemoteConfigResponse{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.ProjectToken)
	httpRequest.Header.Set("Accept", "application/json")
	if request.IfNoneMatch != "" {
		httpRequest.Header.Set("If-None-Match", request.IfNoneMatch)
	}
	httpResponse, err := fetcher.client.Do(httpRequest)
	if err != nil {
		return RemoteConfigResponse{}, err
	}
	defer httpResponse.Body.Close()
	body, err := ioReadAll(httpResponse.Body)
	if err != nil {
		return RemoteConfigResponse{}, err
	}
	return RemoteConfigResponse{
		StatusCode: httpResponse.StatusCode,
		Body:       body,
		ETag:       httpResponse.Header.Get("ETag"),
	}, nil
}

func defaultRemoteConfigURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return strings.TrimRight(endpoint, "/") + "/sdk/config"
	}
	parsed.Path = path.Join(path.Dir(parsed.Path), "sdk", "config")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func balancedCapturePolicy() CapturePolicy {
	return CapturePolicy{
		Preset:               "balanced",
		CaptureLogs:          CaptureLogsWarning,
		CaptureRequestEvents: CaptureRequestEventsFailuresOnly,
		CaptureBreadcrumbs:   "exception_only",
		CaptureProbeEvents:   CaptureProbeEventsBufferOnly,
	}
}

func minimalCapturePolicy() CapturePolicy {
	return CapturePolicy{
		Preset:               "minimal",
		CaptureLogs:          CaptureLogsError,
		CaptureRequestEvents: CaptureRequestEventsFailuresOnly,
		CaptureBreadcrumbs:   "local_only",
		CaptureProbeEvents:   CaptureProbeEventsBufferOnly,
	}
}

func parseRemoteConfig(body []byte, fallbackInterval time.Duration, now time.Time) (RemoteConfigSnapshot, error) {
	var payload struct {
		ProbesEnabled       bool `json:"probes_enabled"`
		RemoteProbesEnabled bool `json:"remote_probes_enabled"`
		ActiveProbes        []struct {
			ID           string `json:"id"`
			LabelPattern string `json:"label_pattern"`
			Service      string `json:"service"`
			Environment  string `json:"environment"`
			ExpiresAt    string `json:"expires_at"`
		} `json:"active_probes"`
		PollIntervalMS int `json:"poll_interval_ms"`
		TriggerTokenKey string `json:"trigger_token_key"`
		CapturePolicy *struct {
			Preset                     string   `json:"preset"`
			CaptureLogs                string   `json:"capture_logs"`
			CaptureRequestEvents       string   `json:"capture_request_events"`
			CaptureBreadcrumbs         string   `json:"capture_breadcrumbs"`
			CaptureProbeEvents         string   `json:"capture_probe_events"`
			ImmediateClientErrorStatuses []int `json:"immediate_client_error_statuses"`
		} `json:"capture_policy"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RemoteConfigSnapshot{}, err
	}
	policy, err := parseCapturePolicy(payload.CapturePolicy)
	if err != nil {
		return RemoteConfigSnapshot{}, err
	}
	interval := fallbackInterval
	if payload.PollIntervalMS > 0 {
		interval = time.Duration(payload.PollIntervalMS) * time.Millisecond
	}
	directives := make([]RemoteProbeDirective, 0, len(payload.ActiveProbes))
	for _, candidate := range payload.ActiveProbes {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.LabelPattern) == "" || strings.TrimSpace(candidate.Service) == "" || strings.TrimSpace(candidate.Environment) == "" {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, normalizeRFC3339(candidate.ExpiresAt))
		if err != nil || !expiresAt.After(now) {
			continue
		}
		directives = append(directives, RemoteProbeDirective{
			ID:           candidate.ID,
			LabelPattern: candidate.LabelPattern,
			Service:      candidate.Service,
			Environment:  candidate.Environment,
			ExpiresAt:    expiresAt,
		})
	}
	return RemoteConfigSnapshot{
		ProbesEnabled:       payload.ProbesEnabled,
		RemoteProbesEnabled: payload.RemoteProbesEnabled,
		Directives:          directives,
		PollInterval:        interval,
		TriggerTokenKey:     strings.TrimSpace(payload.TriggerTokenKey),
		CapturePolicy:       policy,
	}, nil
}

func parseCapturePolicy(payload *struct {
	Preset                     string   `json:"preset"`
	CaptureLogs                string   `json:"capture_logs"`
	CaptureRequestEvents       string   `json:"capture_request_events"`
	CaptureBreadcrumbs         string   `json:"capture_breadcrumbs"`
	CaptureProbeEvents         string   `json:"capture_probe_events"`
	ImmediateClientErrorStatuses []int `json:"immediate_client_error_statuses"`
}) (CapturePolicy, error) {
	if payload == nil {
		return balancedCapturePolicy(), nil
	}
	policy := CapturePolicy{
		Preset:               firstNonEmpty(strings.TrimSpace(payload.Preset), "balanced"),
		CaptureLogs:          CaptureLogsMode(strings.TrimSpace(payload.CaptureLogs)),
		CaptureRequestEvents: CaptureRequestEventsMode(strings.TrimSpace(payload.CaptureRequestEvents)),
		CaptureBreadcrumbs:   strings.TrimSpace(payload.CaptureBreadcrumbs),
		CaptureProbeEvents:   CaptureProbeEventsMode(strings.TrimSpace(payload.CaptureProbeEvents)),
	}
	switch policy.CaptureLogs {
	case CaptureLogsOff, CaptureLogsError, CaptureLogsWarning, CaptureLogsInfo:
	default:
		return CapturePolicy{}, errors.New("invalid capture_logs policy")
	}
	switch policy.CaptureRequestEvents {
	case CaptureRequestEventsOff, CaptureRequestEventsFailuresOnly, CaptureRequestEventsFiltered, CaptureRequestEventsAll:
	default:
		return CapturePolicy{}, errors.New("invalid capture_request_events policy")
	}
	if policy.CaptureBreadcrumbs == "" {
		return CapturePolicy{}, errors.New("invalid capture_breadcrumbs policy")
	}
	switch policy.CaptureProbeEvents {
	case CaptureProbeEventsBufferOnly, CaptureProbeEventsStandaloneWhenActivated:
	default:
		return CapturePolicy{}, errors.New("invalid capture_probe_events policy")
	}
	policy.ImmediateClientErrorStatuses = uniqueSortedClientStatuses(payload.ImmediateClientErrorStatuses)
	return policy, nil
}

func uniqueSortedClientStatuses(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := map[int]struct{}{}
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value < 400 || value > 499 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func normalizeRFC3339(value string) string {
	if strings.HasSuffix(value, "Z") {
		return strings.TrimSuffix(value, "Z") + "Z"
	}
	return value
}
