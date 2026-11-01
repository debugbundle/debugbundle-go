package debugbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
)

type contextKey string

const (
	contextValuesKey    contextKey = "debugbundle.values"
	contextTraceIDKey   contextKey = "debugbundle.trace_id"
	contextRequestIDKey contextKey = "debugbundle.request_id"
)

func ContextWithValue(ctx context.Context, key string, value any) context.Context {
	values, lease := captureContextValues(ctx)
	values[key] = value
	if lease != nil {
		return context.WithValue(ctx, contextValuesKey, operationContextValues{values, lease})
	}
	return context.WithValue(ctx, contextValuesKey, values)
}

func ContextWithUserHash(ctx context.Context, value string) context.Context {
	return ContextWithValue(ctx, "user_id_hash", stableHash(value))
}

func ContextWithRequestID(ctx context.Context, value string) context.Context {
	ctx = context.WithValue(ctx, contextRequestIDKey, value)
	return ContextWithValue(ctx, "request_id", value)
}

func ContextWithTraceID(ctx context.Context, value string) context.Context {
	ctx = context.WithValue(ctx, contextTraceIDKey, value)
	return ContextWithValue(ctx, "trace_id", value)
}

func ContextValues(ctx context.Context) map[string]any {
	values, _ := captureContextValues(ctx)
	return values
}
func captureContextValues(ctx context.Context) (map[string]any, *operationLease) {
	if ctx == nil {
		return map[string]any{}, nil
	}
	var values map[string]any
	var lease *operationLease
	switch stored := ctx.Value(contextValuesKey).(type) {
	case map[string]any:
		values = stored
	case operationContextValues:
		values = stored.values
		lease = stored.lease
	}
	cloned := map[string]any{}
	maps.Copy(cloned, values)
	return cloned, lease
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(contextTraceIDKey).(string); ok {
		return value
	}
	if values, ok := ContextValues(ctx)["trace_id"].(string); ok {
		return values
	}
	return ""
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(contextRequestIDKey).(string); ok {
		return value
	}
	if values, ok := ContextValues(ctx)["request_id"].(string); ok {
		return values
	}
	return ""
}

func stableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
