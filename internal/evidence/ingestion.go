package evidence

import "encoding/json"

// Prefix counts the native encoder's event form plus the complete request wrapper.
func Prefix(events []json.RawMessage) int {
	size, count := 13, 0
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return count
		}
		next := len(encoded)
		if count > 0 {
			next++
		}
		if next > MaxBodyBytes-size {
			break
		}
		size += next
		count++
	}
	return count
}
func eventFits(event json.RawMessage) bool {
	encoded, err := json.Marshal(event)
	return err == nil && len(encoded) <= MaxBodyBytes-13
}

// FitEvent reduces only optional metadata on the latest accepted primary.
func FitEvent(event json.RawMessage) (json.RawMessage, bool) {
	if eventFits(event) {
		return event, true
	}
	var owned map[string]any
	if json.Unmarshal(event, &owned) != nil {
		return nil, false
	}
	for _, mark := range []bool{true, false} {
		reduced := WithoutOptional(owned, mark)
		if reduced == nil {
			return nil, false
		}
		encoded, err := json.Marshal(reduced)
		if err == nil && eventFits(encoded) {
			return encoded, true
		}
	}
	return nil, false
}
func WithoutOptional(event map[string]any, mark bool) map[string]any {
	if event["event_type"] != "backend_exception" && event["event_type"] != "frontend_exception" {
		return nil
	}
	payload := map[string]any{}
	for key, value := range object(event["payload"]) {
		payload[key] = value
	}
	limits := map[string]any{"version": float64(1)}
	for _, field := range []string{"exception_evidence", "execution", "resource_observations"} {
		if _, exists := payload[field]; exists {
			delete(payload, field)
			limits[field] = "budget_exceeded"
		}
	}
	if len(limits) == 1 {
		return nil
	}
	output := map[string]any{}
	for key, value := range event {
		output[key] = value
	}
	output["payload"] = payload
	context := map[string]any{}
	for key, value := range object(event["context"]) {
		context[key] = value
	}
	if Reference(context["evidence_reference"]) {
		delete(context, "evidence_reference")
	}
	if mark {
		context["evidence_limits"] = limits
	}
	output["context"] = context
	return output
}
