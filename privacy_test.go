package debugbundle

import (
	"errors"
	"testing"
)

func TestProtectEventPreservesWireIdentifiersAndProtectsApplicationFields(t *testing.T) {
	client := &Client{}
	event := canonicalBeforeSendEvent("log_event", map[string]any{
		"message": "accessToken=secret", "level": "error", "attributes": map[string]any{},
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
			event := canonicalBeforeSendEvent("log_event", map[string]any{"message": "ok", "level": "error", "attributes": map[string]any{}})
			event.Service.Name = ""
			return event
		}(),
		func() EventEnvelope {
			event := canonicalBeforeSendEvent("log_event", map[string]any{"message": "ok", "level": "error", "attributes": map[string]any{}})
			event.Service.Environment = ""
			return event
		}(),
	} {
		if client.protectEvent(event) != nil {
			t.Fatal("invalid event must be withheld")
		}
	}
}

func TestOptionalGraphPrivacyFailurePreservesLatestAcceptedPrimary(t *testing.T) {
	client := &Client{config: resolvedConfig{redactFields: []string{"classification"}}}
	graph := collectErrorEvidence(errors.New("graph fixture"), "graph fixture", "goroutine 1 [running]:\nfixture.Main()\n\t/app/main.go:42 +0x1\n", false)
	event := canonicalBeforeSendEvent("backend_exception", map[string]any{
		"name": "Failure", "message": "latest accepted redaction", "stack": "retained", "handled": true,
		"request": emptyRequestPayload(), "response": emptyResponsePayload(), "runtime": map[string]any{"version": "go-test"}, "exception_evidence": graph,
	})
	event.Context = map[string]any{"evidence_reference": map[string]any{"owner": "legacy-custom"}}
	protected := client.protectEvent(event)
	if protected == nil || protected.Payload["message"] != "latest accepted redaction" {
		t.Fatal("optional metadata privacy failure discarded the accepted primary")
	}
	if _, exists := protected.Payload["exception_evidence"]; exists {
		t.Fatal("invalid closed graph survived privacy")
	}
	if protected.Context["evidence_limits"] == nil || protected.Context["evidence_reference"] == nil {
		t.Fatal("fallback lacks limits or removed unrelated legacy custom context")
	}
}
