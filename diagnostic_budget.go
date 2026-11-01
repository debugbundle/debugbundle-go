package debugbundle

import (
	"encoding/json"
	"time"
)

const diagnosticMaxBytes = 64 * 1024
const diagnosticWindow = 5 * time.Minute

// All access is owned by Client.mu. Only canonical JSON and bounded UUIDs enter
// the tracker; serialized metadata and pending events share one byte allowance.
type diagnosticWindowState struct {
	StartsAt     time.Time
	FailureAt    time.Time
	FailureID    string
	Attempts     map[string]bool
	Observations int
	Resource     bool
	Ambiguous    bool
	Pending      int
}
type diagnosticReceipt struct {
	key, kind, attempt string
	bytes, cost        int
}
type diagnosticCaptureBudget struct {
	windows map[string]diagnosticWindowState
	pending map[string]diagnosticReceipt
	held    int
}

func newDiagnosticCaptureBudget() *diagnosticCaptureBudget {
	return &diagnosticCaptureBudget{windows: map[string]diagnosticWindowState{}, pending: map[string]diagnosticReceipt{}}
}
func diagnosticKey(payload map[string]any) string {
	encoded, _ := json.Marshal([]any{payload["failure_ref"], payload["scope"]})
	return string(encoded)
}
func windowBytes(key string, window diagnosticWindowState) int {
	encoded, _ := json.Marshal(window)
	return len(key) + len(encoded)
}
func (budget *diagnosticCaptureBudget) bytes() int {
	total := budget.held
	for key, window := range budget.windows {
		total += windowBytes(key, window)
	}
	return total
}
func (budget *diagnosticCaptureBudget) pendingCount() int { return len(budget.pending) }
func (budget *diagnosticCaptureBudget) prune(now time.Time) {
	for key, window := range budget.windows {
		if window.Pending == 0 && !now.Before(window.StartsAt.Add(diagnosticWindow)) {
			delete(budget.windows, key)
		}
	}
}
func (budget *diagnosticCaptureBudget) register(reference map[string]any, eventID string, now time.Time) bool {
	budget.prune(now)
	key := diagnosticKey(reference)
	window, exists := budget.windows[key]
	if exists && window.FailureID != "" && window.FailureID != eventID {
		window.Ambiguous = true
		budget.windows[key] = window
		return false
	}
	if !exists {
		if len(budget.windows) >= 50 {
			return false
		}
		window = diagnosticWindowState{StartsAt: now, Attempts: map[string]bool{}}
	}
	previous := 0
	if exists {
		previous = windowBytes(key, window)
	}
	window.FailureID = eventID
	if window.FailureAt.IsZero() {
		window.FailureAt = now
	}
	if budget.bytes()-previous+windowBytes(key, window) > diagnosticMaxBytes {
		return false
	}
	budget.windows[key] = window
	return !window.Ambiguous
}
func (budget *diagnosticCaptureBudget) reserve(id string, payload map[string]any, bytes int, now time.Time) bool {
	if bytes < 0 || bytes > diagnosticMaxBytes {
		return false
	}
	if _, exists := budget.pending[id]; exists {
		return false
	}
	budget.prune(now)
	key := diagnosticKey(payload)
	window, exists := budget.windows[key]
	if !exists {
		if len(budget.windows) >= 50 {
			return false
		}
		window = diagnosticWindowState{StartsAt: now, Attempts: map[string]bool{}}
	}
	at := window.FailureAt
	if at.IsZero() {
		at = window.StartsAt
	}
	if window.Ambiguous || now.Before(window.StartsAt) || now.After(at.Add(diagnosticWindow)) || window.Observations >= 20 {
		return false
	}
	oldCost := 0
	if exists {
		oldCost = windowBytes(key, window)
	}
	attempts := make(map[string]bool, len(window.Attempts)+1)
	for attempt := range window.Attempts {
		attempts[attempt] = true
	}
	window.Attempts = attempts
	kind, _ := payload["kind"].(string)
	attempt, _ := payload["attempt_id"].(string)
	if kind == "recovery" {
		if !attempts[attempt] && len(attempts) >= 5 {
			return false
		}
		attempts[attempt] = true
	} else {
		if window.Resource || window.FailureID != stringValue(payload["source_event_id"]) || window.FailureAt.IsZero() || now.After(window.FailureAt.Add(30*time.Second)) {
			return false
		}
		window.Resource = true
	}
	window.Observations++
	window.Pending++
	cost := bytes + len(id) + len(key) + 64
	if budget.bytes()-oldCost+windowBytes(key, window)+cost > diagnosticMaxBytes {
		return false
	}
	budget.windows[key] = window
	budget.pending[id] = diagnosticReceipt{key, kind, attempt, bytes, cost}
	budget.held += cost
	return true
}
func (budget *diagnosticCaptureBudget) eligible(id string, payload map[string]any) bool {
	receipt, ok := budget.pending[id]
	window, exists := budget.windows[diagnosticKey(payload)]
	return ok && exists && receipt.key == diagnosticKey(payload) && !window.Ambiguous && (payload["kind"] == "recovery" || window.FailureID == stringValue(payload["source_event_id"]))
}
func (budget *diagnosticCaptureBudget) replace(original, id string, payload map[string]any, bytes int, now time.Time) bool {
	receipt, exists := budget.pending[original]
	if exists && receipt.key == diagnosticKey(payload) && receipt.kind == stringValue(payload["kind"]) && receipt.attempt == stringValue(payload["attempt_id"]) {
		if id != original {
			if _, collision := budget.pending[id]; collision {
				budget.release(original)
				return false
			}
		}
		delta := bytes - receipt.bytes + len(id) - len(original)
		if bytes < 0 || bytes > diagnosticMaxBytes || budget.bytes()+delta > diagnosticMaxBytes || !budget.eligible(original, payload) {
			budget.release(original)
			return false
		}
		delete(budget.pending, original)
		receipt.bytes = bytes
		receipt.cost += delta
		budget.held += delta
		budget.pending[id] = receipt
		return true
	}
	budget.release(original)
	return budget.reserve(id, payload, bytes, now)
}
func (budget *diagnosticCaptureBudget) release(id string) {
	receipt, exists := budget.pending[id]
	if !exists {
		return
	}
	delete(budget.pending, id)
	budget.held -= receipt.cost
	window, exists := budget.windows[receipt.key]
	if exists {
		window.Pending = max(0, window.Pending-1)
		budget.windows[receipt.key] = window
	}
}
func (budget *diagnosticCaptureBudget) clear() {
	clear(budget.pending)
	clear(budget.windows)
	budget.held = 0
}
