package debugbundle

import "github.com/debugbundle/debugbundle-go/v2/redaction"

// protectEvent keeps wire/auth identifiers intact while enforcing mandatory policy on captured data.
func (client *Client) protectEvent(event EventEnvelope) *EventEnvelope {
	if !redaction.SafeEventIdentity(map[string]any{
		"schema_version": event.SchemaVersion, "sdk_name": event.SDKName,
		"sdk_version": event.SDKVersion, "correlation": event.Correlation,
	}, client.config.redactFields) {
		return nil
	}
	protected, err := redaction.ProtectTelemetry(map[string]any{
		"payload": event.Payload,
		"context": event.Context,
		"service": event.Service,
	}, client.config.redactFields)
	if err != nil {
		return nil
	}
	fields, ok := protected.(map[string]any)
	if !ok {
		return nil
	}
	payload, ok := fields["payload"].(map[string]any)
	if !ok {
		return nil
	}
	service, ok := fields["service"].(map[string]any)
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
	if fields["context"] != nil {
		context, valid := fields["context"].(map[string]any)
		if !valid {
			return nil
		}
		safe.Context = context
	}
	safe.Service.Name = name
	safe.Service.Environment = environment
	if runtime, valid := service["runtime"].(string); valid {
		safe.Service.Runtime = runtime
	}
	if framework, valid := service["framework"].(string); valid {
		safe.Service.Framework = framework
	}
	return &safe
}
