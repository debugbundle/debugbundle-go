package evidence

import (
	"encoding/json"
	"sync"
)

// Capabilities describes verified support for one complete destination path.
type Capabilities struct {
	Version  int      `json:"version"`
	Features []string `json:"features"`
}

func unsupported() Capabilities { return Capabilities{Version: 1, Features: []string{}} }
func Descriptor(value any) Capabilities {
	var version any
	var features []string
	switch input := value.(type) {
	case *Capabilities:
		if input == nil {
			return unsupported()
		}
		return Descriptor(*input)
	case Capabilities:
		if len(input.Features) > 4 {
			return unsupported()
		}
		version = input.Version
		features = append([]string{}, input.Features...)
	case map[string]any:
		if len(input) != 2 {
			return unsupported()
		}
		version = input["version"]
		switch items := input["features"].(type) {
		case []string:
			if len(items) > 4 {
				return unsupported()
			}
			features = append([]string{}, items...)
		case []any:
			if len(items) > 4 {
				return unsupported()
			}
			for _, item := range items {
				feature, ok := item.(string)
				if !ok {
					return unsupported()
				}
				features = append(features, feature)
			}
		default:
			return unsupported()
		}
	default:
		return unsupported()
	}
	validVersion := false
	switch number := version.(type) {
	case int:
		validVersion = number == 1
	case float64:
		validVersion = number == 1
	case json.Number:
		validVersion = number == "1"
	}
	if !validVersion || len(features) > 4 {
		return unsupported()
	}
	seen := map[string]bool{}
	for _, feature := range features {
		if seen[feature] {
			return unsupported()
		}
		valid := false
		for _, known := range Features {
			if feature == known {
				valid = true
				break
			}
		}
		if !valid {
			return unsupported()
		}
		seen[feature] = true
	}
	result := unsupported()
	for _, known := range Features {
		if seen[known] {
			result.Features = append(result.Features, known)
		}
	}
	return result
}
func Intersection(hops []Capabilities) Capabilities {
	if len(hops) == 0 || len(hops) > 8 {
		return unsupported()
	}
	result := Capabilities{Version: 1, Features: append([]string{}, Features...)}
	for _, hop := range hops {
		allowed := map[string]bool{}
		for _, feature := range Descriptor(hop).Features {
			allowed[feature] = true
		}
		next := []string{}
		for _, feature := range result.Features {
			if allowed[feature] {
				next = append(next, feature)
			}
		}
		result.Features = next
	}
	return result
}

type State struct {
	mu    sync.RWMutex
	proof Capabilities
}

func NewState(pin any) *State { return &State{proof: Descriptor(pin)} }
func (state *State) Descriptor() Capabilities {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return Descriptor(state.proof)
}
func (state *State) Supports(feature string) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, known := range state.proof.Features {
		if known == feature {
			return true
		}
	}
	return false
}
func (state *State) Observe(value any) bool {
	next := Descriptor(value)
	state.mu.Lock()
	defer state.mu.Unlock()
	lost := false
	for _, previous := range state.proof.Features {
		found := false
		for _, feature := range next.Features {
			if previous == feature {
				found = true
				break
			}
		}
		if !found {
			lost = true
		}
	}
	state.proof = next
	return lost
}
func (state *State) ObserveBody(body []byte, required, valid bool) bool {
	var parsed map[string]any
	if len(body) > 262144 || json.Unmarshal(body, &parsed) != nil {
		if required {
			return state.Observe(nil)
		}
		return false
	}
	descriptor, exists := parsed["evidence_capabilities"]
	if !required && !exists {
		return false
	}
	if !valid {
		descriptor = nil
	}
	return state.Observe(descriptor)
}

// Filter operates on normalized owned JSON, including finalized retries.
func (state *State) Filter(event map[string]any, exceptionEnabled bool) map[string]any {
	if event == nil {
		return nil
	}
	proof := state.Descriptor()
	allowed := map[string]bool{}
	for _, feature := range proof.Features {
		allowed[feature] = true
	}
	original := object(event["payload"])
	if event["event_type"] == "diagnostic_context" {
		feature := "resource_observations_v1"
		if original["kind"] == "recovery" {
			feature = "recovery_v1"
		}
		if !allowed[feature] {
			return nil
		}
		return event
	}
	payload := map[string]any{}
	for key, value := range original {
		payload[key] = value
	}
	if !exceptionEnabled || !allowed["exception_evidence_v1"] {
		delete(payload, "exception_evidence")
		delete(payload, "execution")
	}
	if !allowed["resource_observations_v1"] {
		delete(payload, "resource_observations")
	}
	result := map[string]any{}
	for key, value := range event {
		result[key] = value
	}
	result["payload"] = payload
	context := object(event["context"])
	if !allowed["recovery_v1"] && !allowed["resource_observations_v1"] && Reference(context["evidence_reference"]) {
		reduced := map[string]any{}
		for key, value := range context {
			if key != "evidence_reference" {
				reduced[key] = value
			}
		}
		result["context"] = reduced
	}
	return result
}
