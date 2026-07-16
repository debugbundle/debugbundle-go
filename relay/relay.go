package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/transport"
)

const (
	defaultMaxBodyBytes        int64 = 256 * 1024
	defaultRateLimitPerMinit         = 60
	browserSDKName                   = "@debugbundle/sdk-browser"
	relayDeliveredMarkerSuffix       = ".delivered"
)

var acceptedEventTypes = map[string]struct{}{
	"frontend_exception":  {},
	"error_suppressed":    {},
	"frontend_breadcrumb": {},
	"request_event":       {},
	"probe_event":         {},
	"analytics_event":     {},
}

var errInvalidBrowserRelayPayload = errString("Invalid browser relay event payload.")

type Options struct {
	AllowedOrigins       []string
	MaxBodyBytes         int64
	RateLimitPerMinute   int
	DurableWrite         bool
	TrustForwardedHeader bool
	ProjectMode          debugbundle.ProjectMode
	ProjectToken         string
	Endpoint             string
	LocalEventsDir       string
	SpoolDir             string
	Service              string
	Environment          string
	Transport            transport.Sender
}

type responseBody struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors"`
}

type rateLimiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{entries: map[string][]time.Time{}}
}

func (limiter *rateLimiter) Allow(key string, limit int, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if key == "" {
		key = "unknown"
	}
	windowStart := now.Add(-time.Minute)
	entries := limiter.entries[key]
	kept := entries[:0]
	for _, entry := range entries {
		if entry.After(windowStart) {
			kept = append(kept, entry)
		}
	}
	if len(kept) >= limit {
		limiter.entries[key] = kept
		return false
	}
	limiter.entries[key] = append(kept, now)
	return true
}

type handler struct {
	options     Options
	config      debugbundle.RuntimeConfig
	rateLimiter *rateLimiter
	now         func() time.Time
}

