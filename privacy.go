package debugbundle

import (
	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/redaction"
)

// protectEvent preserves the latest accepted primary when optional evidence cannot
// survive privacy. It never restores a pre-hook event or drops custom legacy refs.
func (client *Client) protectEvent(event EventEnvelope) *EventEnvelope {
	if safe := client.protectEventOnce(event); safe != nil {
		return safe
	}
	reduced := withoutOptionalEvidence(event, true)
	if reduced == nil {
		return nil
	}
	if safe := client.protectEventOnce(*reduced); safe != nil {
		return safe
	}
	reduced = withoutOptionalEvidence(event, false)
	if reduced == nil {
		return nil
	}
	return client.protectEventOnce(*reduced)
}
func withoutOptionalEvidence(event EventEnvelope, mark bool) *EventEnvelope {
	if event.EventType != "backend_exception" && event.EventType != "frontend_exception" {
		return nil
	}
	payload := map[string]any{}
	for key, value := range event.Payload {
		payload[key] = value
	}
	limits := map[string]any{"version": 1}
	for _, field := range []string{"exception_evidence", "execution", "resource_observations"} {
		if _, exists := payload[field]; exists {
			delete(payload, field)
			limits[field] = "budget_exceeded"
		}
	}
	if len(limits) == 1 {
		return nil
	}
	context := map[string]any{}
	for key, value := range event.Context {
		context[key] = value
	}
	if normalized, err := evidence.CopyJSON(map[string]any{"reference": context["evidence_reference"]}); err == nil && evidence.Reference(normalized["reference"]) {
		delete(context, "evidence_reference")
	}
	if mark {
		context["evidence_limits"] = limits
	}
	event.Payload = payload
	event.Context = context
	return &event
}

// protectEventOnce keeps wire/auth identifiers and validated closed protocol records intact.
func (client *Client) protectEventOnce(event EventEnvelope) *EventEnvelope {
	options := client.telemetryOptions()
	if !redaction.SafeEventIdentityWithOptions(map[string]any{
		"schema_version": event.SchemaVersion, "sdk_name": event.SDKName,
		"sdk_version": event.SDKVersion, "correlation": event.Correlation,
	}, options) {
		return nil
	}
	payload, service, contextMap, ok := evidence.ProtectEventFields(
		event.EventType,
		event.Payload,
		event.Service,
		event.Context,
		options,
	)
	if !ok {
		return nil
	}
	name, ok := service["name"].(string)
	if !ok || name == "" {
		return nil
	}
	environment, ok := service["environment"].(string)
	if !ok || environment == "" {
		return nil
	}
	safe := event
	safe.Payload = payload
	if event.Context != nil {
		safe.Context = contextMap
	}
	safe.Service.Name = name
	safe.Service.Environment = environment
	if runtime, valid := service["runtime"].(string); valid {
		safe.Service.Runtime = runtime
	}
	if framework, valid := service["framework"].(string); valid {
		safe.Service.Framework = framework
	}
	if !validBeforeSendEvent(safe) {
		return nil
	}
	return &safe
}
