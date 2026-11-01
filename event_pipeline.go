package debugbundle

import (
	"context"
	"encoding/json"
	"time"
)

func (client *Client) finalizeQueuedEvent(candidate queuedEvent) (queuedEvent, bool) {
	if candidate.finalized {
		return client.finalizeEvidenceOwnership(candidate, candidate.diagnosticID)
	}
	var event EventEnvelope
	if err := json.Unmarshal(candidate.encoded, &event); err != nil {
		return queuedEvent{}, false
	}
	hook := client.config.beforeSend
	if candidate.fatal {
		hook = nil
	}
	prepared, diagnostic := applyBeforeSend(event, hook)
	if diagnostic != "" {
		client.recordDiagnostic(diagnostic)
	}
	if prepared == nil {
		return queuedEvent{}, false
	}
	prepared = client.protectEvent(*prepared)
	if prepared == nil {
		return queuedEvent{}, false
	}
	client.mu.Lock()
	allowed := client.passesCapturePolicyLocked(*prepared)
	client.mu.Unlock()
	if !allowed {
		return queuedEvent{}, false
	}
	encoded, err := json.Marshal(prepared)
	if err != nil || len(encoded) > maxPendingBytes {
		return queuedEvent{}, false
	}
	return client.finalizeEvidenceOwnership(queuedEvent{encoded: encoded, finalized: true, fatal: candidate.fatal, highPriority: candidate.highPriority}, candidate.diagnosticID)
}

func (client *Client) capture(ctx context.Context, eventType string, payload map[string]any, options ...EventOption) {
	client.captureWithMode(ctx, eventType, payload, false, options...)
}

func (client *Client) captureWithMode(ctx context.Context, eventType string, payload map[string]any, fatal bool, options ...EventOption) {
	highPriority := eventType == "backend_exception" ||
		(eventType == "log_event" && shouldCaptureLog(LevelError, LogLevel(stringValue(payload["level"])))) ||
		(eventType == "request_event" && intValue(payload["response_status"]) >= 500)
	if eventType != "diagnostic_context" && !client.mayPrepareCapture(highPriority) {
		return
	}
	if !client.mu.TryLock() {
		if eventType != "diagnostic_context" {
			client.contentionDrops.Add(1)
		}
		return
	}
	if client.closed || client.transport == nil {
		client.mu.Unlock()
		return
	}
	persistent := make(map[string]any, len(client.persistent))
	for key, value := range client.persistent {
		persistent[key] = value
	}
	client.mu.Unlock()
	mergedContext, owner := client.mergedContextWithOwner(persistent, ctx, options...)
	if eventType == "backend_exception" && client.config.captureExceptionEvidence && client.evidence.Supports("exception_evidence_v1") {
		if execution := owner.execution(client.operations); execution != nil {
			payload["execution"] = execution
		}
	}
	if fatal {
		fitFatalEvidence(payload, mergedContext)
	}
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
	protected := client.protectEvent(event)
	if protected == nil {
		return
	}

	encoded, err := json.Marshal(protected)
	if err != nil {
		return
	}
	if !client.mu.TryLock() {
		if eventType != "diagnostic_context" {
			client.contentionDrops.Add(1)
		}
		return
	}
	defer client.mu.Unlock()
	if client.closed || client.transport == nil || !client.passesCapturePolicyLocked(*protected) || !client.shouldSample() {
		return
	}
	fingerprint := client.fingerprintForEvent(protected.EventType, protected.Payload)
	if fingerprint != "" && !client.suppression.ShouldCapture(fingerprint, time.Now().UTC()) {
		return
	}
	diagnosticID := ""
	if protected.EventType == "diagnostic_context" {
		if client.inFlightCount+len(client.buffer) >= maxPendingEvents || client.inFlightBytes+client.bufferBytes+len(encoded) > maxPendingBytes || !client.diagnosticBudget.reserve(protected.EventID, protected.Payload, len(encoded), time.Now()) {
			return
		}
		diagnosticID = protected.EventID
	}
	if !client.offerEncodedLocked(queuedEvent{
		diagnosticID: diagnosticID,
		fatal:        fatal,
		encoded:      encoded,
		highPriority: protected.EventType == "backend_exception" ||
			(protected.EventType == "log_event" && shouldCaptureLog(LevelError,
				LogLevel(stringValue(protected.Payload["level"])))) ||
			(protected.EventType == "request_event" && intValue(protected.Payload["response_status"]) >= 500),
	}) {
		client.diagnosticBudget.release(diagnosticID)
	}
}

func (client *Client) passesCapturePolicyLocked(event EventEnvelope) bool {
	switch event.EventType {
	case "diagnostic_context":
		feature := "resource_observations_v1"
		if event.Payload["kind"] == "recovery" {
			if !client.config.captureRecoveryObservations {
				return false
			}
			feature = "recovery_v1"
		}
		return client.capturePolicy.Preset != "minimal" && client.capturePolicy.CaptureDiagnosticContext && client.evidence.Supports(feature)
	case "log_event":
		level := LogLevel(stringValue(event.Payload["level"]))
		return client.captureLogLevelAllowedLocked(level)
	case "request_event":
		return shouldCaptureRequestByPolicy(
			intValue(event.Payload["response_status"]),
			stringValue(event.Payload["path"]),
			stringValue(event.Payload["method"]),
			client.capturePolicy,
		)
	case "probe_event":
		return client.capturePolicy.CaptureProbeEvents == CaptureProbeEventsStandaloneWhenActivated
	default:
		return true
	}
}
