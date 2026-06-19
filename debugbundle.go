package debugbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mathrand "math/rand"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/debugbundle/debugbundle-go/redaction"
	"github.com/debugbundle/debugbundle-go/transport"
)

type Client struct {
	mu                      sync.Mutex
	config                  resolvedConfig
	transport               transport.Sender
	redactor                *redaction.Redactor
	buffer                  []json.RawMessage
	persistent              map[string]any
	probes                  map[string][]probeEntry
	suppression             *suppressionTracker
	status                  SDKStatus
	lastEventAt             *time.Time
	retryUntil              time.Time
	failures                int
	flushTimer              *time.Timer
	remoteConfigTimer       *time.Timer
	rand                    *mathrand.Rand
	closed                  bool
	diagnostics             []string
	remoteConfigFetcher     RemoteConfigFetcher
	remoteConfigURL         string
	remoteConfigETag        string
	remoteConfigSnapshot    RemoteConfigSnapshot
	remoteConfigInitialized bool
	capturePolicy           CapturePolicy
}

type probeEntry struct {
	Label      string         `json:"label"`
	OccurredAt string         `json:"occurred_at"`
	Data       map[string]any `json:"data"`
}

var (
	defaultClientMu sync.RWMutex
	defaultClient   *Client
)

func Init(config Config) *Client {
	client := New(config)
	defaultClientMu.Lock()
	defaultClient = client
	defaultClientMu.Unlock()
	return client
}

func New(config Config) *Client {
	resolved := config.resolve()
	client := &Client{
		config:        resolved,
		redactor:      redaction.New(resolved.redactFields),
		buffer:        make([]json.RawMessage, 0, resolved.batchSize),
		persistent:    map[string]any{},
		probes:        map[string][]probeEntry{},
		suppression:   newSuppressionTracker(),
		status:        StatusDisconnected,
		rand:          mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
		capturePolicy: balancedCapturePolicy(),
	}
	if !resolved.enabled {
		return client
	}

	client.transport = resolved.transport
	if client.transport == nil {
		client.transport = client.defaultTransport()
	}
	client.remoteConfigFetcher = resolved.remoteConfigFetcher
	if client.remoteConfigFetcher == nil && resolved.transport == nil && resolved.projectMode == ProjectModeConnected {
		client.remoteConfigFetcher = NewHTTPRemoteConfigFetcher(resolved.requestTimeout)
		client.remoteConfigURL = defaultRemoteConfigURL(resolved.endpoint)
	} else if client.remoteConfigFetcher != nil {
		client.remoteConfigURL = defaultRemoteConfigURL(resolved.endpoint)
	}
	if client.transport == nil {
		client.status = StatusDisconnected
		return client
	}
	client.status = StatusHealthy
	if client.remoteConfigFetcher != nil {
		if err := client.RefreshRemoteConfigNow(context.Background()); err != nil {
			client.mu.Lock()
			client.capturePolicy = minimalCapturePolicy()
			client.diagnostics = append(client.diagnostics, err.Error())
			client.mu.Unlock()
		}
	}
	return client
}

func (client *Client) defaultTransport() transport.Sender {
	useFileTransport := client.config.projectMode == ProjectModeLocalOnly || client.config.environment == "development" || client.config.environment == "local"
	if useFileTransport {
		fileTransport, err := transport.NewFileTransport(client.config.localEventsDir)
		if err == nil {
			return fileTransport
		}
		client.diagnostics = append(client.diagnostics, err.Error())
	}
	return transport.NewHTTPTransport(client.config.endpoint, client.config.requestTimeout)
}

func (client *Client) CaptureException(ctx context.Context, err error, options ...EventOption) {
	if err == nil {
		return
	}
	payload := map[string]any{
		"name":     fmt.Sprintf("%T", err),
		"message":  err.Error(),
		"handled":  true,
		"request":  emptyRequestPayload(),
		"response": emptyResponsePayload(),
		"runtime":  buildRuntimeFacts(),
		"stack":    string(debug.Stack()),
	}
	if client.config.probeFlushOnError {
		if probeData := client.snapshotProbes(); len(probeData) > 0 {
			payload["probe_data"] = probeData
		}
	}
	client.capture(ctx, "backend_exception", payload, options...)
}

