package evidence

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/debugbundle/debugbundle-go/v3/redaction"
)

const (
	maxProtectedEventBytes = 256 * 1024
	maxSafeJSONInteger     = 9007199254740991
)

var (
	allowedHTMLResourceTags = stringSet("script", "link", "img", "audio", "video", "source", "iframe", "object", "embed", "input")
	allowedReadyStates      = stringSet("loading", "interactive", "complete")
	allowedVisibilityStates = stringSet("visible", "hidden", "prerender", "unloaded")
	allowedRejectionKinds   = stringSet("error", "string", "object", "null", "undefined", "unknown")
	allowedLimitReasons     = stringSet("budget_exceeded", "unsafe", "unsupported", "unknown")
	allowedLimitKeys        = stringSet("version", "exception_evidence", "execution", "resource_observations")
	allowedBrowserEventKeys = stringSet("kind", "message", "file_name", "line_number", "column_number", "target", "page", "opaque")
	allowedTargetKeys       = stringSet("tag_name", "source_url", "attributes")
	allowedTargetAttrStrs   = []string{"rel", "as", "type", "media", "cross_origin"}
	allowedTargetAttrBools  = []string{"async", "defer", "integrity_present"}
	allowedTargetAttrKeys   = stringSet("rel", "as", "type", "media", "cross_origin", "async", "defer", "integrity_present")
	allowedPageKeys         = stringSet("url", "referrer", "ready_state", "visibility_state")
	allowedRejectionKeys    = stringSet("kind", "name", "message", "preview")
	allowedRuntimes         = stringSet("node", "browser", "python", "php", "jvm", "go", "ruby", "dotnet", "android", "swift", "react_native", "unknown")
	allowedTraceOrigins     = stringSet("throw", "native_fatal", "capture", "runtime_reported", "unavailable", "unknown")
	allowedParsingStatuses  = stringSet("parsed", "partial", "unparsed", "unavailable", "truncated")
	allowedClassifications  = stringSet("application", "framework", "sdk", "unknown")
	allowedEdgeKinds        = stringSet("cause", "context", "aggregate", "suppressed")
	allowedUnavailable      = stringSet("unknown", "unsupported", "unsafe", "budget_exceeded")
	allowedExecutionKinds   = stringSet("request", "command", "job", "unknown")
	allowedAvailabilities   = stringSet("observed", "unsupported", "no_match", "withheld", "ambiguous", "unknown")
	allowedDispositions     = stringSet("enforce", "report")
	allowedRecoveryStates   = stringSet("attempt_started", "attempt_completed", "attempt_failed", "recovery_observed", "failure_persisted")
	allowedRecoveryKeys     = stringSet("version", "kind", "failure_ref", "scope", "attempt_id", "state", "operation", "status_code", "duration_ms")
	effectiveDirectiveRegex = regexp.MustCompile(`^[a-z-]{1,64}$`)
	browserStackURLRegex    = regexp.MustCompile(`(?i)https?://[^\s]+`)
	browserStackCoordRegex  = regexp.MustCompile(`:\d+(?::\d+)?[),]*$`)
	browserStackPunctRegex  = regexp.MustCompile(`[),]+$`)
)

