package debugbundle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	triggerTokenPrefix         = "dbundle_probe_"
	triggerTokenQueryParameter = "_debug_probe"
	triggerTokenHeader         = "X-DebugBundle-Probe-Trigger"
)

type requestProbeDirectivesContextKey struct{}

type triggerTokenPayload struct {
	ActivationID     string `json:"activation_id"`
	LabelPattern     string `json:"label_pattern"`
	Service          string `json:"service"`
	Environment      string `json:"environment"`
	TriggerExpiresAt string `json:"trigger_expires_at"`
}

func (client *Client) ContextForRequest(request *http.Request) context.Context {
	if request == nil {
		return context.Background()
	}
	ctx := request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if traceID := strings.TrimSpace(request.Header.Get("X-DebugBundle-Trace-Id")); traceID != "" {
		ctx = ContextWithTraceID(ctx, traceID)
	}
	if requestID := strings.TrimSpace(firstNonEmpty(request.Header.Get("X-Request-Id"), request.Header.Get("X-Correlation-Id"))); requestID != "" {
		ctx = ContextWithRequestID(ctx, requestID)
	}
	client.mu.Lock()
	triggerTokenKey := client.remoteConfigSnapshot.TriggerTokenKey
	client.mu.Unlock()
	if directives := resolveRequestTriggerDirectives(request, triggerTokenKey, time.Now().UTC()); len(directives) > 0 {
		ctx = context.WithValue(ctx, requestProbeDirectivesContextKey{}, directives)
	}
	return ctx
}

func requestProbeDirectivesFromContext(ctx context.Context) []RemoteProbeDirective {
	if ctx == nil {
		return nil
	}
	directives, ok := ctx.Value(requestProbeDirectivesContextKey{}).([]RemoteProbeDirective)
	if !ok || len(directives) == 0 {
		return nil
	}
	return append([]RemoteProbeDirective{}, directives...)
}

func resolveRequestTriggerDirectives(request *http.Request, triggerTokenKey string, now time.Time) []RemoteProbeDirective {
	if request == nil || strings.TrimSpace(triggerTokenKey) == "" {
		return nil
	}
	token := extractRequestTriggerToken(request)
	if !strings.HasPrefix(token, triggerTokenPrefix) {
		return nil
	}
	encoded := strings.TrimPrefix(token, triggerTokenPrefix)
	separatorIndex := strings.Index(encoded, ".")
	if separatorIndex <= 0 || separatorIndex >= len(encoded)-1 {
		return nil
	}
	payloadSegment := encoded[:separatorIndex]
	signatureSegment := encoded[separatorIndex+1:]
	if !hasValidTriggerTokenSignature(payloadSegment, signatureSegment, triggerTokenKey) {
		return nil
	}
	payload, ok := decodeTriggerTokenPayload(payloadSegment)
	if !ok {
		return nil
	}
	if !payload.ExpiresAt.After(now) {
		return nil
	}
	return []RemoteProbeDirective{payload}
}

func extractRequestTriggerToken(request *http.Request) string {
	if request == nil {
		return ""
	}
	if token := strings.TrimSpace(request.Header.Get(triggerTokenHeader)); token != "" {
		return token
	}
	return strings.TrimSpace(request.URL.Query().Get(triggerTokenQueryParameter))
}

func hasValidTriggerTokenSignature(payloadSegment, signatureSegment, triggerTokenKey string) bool {
	mac := hmac.New(sha256.New, []byte(triggerTokenKey))
	if _, err := mac.Write([]byte(payloadSegment)); err != nil {
		return false
	}
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(signatureSegment)
	if err != nil || len(actual) != len(expected) {
		return false
	}
	return hmac.Equal(expected, actual)
}

func decodeTriggerTokenPayload(payloadSegment string) (RemoteProbeDirective, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(payloadSegment)
	if err != nil {
		return RemoteProbeDirective{}, false
	}
	var payload triggerTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return RemoteProbeDirective{}, false
	}
	if strings.TrimSpace(payload.ActivationID) == "" || strings.TrimSpace(payload.LabelPattern) == "" || strings.TrimSpace(payload.Service) == "" || strings.TrimSpace(payload.Environment) == "" || strings.TrimSpace(payload.TriggerExpiresAt) == "" {
		return RemoteProbeDirective{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, normalizeRFC3339(payload.TriggerExpiresAt))
	if err != nil {
		return RemoteProbeDirective{}, false
	}
	return RemoteProbeDirective{
		ID:           strings.TrimSpace(payload.ActivationID),
		LabelPattern: strings.TrimSpace(payload.LabelPattern),
		Service:      strings.TrimSpace(payload.Service),
		Environment:  strings.TrimSpace(payload.Environment),
		ExpiresAt:    expiresAt,
	}, true
}
