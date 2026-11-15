package debugbundle

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBeforeSendReducesCombinedEvidenceWithoutRestoringRemovedContent(t *testing.T) {
	data, err := os.ReadFile("internal/evidence/testdata/evidence-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Events []struct {
			ID    string        `json:"id"`
			Event EventEnvelope `json:"event"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range corpus.Events {
		if fixture.ID != "combined_optional_bytes" {
			continue
		}
		original := canonicalBeforeSendEvent("log_event", map[string]any{"level": "error", "message": "owner removed", "attributes": map[string]any{}})
		result, diagnostic := applyBeforeSend(original, func(EventEnvelope) *EventEnvelope {
			fixture.Event.Payload["message"] = "owner replacement"
			return &fixture.Event
		})
		if result == nil || result.Payload["message"] != "owner replacement" || diagnostic != "" {
			t.Fatalf("hook redaction lost: %#v %s", result, diagnostic)
		}
		if !validBeforeSendEvent(*result) {
			t.Fatal("reduced event must satisfy the unchanged receiving contract")
		}
		if _, exists := result.Payload["exception_evidence"]; exists {
			t.Fatal("oversized optional evidence retained")
		}
		if _, exists := fixture.Event.Payload["exception_evidence"]; !exists {
			t.Fatal("hook-owned result mutated")
		}
		return
	}
	t.Fatal("missing shared combined-budget fixture")
}

type hookForeignJSON struct{ calls *int }

func TestBeforeSendSharedByteBoundaryCases(t *testing.T) {
	data, err := os.ReadFile("internal/evidence/testdata/before-send-budget.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Original EventEnvelope `json:"original"`
		Cases    []struct {
			ID      string        `json:"id"`
			Event   EventEnvelope `json:"event"`
			Outcome string        `json:"outcome"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range corpus.Cases {
		t.Run(fixture.ID, func(t *testing.T) {
			result, _ := applyBeforeSend(corpus.Original, func(EventEnvelope) *EventEnvelope { return &fixture.Event })
			if result == nil {
				t.Fatal("lost admissible primary")
			}
			message := "owner replacement"
			if fixture.Outcome == "fallback" {
				message = "owner removed"
			}
			if result.Payload["message"] != message || !validBeforeSendEvent(*result) {
				t.Fatal("incorrect hook result")
			}
			if fixture.Outcome != "fallback" {
				_, retained := result.Payload["exception_evidence"]
				if retained != (fixture.Outcome == "keep") {
					t.Fatal("incorrect budget reduction")
				}
				if result.Context["owner_field"] != "preserved" {
					t.Fatal("lost custom context")
				}
				if strings.HasSuffix(fixture.ID, "-legacy") && result.Context["evidence_reference"] == nil {
					t.Fatal("lost legacy reference")
				}
				if strings.HasSuffix(fixture.ID, "-typed") && fixture.Outcome == "reduce" && result.Context["evidence_reference"] != nil {
					t.Fatal("orphaned typed reference")
				}
			}
		})
	}
}

func (value hookForeignJSON) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte(`"foreign"`), nil
}

func TestBeforeSendOwnsCompleteReplacementWithoutApplicationSerializers(t *testing.T) {
	original := canonicalBeforeSendEvent("log_event", map[string]any{"level": "error", "message": "original private", "attributes": map[string]any{}})
	calls := 0
	result, diagnostic := applyBeforeSend(original, func(event EventEnvelope) *EventEnvelope {
		event.Payload["message"] = "redacted replacement"
		event.Context = map[string]any{"foreign": hookForeignJSON{&calls}}
		return &event
	})
	if result != nil || calls != 0 || diagnostic != "before_send_validation_unavailable" {
		t.Fatal("unavailable validation must withhold without foreign serialization or original restoration")
	}
	result, _ = applyBeforeSend(original, func(event EventEnvelope) *EventEnvelope {
		event.Payload["message"] = "redacted replacement"
		event.Context = map[string]any{"over_budget": strings.Repeat("x", 8*1024*1024)}
		return &event
	})
	if result != nil {
		t.Fatal("validation exhaustion restored the original")
	}
	var retained map[string]any
	result, diagnostic = applyBeforeSend(original, func(event EventEnvelope) *EventEnvelope {
		retained = map[string]any{"plain": struct {
			Value string `json:"value"`
		}{"safe"}, "raw": json.RawMessage(`{"value":1}`)}
		event.Context = retained
		event.Payload["message"] = "redacted replacement"
		return &event
	})
	if result == nil || diagnostic != "" {
		t.Fatalf("native compatibility JSON rejected: %s", diagnostic)
	}
	retained["plain"] = "changed later"
	if _, ok := result.Context["plain"].(map[string]any); !ok {
		t.Fatal("accepted result retained application mutation")
	}
}

func TestBeforeSendRejectsIncompleteNestedCanonicalRequest(t *testing.T) {
	event := canonicalBeforeSendEvent("backend_exception", map[string]any{
		"name": "Failure", "message": "failed", "stack": "stack", "handled": true,
		"request": map[string]any{}, "response": map[string]any{}, "runtime": map[string]any{},
	})
	if validBeforeSendEvent(event) {
		t.Fatal("incomplete nested request must not pass the receiving contract")
	}
}

