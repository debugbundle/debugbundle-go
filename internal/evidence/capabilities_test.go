package evidence

import (
	"encoding/json"
	"testing"
)

func TestNativeDescriptorFormsAndCompletePathIntersection(t *testing.T) {
	full := Capabilities{Version: 1, Features: append([]string{}, Features...)}
	if len(Descriptor(&full).Features) != 4 || len(Descriptor(map[string]any{"version": json.Number("1"), "features": []string{"recovery_v1"}}).Features) != 1 {
		t.Fatal("native descriptor lost")
	}
	var nilDescriptor *Capabilities
	for _, value := range []any{nilDescriptor, Capabilities{Version: 1, Features: []string{"a", "b", "c", "d", "e"}}, map[string]any{"version": 1, "features": []string{"a", "b", "c", "d", "e"}}, map[string]any{"version": 1, "features": []any{"a", "b", "c", "d", "e"}}, map[string]any{"version": 1, "features": []any{1}}, map[string]any{"version": 1, "features": nil}, map[string]any{"version": 1, "features": []string{}, "unknown": true}, "unknown"} {
		if len(Descriptor(value).Features) != 0 {
			t.Fatal("unsupported descriptor accepted")
		}
	}
	if len(Intersection(nil).Features) != 0 || len(Intersection(make([]Capabilities, 9)).Features) != 0 || len(Intersection([]Capabilities{full, {Version: 1, Features: []string{"recovery_v1"}}}).Features) != 1 {
		t.Fatal("intersection guessed support")
	}
	state := NewState(&full)
	snapshot := state.Descriptor()
	snapshot.Features[0] = "changed"
	if !state.Supports(Features[0]) || state.Supports("missing") {
		t.Fatal("proof mutated externally")
	}
	if state.ObserveBody(nil, false, false) || !state.Supports("recovery_v1") {
		t.Fatal("legacy custom reply revoked explicit proof")
	}
	if !state.ObserveBody([]byte(`{}`), true, true) || state.Supports("recovery_v1") {
		t.Fatal("native missing descriptor retained proof")
	}
	state.Observe(&full)
	if !state.ObserveBody([]byte(`{"evidence_capabilities":{"version":1,"features":["recovery_v1"]}}`), true, true) || !state.Supports("recovery_v1") {
		t.Fatal("valid native proof not observed")
	}
	if !state.ObserveBody([]byte(`{"evidence_capabilities":{"version":1,"features":["recovery_v1"]}}`), false, false) || state.Supports("recovery_v1") {
		t.Fatal("invalid ACK granted descriptor")
	}
	state.Observe(&full)
	if !state.ObserveBody([]byte(`{`), true, false) {
		t.Fatal("invalid native JSON retained proof")
	}
}
func TestFinalProofFilterRemovesOnlyTypedOptionalEvidence(t *testing.T) {
	state := NewState(nil)
	if state.Filter(nil, true) != nil {
		t.Fatal("nil event accepted")
	}
	reference := map[string]any{"version": float64(1), "failure_ref": "11111111-1111-4111-8111-111111111111", "scope": map[string]any{"kind": "operation", "operation_id": "22222222-2222-4222-8222-222222222222"}}
	event := map[string]any{"event_type": "frontend_exception", "payload": map[string]any{"message": "protected primary", "exception_evidence": map[string]any{}, "execution": map[string]any{}, "resource_observations": map[string]any{}}, "context": map[string]any{"evidence_reference": reference, "custom": "retain"}}
	filtered := state.Filter(event, true)
	payload := filtered["payload"].(map[string]any)
	if len(payload) != 1 || filtered["context"].(map[string]any)["evidence_reference"] != nil || payload["message"] != "protected primary" {
		t.Fatal("unknown proof did not reduce closed evidence")
	}
	for _, kind := range []string{"recovery", "resource_observation"} {
		event := map[string]any{"event_type": "diagnostic_context", "payload": map[string]any{"kind": kind}}
		if state.Filter(event, true) != nil {
			t.Fatal("unknown standalone evidence emitted")
		}
		full := NewState(Capabilities{Version: 1, Features: Features})
		if full.Filter(event, true) == nil {
			t.Fatal("verified standalone evidence lost")
		}
	}
	full := NewState(Capabilities{Version: 1, Features: Features})
	if full.Filter(event, false)["payload"].(map[string]any)["exception_evidence"] != nil {
		t.Fatal("disabled exception metadata retained")
	}
}