func (client *Client) CaptureError(ctx context.Context, err error, options ...EventOption) {
	client.CaptureException(ctx, err, options...)
}

func (client *Client) CaptureLog(ctx context.Context, message string, level LogLevel, fields map[string]any, options ...EventOption) {
	if strings.TrimSpace(message) == "" || !shouldCaptureLog(client.config.logLevel, level) || !client.shouldCaptureLogByPolicy(level) {
		return
	}
	payload := map[string]any{
		"message":    message,
		"level":      normalizeLogLevel(level),
		"attributes": map[string]any{},
	}
	if len(fields) > 0 {
		payload["attributes"] = fields
	}
	client.capture(ctx, "log_event", payload, options...)
}

func (client *Client) CaptureMessage(ctx context.Context, message string, options ...MessageOption) {
	if strings.TrimSpace(message) == "" {
		return
	}
	payload := map[string]any{
		"message":    message,
		"level":      LevelInfo,
		"attributes": map[string]any{},
	}
	converted := make([]EventOption, 0, len(options))
	converted = append(converted, options...)
	client.capture(ctx, "log_event", payload, converted...)
}

func (client *Client) CaptureRequest(ctx context.Context, request *http.Request, response ResponseInfo, options ...EventOption) {
	if request == nil {
		return
	}
	if !client.shouldCaptureRequestByPolicy(response.StatusCode, request.URL.String(), request.Method) {
		return
	}
	if traceID := strings.TrimSpace(request.Header.Get("X-DebugBundle-Trace-Id")); traceID != "" && TraceIDFromContext(ctx) == "" {
		ctx = ContextWithTraceID(ctx, traceID)
	}
	if requestID := firstNonEmpty(strings.TrimSpace(request.Header.Get("X-Request-Id")), strings.TrimSpace(request.Header.Get("X-Correlation-Id"))); requestID != "" && RequestIDFromContext(ctx) == "" {
		ctx = ContextWithRequestID(ctx, requestID)
	}
	client.capture(ctx, "request_event", requestPayload(request, response), options...)
}