func NewHandler(client *debugbundle.Client, options Options) http.Handler {
	config := debugbundle.RuntimeConfig{}
	if client != nil {
		config = client.RuntimeConfig()
	}
	resolved := resolveOptions(config, options)
	return &handler{
		options:     resolved,
		config:      config,
		rateLimiter: newRateLimiter(),
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func resolveOptions(config debugbundle.RuntimeConfig, options Options) Options {
	resolved := options
	if resolved.MaxBodyBytes <= 0 {
		resolved.MaxBodyBytes = defaultMaxBodyBytes
	}
	if resolved.RateLimitPerMinute <= 0 {
		resolved.RateLimitPerMinute = defaultRateLimitPerMinit
	}
	if resolved.ProjectMode == "" {
		resolved.ProjectMode = config.ProjectMode
	}
	if resolved.ProjectToken == "" {
		resolved.ProjectToken = config.ProjectToken
	}
	if resolved.Endpoint == "" {
		resolved.Endpoint = config.Endpoint
	}
	if resolved.LocalEventsDir == "" {
		resolved.LocalEventsDir = config.LocalEventsDir
	}
	if resolved.SpoolDir == "" {
		resolved.SpoolDir = config.SpoolDir
	}
	return resolved
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !handler.originAllowed(request) {
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	setCORSHeaders(writer, requestOrigin(request))
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeJSON(writer, http.StatusBadRequest, responseBody{Accepted: 0, Rejected: 0, Errors: []string{"Relay requests must use Content-Type: application/json."}})
		return
	}
	if request.ContentLength > handler.options.MaxBodyBytes && request.ContentLength >= 0 {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, handler.options.MaxBodyBytes)
	var payload struct {
		Batch []map[string]any `json:"batch"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		writeJSON(writer, http.StatusBadRequest, responseBody{Accepted: 0, Rejected: 0, Errors: []string{"Relay request body must include a batch array."}})
		return
	}
	ipAddress := clientIP(request, handler.options.TrustForwardedHeader)
	if !handler.rateLimiter.Allow(ipAddress, handler.options.RateLimitPerMinute, handler.now()) {
		writer.WriteHeader(http.StatusTooManyRequests)
		return
	}
	accepted := make([]json.RawMessage, 0, len(payload.Batch))
	errors := make([]string, 0)
	for index, candidate := range payload.Batch {
		sanitized, err := sanitizeEvent(candidate, handler.options)
		if err != nil {
			errors = append(errors, "batch["+strconvItoa(index)+"]: "+err.Error())
			continue
		}
		encoded, err := json.Marshal(sanitized)
		if err != nil {
			errors = append(errors, "batch["+strconvItoa(index)+"]: invalid browser relay event payload.")
			continue
		}
		accepted = append(accepted, encoded)
	}
	if len(accepted) > 0 {
		if !handler.deliverAccepted(request.Context(), accepted) {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	status := http.StatusAccepted
	if len(errors) > 0 {
		status = http.StatusBadRequest
	}
	writeJSON(writer, status, responseBody{Accepted: len(accepted), Rejected: len(errors), Errors: errors})
}

func (handler *handler) deliverAccepted(ctx context.Context, accepted []json.RawMessage) bool {
	switch handler.options.ProjectMode {
	case debugbundle.ProjectModeLocalOnly:
		transportToUse := handler.options.Transport
		if transportToUse == nil {
			transportToUse = handler.localDeliveryTransport()
		}
		if transportToUse == nil {
			return false
		}
		response, err := transportToUse.Send(ctx, transport.Request{ProjectToken: handler.options.ProjectToken, Events: accepted})
		return err == nil && response.StatusCode >= 200 && response.StatusCode < 300
	case debugbundle.ProjectModeConnected:
		if handler.options.DurableWrite {
			spoolTransport := handler.spoolDeliveryTransport()
			if spoolTransport == nil {
				return false
			}
			spoolResponse, err := spoolTransport.Send(ctx, transport.Request{Events: accepted})
			if err != nil || spoolResponse.StatusCode < 200 || spoolResponse.StatusCode >= 300 {
				return false
			}
			forwardTransport := handler.forwardDeliveryTransport()
			if forwardTransport != nil {
				forwardResponse, err := forwardTransport.Send(ctx, transport.Request{ProjectToken: handler.options.ProjectToken, Events: accepted})
				if err == nil && forwardResponse.StatusCode >= 200 && forwardResponse.StatusCode < 300 {
					markSpoolFileDelivered(spoolResponse.WrittenFilePath)
				}
			}
			return true
		}
		forwardTransport := handler.forwardDeliveryTransport()
		if forwardTransport == nil {
			return false
		}
		forwardResponse, err := forwardTransport.Send(ctx, transport.Request{ProjectToken: handler.options.ProjectToken, Events: accepted})
		return err == nil && forwardResponse.StatusCode >= 200 && forwardResponse.StatusCode < 300
	default:
		return true
	}
}

func (handler *handler) localDeliveryTransport() transport.Sender {
	fileTransport, err := transport.NewFileTransport(handler.defaultLocalDir())
	if err != nil {
		return nil
	}
	return fileTransport
}

func (handler *handler) spoolDeliveryTransport() transport.Sender {
	fileTransport, err := transport.NewFileTransport(handler.defaultSpoolDir())
	if err != nil {
		return nil
	}
	return fileTransport
}

func (handler *handler) forwardDeliveryTransport() transport.Sender {
	if handler.options.Transport != nil {
		return handler.options.Transport
	}
	if strings.TrimSpace(handler.options.Endpoint) == "" || strings.TrimSpace(handler.options.ProjectToken) == "" {
		return nil
	}
	return transport.NewHTTPTransport(handler.options.Endpoint, 5*time.Second)
}

func (handler *handler) defaultLocalDir() string {
	if handler.options.LocalEventsDir != "" {
		return handler.options.LocalEventsDir
	}
	return filepath.Clean(".debugbundle/local/events")
}

func (handler *handler) defaultSpoolDir() string {
	if handler.options.SpoolDir != "" {
		return handler.options.SpoolDir
	}
	return filepath.Clean(".debugbundle/local/browser-relay-spool")
}

func (handler *handler) originAllowed(request *http.Request) bool {
	origin := requestOrigin(request)
	if origin == "" {
		return false
	}
	if len(handler.options.AllowedOrigins) > 0 {
		for _, candidate := range handler.options.AllowedOrigins {
			if normalizeOrigin(candidate) == normalizeOrigin(origin) {
				return true
			}
		}
		return false
	}
	expected := inferredOrigin(request, handler.options.TrustForwardedHeader)
	return normalizeOrigin(expected) == normalizeOrigin(origin)
}

func requestOrigin(request *http.Request) string {
	origin := request.Header.Get("Origin")
	if strings.TrimSpace(origin) != "" {
		return origin
	}
	return originFromReferer(request.Header.Get("Referer"))
}

func setCORSHeaders(writer http.ResponseWriter, origin string) {
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	writer.Header().Set("Access-Control-Allow-Headers", "content-type")
	writer.Header().Set("Access-Control-Max-Age", "600")
	writer.Header().Set("Vary", "Origin")
}

func sanitizeEvent(candidate map[string]any, options Options) (map[string]any, error) {
	schemaVersion, ok := nonEmptyString(candidate["schema_version"])
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	eventID, ok := nonEmptyString(candidate["event_id"])
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	eventType, ok := nonEmptyString(candidate["event_type"])
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	if _, ok := acceptedEventTypes[eventType]; !ok {
		return nil, errString("Unsupported browser relay event type " + eventType + ".")
	}
	occurredAt, ok := nonEmptyString(candidate["occurred_at"])
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	sdkVersion, ok := nonEmptyString(candidate["sdk_version"])
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	payload, ok := candidate["payload"].(map[string]any)
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	service, ok := sanitizeService(candidate["service"], options)
	if !ok {
		return nil, errInvalidBrowserRelayPayload
	}
	sanitized := map[string]any{
		"schema_version": schemaVersion,
		"event_id":       eventID,
		"event_type":     eventType,
		"sdk_name":       browserSDKName,
		"sdk_version":    sdkVersion,
		"occurred_at":    occurredAt,
		"service":        service,
		"payload":        payload,
	}
	stripSensitiveHeaders(payload)
	if correlation, ok := candidate["correlation"].(map[string]any); ok {
		keptCorrelation := keepCorrelationFields(correlation, eventType)
		if len(keptCorrelation) > 0 {
			sanitized["correlation"] = keptCorrelation
		}
	}
	return sanitized, nil
}

func sanitizeService(value any, options Options) (map[string]any, bool) {
	service, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	name, ok := nonEmptyString(service["name"])
	if !ok {
		return nil, false
	}
	environment, ok := nonEmptyString(service["environment"])
	if !ok {
		return nil, false
	}
	resolvedName := firstNonEmpty(options.Service, name)
	resolvedEnvironment := firstNonEmpty(options.Environment, environment)
	if resolvedName == "" || resolvedEnvironment == "" {
		return nil, false
	}
	sanitized := map[string]any{
		"name":        resolvedName,
		"environment": resolvedEnvironment,
	}
	if runtime, ok := stringOrNil(service["runtime"]); ok {
		sanitized["runtime"] = runtime
	}
	if framework, ok := stringOrNil(service["framework"]); ok {
		sanitized["framework"] = framework
	}
	return sanitized, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func markSpoolFileDelivered(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	markerPath := path + relayDeliveredMarkerSuffix
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = file.Close()
}

func keepCorrelationFields(correlation map[string]any, eventType string) map[string]any {
	kept := map[string]any{}
	keys := []string{"trace_id", "request_id", "session_id", "user_id_hash"}
	if eventType == "analytics_event" {
		keys = []string{"session_id", "visitor_id_hash", "user_id_hash", "trace_id", "deploy_id"}
	}
	for _, key := range keys {
		if value, ok := correlation[key]; ok {
			if value == nil {
				kept[key] = nil
				continue
			}
			if stringValue, ok := value.(string); ok {
				kept[key] = stringValue
			}
		}
	}
	return kept
}

func nonEmptyString(value any) (string, bool) {
	stringValue, ok := value.(string)
	if !ok {
		return "", false
	}
	stringValue = strings.TrimSpace(stringValue)
	if stringValue == "" {
		return "", false
	}
	return stringValue, true
}

func stringOrNil(value any) (any, bool) {
	if value == nil {
		return nil, true
	}
	stringValue, ok := value.(string)
	if !ok {
		return nil, false
	}
	return strings.TrimSpace(stringValue), true
}

func stripSensitiveHeaders(payload map[string]any) {
	for _, key := range []string{"authorization", "cookie", "project_token", "organization_id"} {
		delete(payload, key)
	}
	if headers, ok := payload["headers"].(map[string]any); ok {
		for _, key := range []string{"authorization", "cookie", "x-api-key"} {
			delete(headers, key)
		}
	}
	if request, ok := payload["request"].(map[string]any); ok {
		if headers, ok := request["headers"].(map[string]any); ok {
			for _, key := range []string{"authorization", "cookie", "x-api-key"} {
				delete(headers, key)
			}
		}
	}
}

func originFromReferer(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func inferredOrigin(request *http.Request, trustForwarded bool) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	host := request.Host
	if trustForwarded {
		if forwardedProto := strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
			scheme = forwardedProto
		}
		if forwardedHost := strings.TrimSpace(request.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
			host = forwardedHost
		}
	}
	return scheme + "://" + host
}

func normalizeOrigin(value string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "/"))
}

func clientIP(request *http.Request, trustForwarded bool) string {
	forwarded := strings.TrimSpace(request.Header.Get("X-Forwarded-For"))
	if trustForwarded && forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	return request.RemoteAddr
}

func writeJSON(writer http.ResponseWriter, status int, body responseBody) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

type errString string

func (message errString) Error() string {
	return string(message)
}

func strconvItoa(value int) string {
	return strconv.Itoa(value)
}
