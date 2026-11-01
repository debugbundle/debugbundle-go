package debugbundle

import (
	"encoding/json"
	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/transport"
	"reflect"
	"time"
)

// Recheck destination proof for finalized retries without re-running application hooks.
func (client *Client) filterQueuedEvidence(candidate queuedEvent) (queuedEvent, bool) {
	var event map[string]any
	if json.Unmarshal(candidate.encoded, &event) != nil {
		return queuedEvent{}, false
	}
	if client.evidence == nil {
		return candidate, true
	}
	filtered := client.evidence.Filter(event, client.config.captureExceptionEvidence)
	if filtered == nil {
		return queuedEvent{}, false
	}
	if !reflect.DeepEqual(event, filtered) {
		encoded, err := json.Marshal(filtered)
		if err != nil {
			return queuedEvent{}, false
		}
		candidate.encoded = encoded
	}
	if _, native := client.transport.(*transport.HTTPTransport); native {
		encoded, fits := evidence.FitEvent(candidate.encoded)
		if !fits {
			return queuedEvent{}, false
		}
		candidate.encoded = encoded
	}
	return candidate, true
}

func (client *Client) downgradeRejectedEvidence(candidate queuedEvent) (queuedEvent, bool) {
	var original map[string]any
	if json.Unmarshal(candidate.encoded, &original) != nil {
		return queuedEvent{}, false
	}
	if original["event_type"] != "backend_exception" && original["event_type"] != "frontend_exception" {
		return queuedEvent{}, false
	}
	filtered := client.evidence.Filter(original, client.config.captureExceptionEvidence)
	if filtered == nil || reflect.DeepEqual(original, filtered) {
		return queuedEvent{}, false
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return queuedEvent{}, false
	}
	candidate.encoded = encoded
	candidate.finalized = true
	return candidate, true
}

// Hosted configuration is not a delivery hop for native local files. Only an
// explicit complete-reader pin qualifies that path; config still controls policy.
func (client *Client) observeRemoteEvidence(descriptor any) {
	switch client.transport.(type) {
	case *lazyFileTransport, *transport.FileTransport:
		return
	}
	if client.evidence != nil {
		client.evidence.Observe(descriptor)
	}
}

// Move unsent, finalized tails back under ordinary queue accounting atomically.
func (client *Client) nativeBatchPrefix(batch []queuedEvent) []queuedEvent {
	if _, native := client.transport.(*transport.HTTPTransport); !native {
		return batch
	}
	events := make([]json.RawMessage, len(batch))
	for index, event := range batch {
		events[index] = event.encoded
	}
	count := evidence.Prefix(events)
	if count == len(batch) {
		return batch
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		return nil
	}
	for _, event := range batch[count:] {
		client.inFlightCount--
		client.inFlightBytes -= len(event.encoded)
	}
	client.restoreLocked(batch[count:])
	return batch[:count]
}

// The final protected DTO owns the receipt, even when a hook changes its ID,
// scope, kind or attempt. Retries reuse that ownership and never call the hook again.
func (client *Client) finalizeEvidenceOwnership(candidate queuedEvent, original string) (queuedEvent, bool) {
	filtered, keep := client.filterQueuedEvidence(candidate)
	if !keep {
		return queuedEvent{}, false
	}
	var event EventEnvelope
	if json.Unmarshal(filtered.encoded, &event) != nil {
		return queuedEvent{}, false
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || !client.passesCapturePolicyLocked(event) {
		return queuedEvent{}, false
	}
	if client.capturePolicy.Preset == "minimal" {
		delete(event.Payload, "resource_observations")
		if reference, ok := event.Context["evidence_reference"].(map[string]any); ok && evidence.Reference(reference) {
			delete(event.Context, "evidence_reference")
		}
		var err error
		filtered.encoded, err = json.Marshal(event)
		if err != nil {
			return queuedEvent{}, false
		}
	}
	if event.EventType == "diagnostic_context" {
		if !client.diagnosticBudget.replace(original, event.EventID, event.Payload, len(filtered.encoded), time.Now()) {
			return queuedEvent{}, false
		}
		filtered.diagnosticID = event.EventID
	} else {
		client.diagnosticBudget.release(original)
		filtered.diagnosticID = ""
		if reference, ok := event.Context["evidence_reference"].(map[string]any); ok && evidence.Reference(reference) {
			client.diagnosticBudget.register(reference, event.EventID, time.Now())
		}
	}
	return filtered, true
}
func (client *Client) releaseDiagnosticEventsLocked(events []queuedEvent) {
	for _, event := range events {
		client.diagnosticBudget.release(event.diagnosticID)
	}
}
