package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/debugbundle/debugbundle-go/v3/transport"
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
	// Mutate the closed graph so it fails closed validation and must be dropped without discarding the primary.
	graph["nodes"].([]any)[0].(map[string]any)["frames"].([]any)[0].(map[string]any)["classification"] = "invalid_classification"
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

func TestBroadRedactFieldsPreservesProtocolStructuresWhileRedactingApplicationData(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	failureRef := "11111111-1111-4111-8111-111111111111"
	attemptID := "33333333-3333-4333-8333-333333333333"
	client := New(Config{
		ProjectToken:                "dbundle_proj_test",
		Service:                     "billing",
		Environment:                 "production",
		Transport:                   recorder,
		CaptureRecoveryObservations: true,
		EvidenceCapabilities:        &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1", "recovery_v1"}},
		BeforeSend: func(event EventEnvelope) *EventEnvelope {
			if event.EventType == "backend_exception" {
				event.Context["hook_payout"] = map[string]any{"reference": "SYNTHETIC_HOOK_REF", "reason": "SYNTHETIC_HOOK_REASON"}
			}
			return &event
		},
	}.WithRedactFields("tag", "reference", "reason", "file", "classification"))
	defer func() { _ = client.Close() }()

	_ = client.RunWithOperationContext(context.Background(), OperationOptions{Kind: "job", Name: "payout.run reason=SYNTHETIC_EXEC"}, func(ctx context.Context) error {
		client.CaptureException(ctx, errors.New("boom reason=SYNTHETIC_ERR"),
			WithFailureReference(failureRef),
			WithEventContext(map[string]any{
				"payout": map[string]any{
					"tag":       "SYNTHETIC_TAG",
					"reference": "SYNTHETIC_REF",
					"reason":    "SYNTHETIC_REASON",
					"file":      "SYNTHETIC_FILE",
				},
			}),
		)
		client.CaptureRecovery(ctx, RecoveryObservation{
			FailureRef: failureRef,
			AttemptID:  attemptID,
			State:      "recovery_observed",
			Operation:  "payout.retry reason=SYNTHETIC_OP",
			StatusCode: 200,
		})
		return nil
	})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatalf("expected exception and recovery events, got %#v", recorder.requests)
	}
	var exceptionEvent, recoveryEvent EventEnvelope
	_ = json.Unmarshal(recorder.requests[0].Events[0], &exceptionEvent)
	_ = json.Unmarshal(recorder.requests[0].Events[1], &recoveryEvent)

	if exceptionEvent.EventType != "backend_exception" {
		t.Fatalf("unexpected first event: %s", exceptionEvent.EventType)
	}
	evidenceRef, ok := exceptionEvent.Context["evidence_reference"].(map[string]any)
	if !ok || evidenceRef["failure_ref"] != failureRef {
		t.Fatalf("expected validated evidence_reference to survive broad RedactFields, got %#v", exceptionEvent.Context["evidence_reference"])
	}
	payout := exceptionEvent.Context["payout"].(map[string]any)
	for _, field := range []string{"tag", "reference", "reason", "file"} {
		if payout[field] != "[REDACTED]" {
			t.Fatalf("expected payout.%s redacted, got %#v", field, payout[field])
		}
	}
	hookPayout := exceptionEvent.Context["hook_payout"].(map[string]any)
	if hookPayout["reference"] != "[REDACTED]" || hookPayout["reason"] != "[REDACTED]" {
		t.Fatalf("expected post-BeforeSend re-sanitization, got %#v", hookPayout)
	}
	graph, ok := exceptionEvent.Payload["exception_evidence"].(map[string]any)
	if !ok || graph["unavailable_reason"] != nil {
		t.Fatalf("expected valid exception_evidence preserved, got %#v", exceptionEvent.Payload["exception_evidence"])
	}
	frames := graph["nodes"].([]any)[0].(map[string]any)["frames"].([]any)
	if len(frames) == 0 || frames[0].(map[string]any)["file"] == "[REDACTED]" {
		t.Fatalf("expected frame file preserved, got %#v", frames)
	}
	execution, ok := exceptionEvent.Payload["execution"].(map[string]any)
	if !ok || execution["kind"] != "job" || execution["name"] != "payout.run reason=[REDACTED]" {
		t.Fatalf("expected execution preserved and scrubbed, got %#v", exceptionEvent.Payload["execution"])
	}
	if recoveryEvent.EventType != "diagnostic_context" || recoveryEvent.Payload["state"] != "recovery_observed" ||
		recoveryEvent.Payload["operation"] != "payout.retry reason=[REDACTED]" {
		t.Fatalf("expected diagnostic_context recovery preserved and scrubbed, got %#v", recoveryEvent.Payload)
	}
	rawWire := string(recorder.requests[0].Events[0]) + string(recorder.requests[0].Events[1])
	for _, secret := range []string{"SYNTHETIC_TAG", "SYNTHETIC_REF", "SYNTHETIC_REASON", "SYNTHETIC_FILE", "SYNTHETIC_ERR", "SYNTHETIC_EXEC", "SYNTHETIC_OP", "SYNTHETIC_HOOK_REF", "SYNTHETIC_HOOK_REASON"} {
		if strings.Contains(rawWire, secret) {
			t.Fatalf("secret %s leaked into wire output", secret)
		}
	}
}