// ProtectEventFields enforces mandatory + custom redaction while preserving validated
// closed SDK-owned protocol structures against broad custom RedactFields collisions.
func ProtectEventFields(
	eventType string,
	payload map[string]any,
	service any,
	context map[string]any,
	options redaction.TelemetryOptions,
) (map[string]any, map[string]any, map[string]any, bool) {
	if payload == nil {
		return nil, nil, nil, false
	}
	rawPayload := shallowCloneMap(payload)
	preservedPayload := map[string]any{}
	var diagnosticPayload map[string]any

	if eventType == "diagnostic_context" {
		diagnosticPayload = sanitizeDiagnosticContext(rawPayload, options, true)
		if diagnosticPayload == nil {
			return nil, nil, nil, false
		}
	} else {
		if eventType == "frontend_exception" {
			if raw, exists := rawPayload["browser_event"]; exists && raw != nil {
				if safe := sanitizeBrowserEvent(raw, options, true); safe != nil {
					preservedPayload["browser_event"] = safe
					delete(rawPayload, "browser_event")
				}
			}
			if raw, exists := rawPayload["rejection_reason"]; exists && raw != nil {
				if safe := sanitizeRejectionReason(raw, options, true); safe != nil {
					preservedPayload["rejection_reason"] = safe
					delete(rawPayload, "rejection_reason")
				}
			}
			if raw, exists := rawPayload["resource_observations"]; exists && raw != nil {
				if safe := sanitizeResourceObservations(raw, options, true); safe != nil {
					preservedPayload["resource_observations"] = safe
					delete(rawPayload, "resource_observations")
				}
			}
		}
		if eventType == "backend_exception" || eventType == "frontend_exception" {
			if raw, exists := rawPayload["exception_evidence"]; exists && raw != nil {
				if safe := sanitizeExceptionEvidence(raw, options, true); safe != nil {
					preservedPayload["exception_evidence"] = safe
					delete(rawPayload, "exception_evidence")
				}
			}
		}
		if eventType == "backend_exception" {
			if raw, exists := rawPayload["execution"]; exists && raw != nil {
				if safe := sanitizeExecution(raw, options, true); safe != nil {
					preservedPayload["execution"] = safe
					delete(rawPayload, "execution")
				}
			}
		}
		if eventType == "error_suppressed" {
			if reason, ok := rawPayload["reason"].(string); ok && reason == "queue_pressure" {
				preservedPayload["reason"] = "queue_pressure"
				delete(rawPayload, "reason")
			}
		}
	}

	var contextInput any
	preservedContext := map[string]any{}
	if context != nil {
		rawContext := shallowCloneMap(context)
		if raw, exists := rawContext["evidence_reference"]; exists {
			if normalized, err := CopyJSON(raw); err == nil && Reference(normalized) {
				preservedContext["evidence_reference"] = normalized
				delete(rawContext, "evidence_reference")
			}
		}
		if raw, exists := rawContext["evidence_limits"]; exists {
			if normalized, err := CopyJSON(raw); err == nil && isValidEvidenceLimits(normalized) {
				preservedContext["evidence_limits"] = normalized
				delete(rawContext, "evidence_limits")
			}
		}
		contextInput = rawContext
	}

	payloadInput := rawPayload
	if diagnosticPayload != nil {
		payloadInput = map[string]any{}
	}
	protected, err := redaction.ProtectTelemetryWithOptions(map[string]any{
		"payload": payloadInput,
		"context": contextInput,
		"service": service,
	}, options)
	if err != nil {
		return nil, nil, nil, false
	}
	fields, ok := protected.(map[string]any)
	if !ok {
		return nil, nil, nil, false
	}
	protectedService, ok := fields["service"].(map[string]any)
	if !ok {
		return nil, nil, nil, false
	}
	var protectedPayload map[string]any
	if diagnosticPayload != nil {
		protectedPayload = diagnosticPayload
	} else {
		protectedPayload, ok = fields["payload"].(map[string]any)
		if !ok {
			return nil, nil, nil, false
		}
		for key, value := range preservedPayload {
			protectedPayload[key] = value
		}
	}
	var protectedContext map[string]any
	if context != nil {
		protectedContext, ok = fields["context"].(map[string]any)
		if !ok {
			return nil, nil, nil, false
		}
		for key, value := range preservedContext {
			protectedContext[key] = value
		}
	}
	assembled := map[string]any{"payload": protectedPayload, "service": protectedService}
	if context != nil {
		assembled["context"] = protectedContext
	}
	encoded, err := json.Marshal(assembled)
	if err != nil || len(encoded) > maxProtectedEventBytes {
		return nil, nil, nil, false
	}
	return protectedPayload, protectedService, protectedContext, true
}