func (client *Client) SetContext(key string, value any) {
	if strings.TrimSpace(key) == "" {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if value == nil {
		delete(client.persistent, key)
		return
	}
	client.persistent[key] = value
}

func (client *Client) Probe(ctx context.Context, label string, data any, options ...ProbeOption) {
	client.recordProbe(ctx, label, data, options...)
}

func (client *Client) ProbeLazy(ctx context.Context, label string, data func() any, options ...ProbeOption) {
	if data == nil {
		return
	}
	probeOptions := probeOptions{}
	for _, option := range options {
		option.applyProbe(&probeOptions)
	}
	if probeOptions.heavy && !client.shouldActivateHeavyProbe(ctx, label) {
		return
	}
	client.recordProbe(ctx, label, data(), options...)
}

func (client *Client) recordProbe(ctx context.Context, label string, data any, options ...ProbeOption) {
	if strings.TrimSpace(label) == "" {
		return
	}
	redactedData := toObjectMap(client.redactor.Redact(data))
	now := time.Now().UTC()
	standaloneEvents := make([]map[string]any, 0)
	emitStandalone := false
	client.mu.Lock()
	if client.remoteConfigInitialized && !client.remoteConfigSnapshot.ProbesEnabled {
		client.mu.Unlock()
		return
	}
	if len(client.probes) >= client.config.maxProbeLabels {
		if _, exists := client.probes[label]; !exists {
			client.mu.Unlock()
			return
		}
	}
	entry := probeEntry{
		Label:      label,
		OccurredAt: now.Format(time.RFC3339Nano),
		Data:       redactedData,
	}
	client.probes[label] = append(client.probes[label], entry)
	if len(client.probes[label]) > client.config.maxProbeEntriesPerLabel {
		client.probes[label] = client.probes[label][len(client.probes[label])-client.config.maxProbeEntriesPerLabel:]
	}
	emitStandalone = client.capturePolicy.CaptureProbeEvents == CaptureProbeEventsStandaloneWhenActivated
	client.mu.Unlock()
	if emitStandalone {
		for _, directive := range client.matchingProbeDirectives(ctx, label, now) {
			standaloneEvents = append(standaloneEvents, map[string]any{
				"label":               label,
				"data":                redactedData,
				"activation_id":       directive.ID,
				"probe_label_pattern": directive.LabelPattern,
			})
		}
	}
	for _, payload := range standaloneEvents {
		client.capture(ctx, "probe_event", payload)
	}
}

func (client *Client) Flush(ctx context.Context) error {
	client.mu.Lock()
	if client.closed || client.transport == nil {
		client.mu.Unlock()
		return nil
	}
	now := time.Now().UTC()
	if !client.retryUntil.IsZero() && now.Before(client.retryUntil) {
		client.status = StatusDegraded
		client.mu.Unlock()
		return nil
	}
	batch := append([]json.RawMessage{}, client.buffer...)
	client.buffer = client.buffer[:0]
	aggregates := client.suppression.PendingAggregates(now)
	client.stopFlushTimerLocked()
	client.mu.Unlock()

	for _, aggregate := range aggregates {
		event := client.newEventEnvelope("error_suppressed", now, map[string]any{
			"fingerprint":      aggregate.Fingerprint,
			"suppressed_count": aggregate.Suppressed,
			"first_seen":       aggregate.FirstSeenAt.Format(time.RFC3339Nano),
			"last_seen":        aggregate.LastSeenAt.Format(time.RFC3339Nano),
			"window_seconds":   maxInt64(1, aggregate.WindowMillis/1000),
		})
		encoded, err := json.Marshal(event)
		if err == nil {
			batch = append(batch, encoded)
		}
	}
	if len(batch) == 0 {
		return nil
	}

	if len(batch) == 0 {
		return nil
	}

	response, err := client.transport.Send(ctx, transport.Request{
		ProjectToken: client.config.projectToken,
		Events:       batch,
	})
	client.mu.Lock()
	defer client.mu.Unlock()
	if err != nil {
		client.buffer = append(batch, client.buffer...)
		client.failures++
		client.status = StatusDisconnected
		return nil
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		client.buffer = append(batch, client.buffer...)
		client.failures++
		client.status = StatusDegraded
		retryAfter := response.RetryAfter
		if retryAfter <= 0 {
			retryAfter = defaultRetryBackoff(client.failures)
		}
		client.retryUntil = time.Now().Add(retryAfter)
		return nil
	}
	if response.StatusCode >= http.StatusBadRequest {
		client.status = StatusHealthy
		client.failures = 0
		return nil
	}
	client.status = StatusHealthy
	client.failures = 0
	successAt := time.Now().UTC()
	client.lastEventAt = &successAt
	client.retryUntil = time.Time{}
	return nil
}

func (client *Client) Status() SDKStatus {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.status
}

func (client *Client) LastEventAt() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.lastEventAt == nil {
		return nil
	}
	value := *client.lastEventAt
	return &value
}