func TestRedactExactFieldsMasksExactKeysWhilePreservingCompoundSiblings(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{
		ProjectToken: "dbundle_proj_test",
		Service:      "billing",
		Environment:  "production",
		Transport:    recorder,
		BeforeSend: func(event EventEnvelope) *EventEnvelope {
			event.Context["hook_tag"] = "keep-compound"
			event.Context["TAG"] = "SYNTHETIC_HOOK_TAG"
			return &event
		},
	}.WithRedactExactFields("tag"))
	defer func() { _ = client.Close() }()

	client.SetContext("payout", map[string]any{
		"tag":      "SYNTHETIC_CTX_TAG",
		"tag_name": "script",
		"tagName":  "link",
		"tag-name": "img",
	})
	client.Probe(context.Background(), "checkout tag=SYNTHETIC_PROBE_LABEL tag_name=script", map[string]any{
		"tag":      "SYNTHETIC_PROBE_TAG",
		"tag_name": "script",
	})
	client.CaptureLog(context.Background(), "payout failed tag=SYNTHETIC_MSG tag_name=script tag-name=img", LevelError, map[string]any{
		"tag":        "SYNTHETIC_ATTR_TAG",
		"tag_name":   "script",
		"url":        "https://example.test/api?tag=SYNTHETIC_QUERY&tag_name=script&tag-name=img",
		"body":       `{"tag":"SYNTHETIC_JSON","tag_name":"script"}`,
		"user_token": "SYNTHETIC_MANDATORY",
	})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 1 {
		t.Fatalf("expected one log event, got %#v", recorder.requests)
	}
	var event EventEnvelope
	_ = json.Unmarshal(recorder.requests[0].Events[0], &event)

	if event.Payload["message"] != "payout failed tag=[REDACTED] tag_name=script tag-name=img" {
		t.Fatalf("unexpected log message: %#v", event.Payload["message"])
	}
	attrs := event.Payload["attributes"].(map[string]any)
	if attrs["tag"] != "[REDACTED]" || attrs["tag_name"] != "script" || attrs["user_token"] != "[REDACTED]" {
		t.Fatalf("unexpected log attributes: %#v", attrs)
	}
	if attrs["url"] != "https://example.test/api?tag=%5BREDACTED%5D&tag_name=script&tag-name=img" {
		t.Fatalf("unexpected url scrubbing: %#v", attrs["url"])
	}
	if attrs["body"] != `{"tag":"[REDACTED]","tag_name":"script"}` {
		t.Fatalf("unexpected json string scrubbing: %#v", attrs["body"])
	}
	payout := event.Context["payout"].(map[string]any)
	if payout["tag"] != "[REDACTED]" || payout["tag_name"] != "script" || payout["tagName"] != "link" || payout["tag-name"] != "img" {
		t.Fatalf("unexpected context payout: %#v", payout)
	}
	if event.Context["TAG"] != "[REDACTED]" || event.Context["hook_tag"] != "keep-compound" {
		t.Fatalf("unexpected post-BeforeSend exact redaction: %#v", event.Context)
	}
	probeSnapshot := client.snapshotProbes()
	probeItem := probeSnapshot["items"].([]any)[0].(map[string]any)
	if probeItem["label"] != "checkout tag=[REDACTED] tag_name=script" ||
		probeItem["data"].(map[string]any)["tag"] != "[REDACTED]" ||
		probeItem["data"].(map[string]any)["tag_name"] != "script" {
		t.Fatalf("unexpected probe exact redaction: %#v", probeItem)
	}
}