// IsValidProtocolContextField reports whether a context entry is a closed SDK protocol record.
func IsValidProtocolContextField(key string, value any) bool {
	if key != "evidence_reference" && key != "evidence_limits" {
		return false
	}
	normalized, err := CopyJSON(value)
	if err != nil {
		return false
	}
	if key == "evidence_reference" {
		return Reference(normalized)
	}
	return isValidEvidenceLimits(normalized)
}

// IsValidDiagnosticPayload reports whether a diagnostic_context payload matches the closed schema.
func IsValidDiagnosticPayload(value any) bool {
	return sanitizeDiagnosticContext(value, redaction.TelemetryOptions{}, false) != nil
}

// IsValidProtocolPayloadField reports whether a payload entry is a validated closed SDK protocol record.
func IsValidProtocolPayloadField(eventType, key string, value any) bool {
	empty := redaction.TelemetryOptions{}
	switch key {
	case "exception_evidence":
		return (eventType == "backend_exception" || eventType == "frontend_exception") &&
			sanitizeExceptionEvidence(value, empty, false) != nil
	case "execution":
		return eventType == "backend_exception" && sanitizeExecution(value, empty, false) != nil
	case "resource_observations":
		return eventType == "frontend_exception" && sanitizeResourceObservations(value, empty, false) != nil
	case "browser_event":
		return eventType == "frontend_exception" && sanitizeBrowserEvent(value, empty, false) != nil
	case "rejection_reason":
		return eventType == "frontend_exception" && sanitizeRejectionReason(value, empty, false) != nil
	case "reason":
		reason, ok := value.(string)
		return eventType == "error_suppressed" && ok && reason == "queue_pressure"
	default:
		return false
	}
}

func shallowCloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func hasOnlyAllowedKeys(fields map[string]any, allowed map[string]bool) bool {
	for key := range fields {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func isFiniteInt(value any, minimum, maximum float64) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) &&
		number == math.Trunc(number) && number >= minimum && number <= maximum
}

func isFiniteNumber(value any, minimum float64) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= minimum
}

func utf16Length(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 65535 {
			length++
		}
	}
	return length
}

func asciiLower(value string) string {
	bytes := []byte(value)
	for index, b := range bytes {
		if b >= 'A' && b <= 'Z' {
			bytes[index] = b + ('a' - 'A')
		}
	}
	return string(bytes)
}

func scrubProtocolString(value string, options redaction.TelemetryOptions, scrub bool) (string, bool) {
	if !scrub {
		return value, true
	}
	protected, err := redaction.ProtectTelemetryWithOptions(value, options)
	if err != nil {
		return "", false
	}
	cleaned, ok := protected.(string)
	return cleaned, ok
}

func scrubNullableProtocolString(value any, options redaction.TelemetryOptions, scrub bool) (any, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	return scrubProtocolString(text, options, scrub)
}

func sanitizeBrowserStackURLs(stack string) string {
	return browserStackURLRegex.ReplaceAllStringFunc(stack, func(source string) string {
		suffix := browserStackCoordRegex.FindString(source)
		if suffix == "" {
			suffix = browserStackPunctRegex.FindString(source)
		}
		location := source[:len(source)-len(suffix)]
		parsed, err := url.Parse(location)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "[unavailable-url]" + suffix
		}
		path := parsed.EscapedPath()
		if path == "" {
			path = "/"
		}
		return strings.ToLower(parsed.Scheme) + "://" + parsed.Host + path + suffix
	})
}

func isValidEvidenceLimits(fields map[string]any) bool {
	if !hasOnlyAllowedKeys(fields, allowedLimitKeys) || fields["version"] != float64(1) {
		return false
	}
	for key, value := range fields {
		if key == "version" {
			continue
		}
		reason, ok := value.(string)
		if !ok || !allowedLimitReasons[reason] {
			return false
		}
	}
	return true
}