func (client *Client) Close() error {
	client.mu.Lock()
	client.closed = true
	client.stopFlushTimerLocked()
	if client.remoteConfigTimer != nil {
		client.remoteConfigTimer.Stop()
		client.remoteConfigTimer = nil
	}
	client.mu.Unlock()
	if closer, ok := client.transport.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (client *Client) capture(ctx context.Context, eventType string, payload map[string]any, options ...EventOption) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.transport == nil || !client.shouldSample() {
		return
	}
	mergedContext := client.mergedContextLocked(ctx, options...)
	redactedContext := toObjectMap(client.redactor.Redact(mergedContext))
	redactedPayload := toObjectMap(client.redactor.Redact(payload))
	traceID := firstNonEmpty(TraceIDFromContext(ctx), stringValue(redactedContext["trace_id"]), stringValue(redactedPayload["trace_id"]))
	correlation := client.correlationPayload(ctx, redactedContext, redactedPayload, traceID)
	event := client.newEventEnvelope(eventType, time.Now().UTC(), redactedPayload)
	if correlation != nil {
		event.Correlation = correlation
	}
	if envelopeContext := eventContext(redactedContext); len(envelopeContext) > 0 {
		event.Context = envelopeContext
	}
	fingerprint := client.fingerprintForEvent(eventType, redactedPayload)
	if fingerprint != "" && !client.suppression.ShouldCapture(fingerprint, time.Now().UTC()) {
		return
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	client.buffer = append(client.buffer, encoded)
	client.scheduleFlushLocked(len(client.buffer) >= client.config.batchSize)
}

func (client *Client) newEventEnvelope(eventType string, occurredAt time.Time, payload map[string]any) EventEnvelope {
	return EventEnvelope{
		SchemaVersion: defaultSchemaVersion,
		EventID:       newEventID(),
		EventType:     eventType,
		ProjectToken:  client.config.projectToken,
		SDKName:       defaultSDKName,
		SDKVersion:    Version,
		Service: ServiceDescriptor{
			Name:        client.config.service,
			Runtime:     "go",
			Environment: client.config.environment,
		},
		OccurredAt: occurredAt.Format(time.RFC3339Nano),
		Payload:    payload,
	}
}

func (client *Client) correlationPayload(ctx context.Context, mergedContext map[string]any, redactedPayload map[string]any, traceID string) map[string]any {
	correlation := map[string]any{
		"request_id":   nil,
		"trace_id":     nil,
		"session_id":   nil,
		"user_id_hash": nil,
	}
	hasValue := false
	if requestID := firstNonEmpty(RequestIDFromContext(ctx), stringValue(mergedContext["request_id"]), stringValue(redactedPayload["request_id"])); requestID != "" {
		correlation["request_id"] = requestID
		hasValue = true
	}
	if traceID != "" {
		correlation["trace_id"] = traceID
		hasValue = true
	}
	if sessionID := firstNonEmpty(stringValue(mergedContext["session_id"]), stringValue(redactedPayload["session_id"])); sessionID != "" {
		correlation["session_id"] = sessionID
		hasValue = true
	}
	if userIDHash := firstNonEmpty(stringValue(mergedContext["user_id_hash"]), stringValue(redactedPayload["user_id_hash"])); userIDHash != "" {
		correlation["user_id_hash"] = userIDHash
		hasValue = true
	}
	if !hasValue {
		return nil
	}
	return correlation
}

func (client *Client) mergedContextLocked(ctx context.Context, options ...EventOption) map[string]any {
	merged := map[string]any{}
	for key, value := range client.persistent {
		merged[key] = value
	}
	for key, value := range ContextValues(ctx) {
		merged[key] = value
	}
	resolvedOptions := eventOptions{}
	for _, option := range options {
		option.apply(&resolvedOptions)
	}
	for key, value := range resolvedOptions.context {
		merged[key] = value
	}
	return merged
}

func eventContext(mergedContext map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range mergedContext {
		switch key {
		case "request_id", "trace_id", "session_id", "user_id_hash":
			continue
		default:
			result[key] = value
		}
	}
	return result
}

func (client *Client) scheduleFlushLocked(immediate bool) {
	if client.flushTimer != nil {
		if immediate {
			client.flushTimer.Stop()
			client.flushTimer = nil
		} else {
			return
		}
	}
	delay := client.config.flushInterval
	if immediate {
		delay = 0
	}
	client.flushTimer = time.AfterFunc(delay, func() {
		_ = client.Flush(context.Background())
	})
}

func (client *Client) stopFlushTimerLocked() {
	if client.flushTimer != nil {
		client.flushTimer.Stop()
		client.flushTimer = nil
	}
}

func (client *Client) fingerprintForEvent(eventType string, payload map[string]any) string {
	switch eventType {
	case "backend_exception":
		return fmt.Sprintf("%s:%s:%s", eventType, stringValue(payload["name"]), stringValue(payload["message"]))
	case "log_event":
		return fmt.Sprintf("%s:%s:%s", eventType, stringValue(payload["level"]), stringValue(payload["message"]))
	case "request_event":
		return fmt.Sprintf("%s:%s:%s:%v", eventType, stringValue(payload["method"]), stringValue(payload["path"]), payload["status_code"])
	default:
		return ""
	}
}

func (client *Client) shouldCaptureLogByPolicy(level LogLevel) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	switch client.capturePolicy.CaptureLogs {
	case CaptureLogsOff:
		return false
	case CaptureLogsError:
		return shouldCaptureLog(LevelError, level)
	case CaptureLogsWarning:
		return shouldCaptureLog(LevelWarning, level)
	case CaptureLogsInfo:
		return shouldCaptureLog(LevelInfo, level)
	default:
		return true
	}
}

