package debugbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/redaction"
	"github.com/debugbundle/debugbundle-go/v3/transport"
)

type Client struct {
	diagnosticBudget        *diagnosticCaptureBudget
	operations              *operationRegistry
	evidence                *evidence.State
	mu                      sync.Mutex
	sendMu                  sync.Mutex
	refreshMu               sync.Mutex
	config                  resolvedConfig
	transport               transport.Sender
	redactor                *redaction.Redactor
	buffer                  []queuedEvent
	bufferBytes             int
	pendingLowPriority      int
	inFlightCount           int
	inFlightBytes           int
	flushActive             bool
	pressureDrops           int
	contentionDrops         atomic.Int64
	pressureFirstSeen       time.Time
	pressureLastSeen        time.Time
	lastPressureReport      time.Time
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
	remoteConfigReady       chan struct{}
	remoteConfigCancel      context.CancelFunc
	capturePolicy           CapturePolicy
}

const maxPendingEvents = 1_000
const maxPendingBytes = 8 * 1024 * 1024

type queuedEvent struct {
	fatal        bool
	diagnosticID string
	encoded      json.RawMessage
	highPriority bool
	finalized    bool
}

type probeEntry struct {
	Label     string         `json:"label"`
	Timestamp string         `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

var (
	defaultClientMu   sync.RWMutex
	defaultClient     *Client
	errorRendererSlot = make(chan struct{}, 1)
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
		diagnosticBudget:  newDiagnosticCaptureBudget(),
		operations:        &operationRegistry{},
		evidence:          evidence.NewState(resolved.evidenceCapabilities),
		config:            resolved,
		redactor:          redaction.New(resolved.redactFields),
		buffer:            make([]queuedEvent, 0, min(resolved.batchSize, maxPendingEvents)),
		persistent:        map[string]any{},
		probes:            map[string][]probeEntry{},
		suppression:       newSuppressionTracker(),
		status:            StatusDisconnected,
		rand:              mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
		capturePolicy:     balancedCapturePolicy(),
		remoteConfigReady: make(chan struct{}),
	}
	if !resolved.enabled {
		close(client.remoteConfigReady)
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
		close(client.remoteConfigReady)
		return client
	}
	client.status = StatusHealthy
	if client.remoteConfigFetcher != nil {
		client.capturePolicy = minimalCapturePolicy()
		refreshContext, cancel := context.WithCancel(context.Background())
		client.remoteConfigCancel = cancel
		go func() {
			defer close(client.remoteConfigReady)
			if err := client.RefreshRemoteConfigNow(refreshContext); err != nil {
				client.recordDiagnostic(err.Error())
			}
		}()
	} else {
		close(client.remoteConfigReady)
	}
	return client
}

func (client *Client) defaultTransport() transport.Sender {
	useFileTransport := client.config.projectMode == ProjectModeLocalOnly || client.config.environment == "development" || client.config.environment == "local"
	if useFileTransport {
		return &lazyFileTransport{root: client.config.localEventsDir}
	}
	return transport.NewHTTPTransport(client.config.endpoint, client.config.requestTimeout)
}

// File validation and directory creation belong to the sender, never the constructor.
type lazyFileTransport struct {
	root     string
	once     sync.Once
	delegate *transport.FileTransport
	err      error
}

func (sender *lazyFileTransport) Send(ctx context.Context, request transport.Request) (transport.Response, error) {
	sender.once.Do(func() {
		sender.delegate, sender.err = transport.NewFileTransport(sender.root)
	})
	if sender.err != nil {
		return transport.Response{}, sender.err
	}
	return sender.delegate.Send(ctx, request)
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
	value, succeeded := resolveProbeCallback(data)
	if !succeeded {
		return
	}
	client.recordProbe(ctx, label, value, options...)
}

func (client *Client) recordProbe(ctx context.Context, label string, data any, options ...ProbeOption) {
	if strings.TrimSpace(label) == "" {
		return
	}
	protectedData, err := redaction.ProtectTelemetry(client.redactor.Redact(data), client.config.redactFields)
	if err != nil {
		return
	}
	redactedData := toObjectMap(protectedData)
	protectedLabel, err := redaction.ProtectTelemetry(label, client.config.redactFields)
	if err != nil {
		return
	}
	label, ok := protectedLabel.(string)
	if !ok {
		return
	}
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
		Label:     label,
		Timestamp: now.Format(time.RFC3339Nano),
		Data:      redactedData,
	}
	client.probes[label] = append(client.probes[label], entry)
	if len(client.probes[label]) > client.config.maxProbeEntriesPerLabel {
		client.probes[label] = client.probes[label][len(client.probes[label])-client.config.maxProbeEntriesPerLabel:]
	}
	emitStandalone = true
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
	if client.operations != nil {
		client.operations.stopped.Store(true)
	}
	client.mu.Lock()
	client.closed = true
	clear(client.buffer)
	client.buffer = nil
	client.bufferBytes = 0
	client.pendingLowPriority = 0
	client.diagnosticBudget.clear()
	if client.remoteConfigCancel != nil {
		client.remoteConfigCancel()
	}
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

func (client *Client) shouldCaptureLogLevel(level LogLevel) bool {
	if !shouldCaptureLog(client.config.logLevel, level) {
		return false
	}
	if !client.mu.TryLock() {
		client.contentionDrops.Add(1)
		return false
	}
	defer client.mu.Unlock()
	return !client.closed && client.transport != nil && client.captureLogLevelAllowedLocked(level)
}

func (client *Client) captureLogLevelAllowedLocked(level LogLevel) bool {
	if !shouldCaptureLog(client.config.logLevel, level) {
		return false
	}
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

func (client *Client) recordDiagnostic(code string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.diagnostics = append(client.diagnostics, code)
	if len(client.diagnostics) > 100 {
		client.diagnostics = append([]string{}, client.diagnostics[len(client.diagnostics)-100:]...)
	}
}

func resolveProbeCallback(callback func() any) (value any, succeeded bool) {
	defer func() {
		if recover() != nil {
			value = nil
			succeeded = false
		}
	}()
	return callback(), true
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

func (client *Client) mergedContextWithOwner(persistent map[string]any, ctx context.Context, options ...EventOption) (map[string]any, *operationLease) {
	merged := persistent
	values, owner := captureContextValues(ctx)
	for key, value := range values {
		merged[key] = value
	}
	resolvedOptions := eventOptions{}
	for _, option := range options {
		option.apply(&resolvedOptions)
	}
	for key, value := range resolvedOptions.context {
		merged[key] = value
	}
	if resolvedOptions.failureRef != "" && client.config.captureRecoveryObservations && client.evidence.Supports("recovery_v1") {
		if scope := client.resolveEvidenceScope(owner, resolvedOptions.failureScope); scope != nil {
			merged["evidence_reference"] = map[string]any{"version": 1, "failure_ref": resolvedOptions.failureRef, "scope": scope}
		}
	}
	return merged, owner
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
	if !client.refreshMu.TryLock() {
		return nil
	}
	defer client.refreshMu.Unlock()
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
	response, err := fetchRemoteConfigWithoutPanic(fetcher, ctx, request)
	if err != nil {
		client.observeRemoteEvidence(nil)
		client.mu.Lock()
		client.scheduleRemoteConfigRetryLocked()
		client.mu.Unlock()
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		return nil
	}
	if response.StatusCode == http.StatusNotModified {
		client.scheduleRemoteConfigRefreshLocked(client.remoteConfigSnapshot.PollInterval)
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		client.observeRemoteEvidence(nil)
		client.scheduleRemoteConfigRetryLocked()
		return fmt.Errorf("remote config fetch failed with status %d", response.StatusCode)
	}
	snapshot, err := parseRemoteConfig(response.Body, client.config.probesPollInterval, time.Now().UTC())
	if err != nil {
		client.observeRemoteEvidence(nil)
	} else {
		client.observeRemoteEvidence(snapshot.EvidenceCapabilities)
	}
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
				"timestamp":     entry.Timestamp,
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

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
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