func sanitizeBrowserEvent(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || !hasOnlyAllowedKeys(fields, allowedBrowserEventKeys) {
		return nil
	}
	for _, required := range []string{"kind", "message", "file_name", "line_number", "column_number", "target", "opaque"} {
		if _, exists := fields[required]; !exists {
			return nil
		}
	}
	kind, ok := fields["kind"].(string)
	if !ok || (kind != "window_error" && kind != "resource_error") {
		return nil
	}
	opaque, ok := fields["opaque"].(bool)
	if !ok {
		return nil
	}
	lineNumber := fields["line_number"]
	columnNumber := fields["column_number"]
	if (lineNumber != nil && !isFiniteInt(lineNumber, 0, maxSafeJSONInteger)) ||
		(columnNumber != nil && !isFiniteInt(columnNumber, 0, maxSafeJSONInteger)) {
		return nil
	}
	message, ok := scrubNullableProtocolString(fields["message"], options, scrub)
	if !ok {
		return nil
	}
	fileName, ok := scrubNullableProtocolString(fields["file_name"], options, scrub)
	if !ok {
		return nil
	}

	var targetOut any
	if rawTarget := fields["target"]; rawTarget != nil {
		target, ok := rawTarget.(map[string]any)
		if !ok || !hasOnlyAllowedKeys(target, allowedTargetKeys) {
			return nil
		}
		rawTag, hasTag := target["tag_name"]
		rawSource, hasSource := target["source_url"]
		if !hasTag || !hasSource {
			return nil
		}
		var tagOut any
		if rawTag != nil {
			tagText, ok := rawTag.(string)
			if !ok {
				return nil
			}
			if allowedHTMLResourceTags[asciiLower(tagText)] || !scrub {
				tagOut = tagText
			} else {
				protectedTag, err := redaction.ProtectTelemetryWithOptions(map[string]any{"tag_name": tagText}, options)
				if err != nil {
					return nil
				}
				tagMap, ok := protectedTag.(map[string]any)
				if !ok {
					return nil
				}
				sanitizedTag, ok := tagMap["tag_name"].(string)
				if !ok {
					return nil
				}
				tagOut = sanitizedTag
			}
		}
		sourceURL, ok := scrubNullableProtocolString(rawSource, options, scrub)
		if !ok {
			return nil
		}
		targetMap := map[string]any{"tag_name": tagOut, "source_url": sourceURL}
		if rawAttrs, hasAttrs := target["attributes"]; hasAttrs {
			attrs, ok := rawAttrs.(map[string]any)
			if !ok || !hasOnlyAllowedKeys(attrs, allowedTargetAttrKeys) {
				return nil
			}
			attrsOut := map[string]any{}
			for _, key := range allowedTargetAttrStrs {
				if val, exists := attrs[key]; exists {
					str, ok := val.(string)
					if !ok {
						return nil
					}
					cleaned, ok := scrubProtocolString(str, options, scrub)
					if !ok {
						return nil
					}
					attrsOut[key] = cleaned
				}
			}
			for _, key := range allowedTargetAttrBools {
				if val, exists := attrs[key]; exists {
					flag, ok := val.(bool)
					if !ok {
						return nil
					}
					attrsOut[key] = flag
				}
			}
			targetMap["attributes"] = attrsOut
		}
		targetOut = targetMap
	}

	out := map[string]any{
		"kind":          kind,
		"message":       message,
		"file_name":     fileName,
		"line_number":   lineNumber,
		"column_number": columnNumber,
		"target":        targetOut,
		"opaque":        opaque,
	}
	if rawPage, hasPage := fields["page"]; hasPage {
		page, ok := rawPage.(map[string]any)
		if !ok || len(page) != 4 || !hasOnlyAllowedKeys(page, allowedPageKeys) {
			return nil
		}
		pageURL, ok := scrubNullableProtocolString(page["url"], options, scrub)
		if !ok {
			return nil
		}
		referrer, ok := scrubNullableProtocolString(page["referrer"], options, scrub)
		if !ok {
			return nil
		}
		readyState := page["ready_state"]
		visibilityState := page["visibility_state"]
		if readyState != nil {
			state, ok := readyState.(string)
			if !ok || !allowedReadyStates[state] {
				return nil
			}
		}
		if visibilityState != nil {
			state, ok := visibilityState.(string)
			if !ok || !allowedVisibilityStates[state] {
				return nil
			}
		}
		out["page"] = map[string]any{
			"url":              pageURL,
			"referrer":         referrer,
			"ready_state":      readyState,
			"visibility_state": visibilityState,
		}
	}
	return out
}

