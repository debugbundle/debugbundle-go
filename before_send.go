package debugbundle

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
)

type BeforeSendFunc func(EventEnvelope) *EventEnvelope

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var requiredPayloadFields = map[string][]string{
	"backend_exception":   {"name", "message", "stack", "handled", "request", "response", "runtime"},
	"request_event":       {"method", "path", "query", "headers", "response_status", "duration_ms"},
	"log_event":           {"level", "message", "attributes"},
	"frontend_breadcrumb": {"breadcrumb_type", "data"},
	"frontend_exception":  {"name", "message", "stack"},
	"deploy_metadata":     {"commit_sha", "version", "branch", "environment", "deployed_at"},
	"error_suppressed":    {"fingerprint", "suppressed_count", "window_seconds", "first_seen", "last_seen"},
	"probe_event":         {"label", "data", "activation_id", "probe_label_pattern"},
}

var allowedPayloadFields = map[string]map[string]struct{}{
	"backend_exception": stringSet("name", "message", "stack", "handled", "request", "response", "runtime", "probe_data"),
	"request_event": stringSet(
		"method", "path", "query", "headers", "body", "response_status", "duration_ms",
		"route_template", "response_headers", "response_body", "device",
	),
	"log_event":           stringSet("level", "message", "attributes", "device"),
	"frontend_breadcrumb": stringSet("breadcrumb_type", "route", "data", "device"),
	"frontend_exception": stringSet(
		"name", "message", "stack", "route", "browser", "breadcrumbs", "device",
		"browser_event", "rejection_reason", "dom_context", "probe_data",
	),
	"deploy_metadata":  stringSet("commit_sha", "version", "branch", "environment", "deployed_at"),
	"error_suppressed": stringSet("fingerprint", "suppressed_count", "window_seconds", "first_seen", "last_seen", "device"),
	"probe_event":      stringSet("label", "data", "activation_id", "probe_label_pattern", "device"),
}

func applyBeforeSend(event EventEnvelope, hook BeforeSendFunc) (*EventEnvelope, string) {
	if hook == nil {
		return &event, ""
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		return &event, "before_send_clone_failed"
	}
	var clone EventEnvelope
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return &event, "before_send_clone_failed"
	}

	result, panicked := invokeBeforeSend(hook, clone)
	if panicked {
		return &event, "before_send_failed"
	}
	if result == nil {
		return nil, ""
	}
	if !validBeforeSendEvent(*result) {
		return &event, "before_send_invalid_event"
	}
	return result, ""
}

func invokeBeforeSend(hook BeforeSendFunc, event EventEnvelope) (result *EventEnvelope, panicked bool) {
	defer func() {
		if recover() != nil {
			result = nil
			panicked = true
		}
	}()
	return hook(event), false
}

func validBeforeSendEvent(event EventEnvelope) bool {
	if strings.TrimSpace(event.SchemaVersion) == "" ||
		strings.TrimSpace(event.EventID) == "" ||
		strings.TrimSpace(event.EventType) == "" ||
		strings.TrimSpace(event.SDKName) == "" ||
		strings.TrimSpace(event.SDKVersion) == "" ||
		strings.TrimSpace(event.Service.Name) == "" ||
		strings.TrimSpace(event.Service.Environment) == "" ||
		event.Payload == nil {
		return false
	}
	if !uuidPattern.MatchString(event.EventID) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, event.OccurredAt); err != nil {
		return false
	}
	fields, exists := requiredPayloadFields[event.EventType]
	if !exists {
		return false
	}
	for _, field := range fields {
		if _, exists := event.Payload[field]; !exists {
			return false
		}
	}
	allowed := allowedPayloadFields[event.EventType]
	for field := range event.Payload {
		if _, exists := allowed[field]; !exists {
			return false
		}
	}
	return hasValidPayloadShape(event.EventType, event.Payload)
}

func hasValidPayloadShape(eventType string, payload map[string]any) bool {
	switch eventType {
	case "backend_exception":
		return nonEmptyStrings(payload, "name", "message", "stack") &&
			isBool(payload["handled"]) &&
			isMap(payload["request"]) &&
			isMap(payload["response"]) &&
			isMap(payload["runtime"]) &&
			optionalMap(payload, "probe_data")
	case "request_event":
		return nonEmptyStrings(payload, "method", "path") &&
			isMap(payload["query"]) &&
			isMap(payload["headers"]) &&
			nonNegativeNumber(payload["response_status"]) &&
			nonNegativeNumber(payload["duration_ms"]) &&
			optionalMap(payload, "response_headers")
	case "log_event":
		return nonEmptyStrings(payload, "level", "message") && isMap(payload["attributes"])
	case "frontend_breadcrumb":
		return nonEmptyStrings(payload, "breadcrumb_type") && isMap(payload["data"])
	case "frontend_exception":
		return nonEmptyStrings(payload, "name", "message", "stack") &&
			optionalSlice(payload, "breadcrumbs") &&
			optionalMap(payload, "probe_data")
	case "deploy_metadata":
		return nonEmptyStrings(payload, "commit_sha", "version", "branch", "environment") &&
			isTimestamp(payload["deployed_at"])
	case "error_suppressed":
		return nonEmptyStrings(payload, "fingerprint") &&
			nonNegativeInteger(payload["suppressed_count"]) &&
			positiveInteger(payload["window_seconds"]) &&
			isTimestamp(payload["first_seen"]) &&
			isTimestamp(payload["last_seen"])
	case "probe_event":
		return nonEmptyStrings(payload, "label", "probe_label_pattern") &&
			isMap(payload["data"]) &&
			nullableUUID(payload["activation_id"])
	default:
		return false
	}
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func nonEmptyStrings(payload map[string]any, fields ...string) bool {
	for _, field := range fields {
		value, ok := payload[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func isBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

func isMap(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func optionalMap(payload map[string]any, field string) bool {
	value, exists := payload[field]
	return !exists || isMap(value)
}

func optionalSlice(payload map[string]any, field string) bool {
	value, exists := payload[field]
	if !exists {
		return true
	}
	_, ok := value.([]any)
	return ok
}

func nonNegativeNumber(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && number >= 0
}

func nonNegativeInteger(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && number >= 0 && number == math.Trunc(number)
}

func positiveInteger(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && number > 0 && number == math.Trunc(number)
}

func isTimestamp(value any) bool {
	timestamp, ok := value.(string)
	if !ok {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, timestamp)
	return err == nil
}

func nullableUUID(value any) bool {
	if value == nil {
		return true
	}
	uuid, ok := value.(string)
	return ok && uuidPattern.MatchString(uuid)
}
