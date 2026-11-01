package debugbundle

import (
	"encoding/json"
	"github.com/debugbundle/debugbundle-go/v3/transport"
	"testing"
)

func TestFinalizedRetryRechecksMinimalPolicyWithoutRepeatingHook(t *testing.T) {
	hooks := 0
	client := New(Config{ProjectToken: "synthetic", Transport: &recordingTransport{response: transport.Response{StatusCode: 202}}, CaptureRecoveryObservations: true, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1", "resource_observations_v1", "recovery_v1"}}, BeforeSend: func(event EventEnvelope) *EventEnvelope { hooks++; return &event }})
	defer func() { _ = client.Close() }()
	event := canonicalBeforeSendEvent("frontend_exception", map[string]any{"name": "Error", "message": "latest protected primary", "stack": "retained", "browser": map[string]any{"name": "fixture", "version": "1"}, "resource_observations": map[string]any{"version": 1, "items": []any{map[string]any{"kind": "timing", "observed_at": "2026-10-27T12:00:00Z", "source_url": "/fixture.js", "availability": "no_match", "response_status": nil, "duration_ms": nil, "transfer_size_bytes": nil}}}})
	event.Context = map[string]any{"evidence_reference": map[string]any{"version": 1, "failure_ref": "11111111-1111-4111-8111-111111111111", "scope": map[string]any{"kind": "operation", "operation_id": "22222222-2222-4222-8222-222222222222"}}}
	if !validBeforeSendEvent(event) {
		t.Fatal("noncanonical fixture")
	}
	encoded, _ := json.Marshal(event)
	client.mu.Lock()
	client.capturePolicy = minimalCapturePolicy()
	client.mu.Unlock()
	final, keep := client.finalizeQueuedEvent(queuedEvent{encoded: encoded, finalized: true})
	if !keep || hooks != 0 {
		t.Fatal("retry repeated hook or discarded primary")
	}
	var result EventEnvelope
	_ = json.Unmarshal(final.encoded, &result)
	if result.Payload["resource_observations"] != nil || result.Context["evidence_reference"] != nil || result.Payload["message"] != "latest protected primary" {
		t.Fatal("minimal policy did not preserve primary and withhold passive evidence")
	}
}

func TestFinalizedRetryWithMalformedBytesIsWithheld(t *testing.T) {
	client := New(Config{ProjectToken: "synthetic", Transport: &recordingTransport{response: transport.Response{StatusCode: 202}}})
	defer func() { _ = client.Close() }()
	if _, keep := client.finalizeQueuedEvent(queuedEvent{encoded: json.RawMessage(`{`), finalized: true}); keep {
		t.Fatal("corrupt finalized event was delivered")
	}
}