func sanitizeRejectionReason(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || !hasOnlyAllowedKeys(fields, allowedRejectionKeys) {
		return nil
	}
	kind, ok := fields["kind"].(string)
	if !ok || !allowedRejectionKinds[kind] {
		return nil
	}
	out := map[string]any{"kind": kind}
	for _, key := range []string{"name", "message", "preview"} {
		if val, exists := fields[key]; exists {
			text, ok := val.(string)
			if !ok || len(text) == 0 {
				return nil
			}
			cleaned, ok := scrubProtocolString(text, options, scrub)
			if !ok || len(cleaned) == 0 {
				return nil
			}
			out[key] = cleaned
		}
	}
	return out
}

func sanitizeExceptionEvidence(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || len(fields) != 6 || fields["version"] != float64(1) {
		return nil
	}
	runtimeName, ok := fields["runtime"].(string)
	if !ok || !allowedRuntimes[runtimeName] || !isFiniteInt(fields["root_id"], 0, 7) {
		return nil
	}
	truncated, ok := fields["truncated"].(bool)
	if !ok {
		return nil
	}
	unavailable := fields["unavailable_reason"]
	if unavailable != nil {
		reason, ok := unavailable.(string)
		if !ok || !allowedUnavailable[reason] {
			return nil
		}
	}
	rawNodes, ok := fields["nodes"].([]any)
	if !ok || len(rawNodes) < 1 || len(rawNodes) > 8 {
		return nil
	}
	outNodes := make([]any, 0, len(rawNodes))
	for _, rawNode := range rawNodes {
		node, ok := rawNode.(map[string]any)
		if !ok || len(node) != 8 || !isFiniteInt(node["id"], 0, 7) {
			return nil
		}
		name, ok := node["name"].(string)
		if !ok || utf16Length(name) > 256 {
			return nil
		}
		message, ok := node["message"].(string)
		if !ok || utf16Length(message) > 2048 {
			return nil
		}
		traceOrigin, ok := node["trace_origin"].(string)
		if !ok || !allowedTraceOrigins[traceOrigin] {
			return nil
		}
		parsingStatus, ok := node["parsing_status"].(string)
		if !ok || !allowedParsingStatuses[parsingStatus] {
			return nil
		}
		var stackOut any
		if node["stack"] != nil {
			stackText, ok := node["stack"].(string)
			if !ok || utf16Length(stackText) > 16384 {
				return nil
			}
			if scrub {
				stackText = sanitizeBrowserStackURLs(stackText)
			}
			cleaned, ok := scrubProtocolString(stackText, options, scrub)
			if !ok || utf16Length(cleaned) > 16384 {
				return nil
			}
			stackOut = cleaned
		}
		cleanedName, ok := scrubProtocolString(name, options, scrub)
		if !ok || utf16Length(cleanedName) > 256 {
			return nil
		}
		cleanedMessage, ok := scrubProtocolString(message, options, scrub)
		if !ok || utf16Length(cleanedMessage) > 2048 {
			return nil
		}
		rawFrames, ok := node["frames"].([]any)
		if !ok || len(rawFrames) > 32 {
			return nil
		}
		outFrames := make([]any, 0, len(rawFrames))
		for _, rawFrame := range rawFrames {
			frame, ok := rawFrame.(map[string]any)
			if !ok || len(frame) != 5 {
				return nil
			}
			classification, ok := frame["classification"].(string)
			if !ok || !allowedClassifications[classification] {
				return nil
			}
			if (frame["line"] != nil && !isFiniteInt(frame["line"], 1, maxSafeJSONInteger)) ||
				(frame["column"] != nil && !isFiniteInt(frame["column"], 0, maxSafeJSONInteger)) {
				return nil
			}
			var fnOut, fileOut any
			if frame["function"] != nil {
				fnText, ok := frame["function"].(string)
				if !ok || utf16Length(fnText) > 256 {
					return nil
				}
				cleaned, ok := scrubProtocolString(fnText, options, scrub)
				if !ok || utf16Length(cleaned) > 256 {
					return nil
				}
				fnOut = cleaned
			}
			if frame["file"] != nil {
				fileText, ok := frame["file"].(string)
				if !ok || utf16Length(fileText) > 2048 {
					return nil
				}
				if scrub {
					fileText = sanitizeBrowserStackURLs(fileText)
				}
				cleaned, ok := scrubProtocolString(fileText, options, scrub)
				if !ok || utf16Length(cleaned) > 2048 {
					return nil
				}
				fileOut = cleaned
			}
			outFrames = append(outFrames, map[string]any{
				"function":       fnOut,
				"file":           fileOut,
				"line":           frame["line"],
				"column":         frame["column"],
				"classification": classification,
			})
		}
		rawEdges, ok := node["relationships"].([]any)
		if !ok || len(rawEdges) > 5 {
			return nil
		}
		outEdges := make([]any, 0, len(rawEdges))
		for _, rawEdge := range rawEdges {
			edge, ok := rawEdge.(map[string]any)
			if !ok || len(edge) != 2 {
				return nil
			}
			edgeKind, ok := edge["kind"].(string)
			if !ok || !allowedEdgeKinds[edgeKind] || !isFiniteInt(edge["target_id"], 0, 7) {
				return nil
			}
			outEdges = append(outEdges, map[string]any{"kind": edgeKind, "target_id": edge["target_id"]})
		}
		outNodes = append(outNodes, map[string]any{
			"id":             node["id"],
			"name":           cleanedName,
			"message":        cleanedMessage,
			"stack":          stackOut,
			"trace_origin":   traceOrigin,
			"parsing_status": parsingStatus,
			"frames":         outFrames,
			"relationships":  outEdges,
		})
	}
	out := map[string]any{
		"version":            float64(1),
		"runtime":            runtimeName,
		"root_id":            fields["root_id"],
		"nodes":              outNodes,
		"truncated":          truncated,
		"unavailable_reason": unavailable,
	}
	if !graph(out, true) {
		return nil
	}
	return out
}

