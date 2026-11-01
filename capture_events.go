package debugbundle

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/debugbundle/debugbundle-go/v3/redaction"
)

func (client *Client) CaptureException(ctx context.Context, err error, options ...EventOption) {
	defer func() { _ = recover() }()
	if err == nil {
		return
	}
	if !client.mayPrepareCapture(true) {
		return
	}
	stack := string(debug.Stack())
	if len(stack) > 16_384 {
		stack = stack[:16_384]
	}
	snapshot := safeErrorSnapshot(err, stack, client.config.captureExceptionEvidence && client.evidence.Supports("exception_evidence_v1"), false)
	payload := map[string]any{
		"name":     fmt.Sprintf("%T", err),
		"message":  snapshot.message,
		"handled":  true,
		"request":  emptyRequestPayload(),
		"response": emptyResponsePayload(),
		"runtime":  buildRuntimeFacts(),
		"stack":    stack,
	}
	if snapshot.graph != nil {
		payload["exception_evidence"] = snapshot.graph
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
	if strings.TrimSpace(message) == "" {
		return
	}
	if !client.shouldCaptureLogLevel(level) {
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
	if !client.shouldCaptureLogLevel(LevelWarning) {
		return
	}
	payload := map[string]any{
		"message":    message,
		"level":      LevelWarning,
		"attributes": map[string]any{},
	}
	converted := make([]EventOption, 0, len(options))
	converted = append(converted, options...)
	client.capture(ctx, "log_event", payload, converted...)
}

func (client *Client) CaptureRequest(ctx context.Context, request *http.Request, response ResponseInfo, options ...EventOption) {
	if request == nil || request.URL == nil ||
		!client.shouldCaptureRequestMetadata(response.StatusCode, request.URL.Path, request.Method) {
		return
	}
	if !client.mayPrepareCapture(response.StatusCode >= 500) {
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

func (client *Client) shouldCaptureRequestMetadata(statusCode int, path string, method string) bool {
	if !client.mu.TryLock() {
		client.contentionDrops.Add(1)
		return false
	}
	defer client.mu.Unlock()
	return !client.closed && client.transport != nil &&
		shouldCaptureRequestByPolicy(statusCode, path, method, client.capturePolicy)
}

func (client *Client) SetContext(key string, value any) {
	if strings.TrimSpace(key) == "" {
		return
	}
	if value == nil {
		client.mu.Lock()
		delete(client.persistent, key)
		client.mu.Unlock()
		return
	}
	protected, err := redaction.ProtectTelemetry(map[string]any{key: value}, client.config.redactFields)
	if err != nil {
		return
	}
	fields, ok := protected.(map[string]any)
	if !ok {
		return
	}
	client.mu.Lock()
	client.persistent[key] = fields[key]
	client.mu.Unlock()
}