func TestBeforeSendAcceptsEveryCanonicalPayloadVariant(t *testing.T) {
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	payloads := map[string]map[string]any{
		"backend_exception": {
			"name": "Failure", "message": "failed", "stack": "stack", "handled": true,
			"request": map[string]any{"method": "GET", "path": "/", "headers": map[string]any{}, "query": map[string]any{}}, "response": map[string]any{"status_code": float64(500)}, "runtime": map[string]any{"version": "go-test"},
			"probe_data": map[string]any{"version": float64(1), "items": []any{}},
		},
		"request_event": {
			"method": "GET", "path": "/orders", "query": map[string]any{}, "headers": map[string]any{},
			"response_status": float64(503), "duration_ms": float64(12),
			"response_headers": map[string]any{},
		},
		"log_event": {
			"level": "error", "message": "failed", "attributes": map[string]any{},
		},
		"frontend_breadcrumb": {
			"breadcrumb_type": "route_change", "data": map[string]any{},
		},
		"frontend_exception": {
			"name": "TypeError", "message": "failed", "stack": "stack",
			"breadcrumbs": []any{}, "probe_data": map[string]any{"version": float64(1), "items": []any{}}, "browser": map[string]any{"name": "fixture", "version": "1"},
		},
		"deploy_metadata": {
			"commit_sha": "abc123", "version": "1.2.3", "branch": "main",
			"environment": "production", "deployed_at": timestamp,
		},
		"error_suppressed": {
			"fingerprint": "failure", "suppressed_count": float64(2), "window_seconds": float64(30),
			"first_seen": timestamp, "last_seen": timestamp,
		},
		"probe_event": {
			"label": "cart", "data": map[string]any{"value": float64(2)},
			"activation_id": nil, "probe_label_pattern": "cart*",
		},
	}

	for eventType, payload := range payloads {
		event := canonicalBeforeSendEvent(eventType, payload)
		if !validBeforeSendEvent(event) {
			t.Fatalf("expected %s payload to be valid", eventType)
		}
	}
}

func TestBeforeSendRejectsMalformedEnvelopeAndPayloadShapes(t *testing.T) {
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	base := canonicalBeforeSendEvent("probe_event", map[string]any{
		"label": "cart", "data": map[string]any{}, "activation_id": nil, "probe_label_pattern": "cart*",
	})
	mutations := []func(*EventEnvelope){
		func(event *EventEnvelope) { event.SchemaVersion = "" },
		func(event *EventEnvelope) { event.EventID = "not-a-uuid" },
		func(event *EventEnvelope) { event.EventType = "unknown" },
		func(event *EventEnvelope) { event.SDKName = "" },
		func(event *EventEnvelope) { event.SDKVersion = "" },
		func(event *EventEnvelope) { event.Service.Name = "" },
		func(event *EventEnvelope) { event.Service.Environment = "" },
		func(event *EventEnvelope) { event.OccurredAt = "not-a-time" },
		func(event *EventEnvelope) { event.Payload = nil },
		func(event *EventEnvelope) { delete(event.Payload, "label") },
		func(event *EventEnvelope) { event.Payload["extra"] = true },
		func(event *EventEnvelope) { event.Payload["data"] = []any{} },
		func(event *EventEnvelope) { event.Payload["activation_id"] = "bad" },
	}
	for index, mutate := range mutations {
		event := base
		event.Payload = cloneTestMap(base.Payload)
		mutate(&event)
		if validBeforeSendEvent(event) {
			t.Fatalf("expected malformed mutation %d to be rejected", index)
		}
	}

	invalidPayloads := []struct {
		eventType string
		payload   map[string]any
	}{
		{"backend_exception", map[string]any{"name": "x", "message": "x", "stack": "x", "handled": "yes", "request": map[string]any{}, "response": map[string]any{}, "runtime": map[string]any{}, "probe_data": []any{}}},
		{"request_event", map[string]any{"method": "GET", "path": "/", "query": map[string]any{}, "headers": map[string]any{}, "response_status": -1.0, "duration_ms": math.Inf(1), "response_headers": []any{}}},
		{"log_event", map[string]any{"level": "", "message": "x", "attributes": []any{}}},
		{"frontend_breadcrumb", map[string]any{"breadcrumb_type": "", "data": []any{}}},
		{"frontend_exception", map[string]any{"name": "x", "message": "x", "stack": "x", "breadcrumbs": map[string]any{}, "probe_data": []any{}}},
		{"deploy_metadata", map[string]any{"commit_sha": "x", "version": "x", "branch": "x", "environment": "x", "deployed_at": "bad"}},
		{"error_suppressed", map[string]any{"fingerprint": "x", "suppressed_count": 1.5, "window_seconds": 0.0, "first_seen": timestamp, "last_seen": "bad"}},
	}
	for _, candidate := range invalidPayloads {
		if validBeforeSendEvent(canonicalBeforeSendEvent(candidate.eventType, candidate.payload)) {
			t.Fatalf("expected malformed %s payload to be rejected", candidate.eventType)
		}
	}
}

func TestBeforeSendCloneFailureFallsBackToOriginalEvent(t *testing.T) {
	event := canonicalBeforeSendEvent("log_event", map[string]any{
		"level": "error", "message": "failed", "attributes": map[string]any{"invalid": func() {}},
	})
	result, diagnostic := applyBeforeSend(event, func(candidate EventEnvelope) *EventEnvelope {
		return &candidate
	})
	if result == nil || diagnostic != "before_send_clone_failed" {
		t.Fatalf("expected safe clone fallback, got result=%#v diagnostic=%q", result, diagnostic)
	}
}

func canonicalBeforeSendEvent(eventType string, payload map[string]any) EventEnvelope {
	return EventEnvelope{
		SchemaVersion: "2026-03-01",
		EventID:       "11111111-1111-4111-8111-111111111111",
		EventType:     eventType,
		SDKName:       "@debugbundle/sdk-go",
		SDKVersion:    "1.3.0",
		Service:       ServiceDescriptor{Name: "orders", Environment: "test"},
		OccurredAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       payload,
	}
}

func cloneTestMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