func (client *Client) shouldCaptureRequestByPolicy(statusCode int, requestPath string, httpMethod string) bool {
	client.mu.Lock()
	policy := client.capturePolicy
	client.mu.Unlock()
	return shouldCaptureRequestByPolicy(statusCode, requestPath, httpMethod, policy)
}

func (client *Client) shouldActivateHeavyProbe(ctx context.Context, label string) bool {
	return len(client.matchingProbeDirectives(ctx, label, time.Now().UTC())) > 0
}

func (client *Client) matchingProbeDirectives(ctx context.Context, label string, now time.Time) []RemoteProbeDirective {
	requestDirectives := requestProbeDirectivesFromContext(ctx)
	client.mu.Lock()
	probesEnabled := client.remoteConfigSnapshot.ProbesEnabled
	remoteProbesEnabled := client.remoteConfigSnapshot.RemoteProbesEnabled
	remoteDirectives := append([]RemoteProbeDirective{}, client.remoteConfigSnapshot.Directives...)
	remoteConfigInitialized := client.remoteConfigInitialized
	client.mu.Unlock()
	if remoteConfigInitialized && !probesEnabled {
		return nil
	}
	directives := make([]RemoteProbeDirective, 0, len(requestDirectives)+len(remoteDirectives))
	directives = append(directives, requestDirectives...)
	if remoteProbesEnabled {
		directives = append(directives, remoteDirectives...)
	}
	if len(directives) == 0 {
		return nil
	}
	matches := make([]RemoteProbeDirective, 0, len(directives))
	for _, directive := range directives {
		if directive.Matches(label, client.config.service, client.config.environment, now) {
			matches = append(matches, directive)
		}
	}
	return matches
}

func (client *Client) RefreshRemoteConfigNow(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client.mu.Lock()
	fetcher := client.remoteConfigFetcher
	if fetcher == nil {
		client.mu.Unlock()
		return nil
	}
	request := RemoteConfigRequest{
		URL:          client.remoteConfigURL,
		ProjectToken: client.config.projectToken,
		IfNoneMatch:  client.remoteConfigETag,
		Timeout:      client.config.requestTimeout,
	}
	client.mu.Unlock()
	response, err := fetcher.Fetch(ctx, request)
	if err != nil {
		client.mu.Lock()
		client.scheduleRemoteConfigRetryLocked()
		client.mu.Unlock()
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if response.StatusCode == http.StatusNotModified {
		client.scheduleRemoteConfigRefreshLocked(client.remoteConfigSnapshot.PollInterval)
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		client.scheduleRemoteConfigRetryLocked()
		return fmt.Errorf("remote config fetch failed with status %d", response.StatusCode)
	}
	snapshot, err := parseRemoteConfig(response.Body, client.config.probesPollInterval, time.Now().UTC())
	if err != nil {
		client.scheduleRemoteConfigRetryLocked()
		return err
	}
	snapshot.ETag = response.ETag
	client.remoteConfigSnapshot = snapshot
	client.capturePolicy = snapshot.CapturePolicy
	client.remoteConfigETag = response.ETag
	client.remoteConfigInitialized = true
	if snapshot.RemoteProbesEnabled {
		client.scheduleRemoteConfigRefreshLocked(snapshot.PollInterval)
	} else if client.remoteConfigTimer != nil {
		client.remoteConfigTimer.Stop()
		client.remoteConfigTimer = nil
	}
	return nil
}

func (client *Client) scheduleRemoteConfigRefreshLocked(delay time.Duration) {
	if delay <= 0 {
		delay = client.config.probesPollInterval
	}
	if client.remoteConfigTimer != nil {
		client.remoteConfigTimer.Stop()
	}
	client.remoteConfigTimer = time.AfterFunc(delay, func() {
		_ = client.RefreshRemoteConfigNow(context.Background())
	})
}

func (client *Client) scheduleRemoteConfigRetryLocked() {
	if client.remoteConfigFetcher == nil || client.closed {
		return
	}
	if client.remoteConfigInitialized && !client.remoteConfigSnapshot.RemoteProbesEnabled {
		return
	}
	client.scheduleRemoteConfigRefreshLocked(client.config.probesPollInterval)
}

func (client *Client) shouldSample() bool {
	if client.config.sampleRate >= 1 {
		return true
	}
	if client.config.sampleRate <= 0 {
		return false
	}
	return client.rand.Float64() <= math.Max(0, math.Min(1, client.config.sampleRate))
}

func (client *Client) snapshotProbes() map[string]any {
	client.mu.Lock()
	defer client.mu.Unlock()
	labels := make([]string, 0, len(client.probes))
	for label := range client.probes {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	items := make([]any, 0)
	for _, label := range labels {
		entries := client.probes[label]
		for _, entry := range entries {
			items = append(items, map[string]any{
				"label":         label,
				"data":          entry.Data,
				"timestamp":     entry.OccurredAt,
				"activation_id": nil,
			})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return map[string]any{
		"version": 1,
		"items":   items,
	}
}

func toObjectMap(value any) map[string]any {
	if cast, ok := value.(map[string]any); ok {
		return cast
	}
	if cast, ok := value.(map[string]interface{}); ok {
		return cast
	}
	return map[string]any{"value": value}
}

func stringValue(value any) string {
	if cast, ok := value.(string); ok {
		return strings.TrimSpace(cast)
	}
	return ""
}

func maxInt64(left int64, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func defaultRetryBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	duration := time.Second << min(failures-1, 5)
	if duration > maxRetryBackoff {
		return maxRetryBackoff
	}
	return duration
}

func Recover(ctx context.Context) {
	if recovered := recover(); recovered != nil {
		if client := getDefaultClient(); client != nil {
			client.CaptureException(ctx, fmt.Errorf("panic recovered: %v", recovered))
		}
	}
}

func Go(ctx context.Context, runner func(context.Context)) {
	go func() {
		defer Recover(ctx)
		runner(ctx)
	}()
}

func CapturePanics() {
	if client := getDefaultClient(); client != nil {
		client.SetContext("panic_capture", true)
	}
}

func getDefaultClient() *Client {
	defaultClientMu.RLock()
	defer defaultClientMu.RUnlock()
	return defaultClient
}

func CaptureException(err error, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureException(ctx, err)
	}
}

func CaptureError(err error, ctxs ...context.Context) {
	CaptureException(err, ctxs...)
}

func CaptureLog(message string, level LogLevel, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureLog(ctx, message, level, nil)
	}
}

func CaptureLogWithContext(message string, level LogLevel, fields map[string]any, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureLog(ctx, message, level, fields)
	}
}

func CaptureRequest(request *http.Request, response ResponseInfo, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureRequest(ctx, request, response)
	}
}

