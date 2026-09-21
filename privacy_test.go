package debugbundle

import "testing"

func TestProtectEventPreservesWireIdentifiersAndProtectsApplicationFields(t *testing.T) {
	client := &Client{}
	event := canonicalBeforeSendEvent("log_event", map[string]any{
		"message": "accessToken=secret",
	})
	event.Context = map[string]any{"clientSecret": "secret"}
	event.Service.Runtime = "go"
	event.Service.Framework = "echo"
	protected := client.protectEvent(event)
	if protected == nil || protected.EventID != event.EventID || protected.Service.Runtime != "go" || protected.Service.Framework != "echo" {
		t.Fatal("valid identifiers and service descriptors must survive")
	}
	if protected.Payload["message"] != "accessToken=[REDACTED]" || protected.Context["clientSecret"] != "[REDACTED]" {
		t.Fatal("application fields were not protected")
	}
	if event.Payload["message"] != "accessToken=secret" {
		t.Fatal("the caller's event was mutated")
	}
}

func TestProtectEventWithholdsInvalidRequiredFields(t *testing.T) {
	client := &Client{}
	for _, event := range []EventEnvelope{
		canonicalBeforeSendEvent("log_event", map[string]any{"invalid": func() {}}),
		func() EventEnvelope {
			event := canonicalBeforeSendEvent("log_event", map[string]any{"message": "ok"})
			event.Service.Name = ""
			return event
		}(),
		func() EventEnvelope {
			event := canonicalBeforeSendEvent("log_event", map[string]any{"message": "ok"})
			event.Service.Environment = ""
			return event
		}(),
	} {
		if client.protectEvent(event) != nil {
			t.Fatal("invalid event must be withheld")
		}
	}
}