func sanitizeExecution(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || len(fields) != 5 || fields["version"] != float64(1) {
		return nil
	}
	kind, ok := fields["kind"].(string)
	if !ok || !allowedExecutionKinds[kind] {
		return nil
	}
	if (fields["elapsed_ms"] != nil && !isFiniteNumber(fields["elapsed_ms"], 0)) ||
		(fields["limit_ms"] != nil && !isFiniteNumber(fields["limit_ms"], 0)) {
		return nil
	}
	var nameOut any
	if fields["name"] != nil {
		nameText, ok := fields["name"].(string)
		if !ok || utf16Length(nameText) > 128 {
			return nil
		}
		cleaned, ok := scrubProtocolString(nameText, options, scrub)
		if !ok || utf16Length(cleaned) > 128 {
			return nil
		}
		nameOut = cleaned
	}
	return map[string]any{
		"version":    float64(1),
		"kind":       kind,
		"name":       nameOut,
		"elapsed_ms": fields["elapsed_ms"],
		"limit_ms":   fields["limit_ms"],
	}
}

func sanitizeResourceObservations(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || len(fields) != 2 || fields["version"] != float64(1) {
		return nil
	}
	rawItems, ok := fields["items"].([]any)
	if !ok || len(rawItems) < 1 || len(rawItems) > 5 {
		return nil
	}
	outItems := make([]any, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil
		}
		kind, ok := item["kind"].(string)
		if !ok {
			return nil
		}
		observedAt, ok := item["observed_at"].(string)
		if !ok || !timestamp(observedAt) {
			return nil
		}
		var sourceOut any
		if item["source_url"] != nil {
			sourceText, ok := item["source_url"].(string)
			if !ok || !resourceURL(sourceText) {
				return nil
			}
			cleaned, ok := scrubProtocolString(sourceText, options, scrub)
			if !ok || !resourceURL(cleaned) {
				return nil
			}
			sourceOut = cleaned
		}
		switch kind {
		case "timing":
			if len(item) != 7 {
				return nil
			}
			availability, ok := item["availability"].(string)
			if !ok || !allowedAvailabilities[availability] {
				return nil
			}
			if (item["response_status"] != nil && !isFiniteInt(item["response_status"], 100, 599)) ||
				(item["duration_ms"] != nil && !isFiniteNumber(item["duration_ms"], 0)) ||
				(item["transfer_size_bytes"] != nil && !isFiniteInt(item["transfer_size_bytes"], 0, maxSafeJSONInteger)) {
				return nil
			}
			outItems = append(outItems, map[string]any{
				"kind":                "timing",
				"observed_at":         observedAt,
				"source_url":          sourceOut,
				"availability":        availability,
				"response_status":     item["response_status"],
				"duration_ms":         item["duration_ms"],
				"transfer_size_bytes": item["transfer_size_bytes"],
			})
		case "csp":
			if len(item) != 6 {
				return nil
			}
			directive, ok := item["effective_directive"].(string)
			if !ok || !effectiveDirectiveRegex.MatchString(directive) {
				return nil
			}
			disposition, ok := item["disposition"].(string)
			if !ok || !allowedDispositions[disposition] {
				return nil
			}
			if item["document_status_code"] != nil && !isFiniteInt(item["document_status_code"], 100, 599) {
				return nil
			}
			outItems = append(outItems, map[string]any{
				"kind":                 "csp",
				"observed_at":          observedAt,
				"source_url":           sourceOut,
				"effective_directive":  directive,
				"disposition":          disposition,
				"document_status_code": item["document_status_code"],
			})
		default:
			return nil
		}
	}
	out := map[string]any{"version": float64(1), "items": outItems}
	if !resources(out) {
		return nil
	}
	return out
}