func CaptureMessage(message string, options ...MessageOption) {
	if client := getDefaultClient(); client != nil {
		client.CaptureMessage(context.Background(), message, options...)
	}
}

func SetContext(key string, value any) {
	if client := getDefaultClient(); client != nil {
		client.SetContext(key, value)
	}
}

func Probe(label string, data any, options ...ProbeOption) {
	if client := getDefaultClient(); client != nil {
		client.Probe(context.Background(), label, data, options...)
	}
}

func ProbeLazy(label string, data func() any, options ...ProbeOption) {
	if client := getDefaultClient(); client != nil {
		client.ProbeLazy(context.Background(), label, data, options...)
	}
}

func Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if client := getDefaultClient(); client != nil {
		return client.Flush(ctx)
	}
	return nil
}

func Status() SDKStatus {
	if client := getDefaultClient(); client != nil {
		return client.Status()
	}
	return StatusDisconnected
}

func LastEventAt() *time.Time {
	if client := getDefaultClient(); client != nil {
		return client.LastEventAt()
	}
	return nil
}

func Diagnostics() []string {
	if client := getDefaultClient(); client != nil {
		client.mu.Lock()
		defer client.mu.Unlock()
		return append([]string{}, client.diagnostics...)
	}
	return nil
}

func ioReadAll(reader io.Reader) ([]byte, error) {
	return io.ReadAll(reader)
}

func stringsHasPrefix(value string, prefix string) bool {
	return strings.HasPrefix(value, prefix)
}

func stringsHasSuffix(value string, suffix string) bool {
	return strings.HasSuffix(value, suffix)
}

func stringsTrimSuffix(value string, suffix string) string {
	return strings.TrimSuffix(value, suffix)
}

func Hostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hostname
}