func sanitizeDiagnosticContext(raw any, options redaction.TelemetryOptions, scrub bool) map[string]any {
	fields, err := CopyJSON(raw)
	if err != nil || fields["version"] != float64(1) || !fresh(fields["failure_ref"]) || !scope(fields["scope"]) {
		return nil
	}
	switch fields["kind"] {
	case "recovery":
		if !hasOnlyAllowedKeys(fields, allowedRecoveryKeys) || !fresh(fields["attempt_id"]) {
			return nil
		}
		state, ok := fields["state"].(string)
		if !ok || !allowedRecoveryStates[state] {
			return nil
		}
		out := map[string]any{
			"version":     float64(1),
			"kind":        "recovery",
			"failure_ref": fields["failure_ref"],
			"scope":       fields["scope"],
			"attempt_id":  fields["attempt_id"],
			"state":       state,
		}
		if rawOp, exists := fields["operation"]; exists {
			opText, ok := rawOp.(string)
			if !ok || utf16Length(opText) > 64 {
				return nil
			}
			cleaned, ok := scrubProtocolString(opText, options, scrub)
			if !ok || utf16Length(cleaned) > 64 {
				return nil
			}
			out["operation"] = cleaned
		}
		if statusCode, exists := fields["status_code"]; exists {
			if !isFiniteInt(statusCode, 100, 599) {
				return nil
			}
			out["status_code"] = statusCode
		}
		if durationMS, exists := fields["duration_ms"]; exists {
			if !isFiniteNumber(durationMS, 0) {
				return nil
			}
			out["duration_ms"] = durationMS
		}
		return out
	case "resource_observation":
		if len(fields) != 6 {
			return nil
		}
		sourceEventID, ok := fields["source_event_id"].(string)
		if !ok || !uuidPattern.MatchString(sourceEventID) {
			return nil
		}
		observations := sanitizeResourceObservations(fields["observations"], options, scrub)
		if observations == nil {
			return nil
		}
		return map[string]any{
			"version":         float64(1),
			"kind":            "resource_observation",
			"failure_ref":     fields["failure_ref"],
			"scope":           fields["scope"],
			"source_event_id": sourceEventID,
			"observations":    observations,
		}
	default:
		return nil
	}
}
