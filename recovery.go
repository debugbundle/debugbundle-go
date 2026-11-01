package debugbundle

import (
	"context"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
)

// EvidenceScope identifies one page or owned operation, using a fresh UUID v4.
// Exactly one of PageID and OperationID must match Kind.
type EvidenceScope struct {
	Kind        string
	PageID      string
	OperationID string
}

// RecoveryObservation records application knowledge. StatusCode does not imply recovery.
// Duration is optional; zero may be explicitly reported using a pointer.
type RecoveryObservation struct {
	FailureRef string
	AttemptID  string
	State      string
	Scope      *EvidenceScope
	Operation  string
	StatusCode int
	Duration   *time.Duration
}

func validEvidenceID(id string) bool {
	return evidence.Reference(map[string]any{"version": float64(1), "failure_ref": id, "scope": map[string]any{"kind": "operation", "operation_id": id}})
}
func (client *Client) resolveEvidenceScope(owner *operationLease, explicit *EvidenceScope) map[string]any {
	if explicit == nil {
		if !owner.active(client.operations) {
			return nil
		}
		return map[string]any{"kind": "operation", "operation_id": owner.id}
	}
	var scope map[string]any
	if explicit.Kind == "page" && explicit.OperationID == "" {
		scope = map[string]any{"kind": "page", "page_id": explicit.PageID}
	}
	if explicit.Kind == "operation" && explicit.PageID == "" {
		scope = map[string]any{"kind": "operation", "operation_id": explicit.OperationID}
	}
	if !evidence.Reference(map[string]any{"version": float64(1), "failure_ref": "00000000-0000-4000-8000-000000000000", "scope": scope}) {
		return nil
	}
	return scope
}

// CaptureRecovery is opt-in and best effort. It performs no retry action and never
// infers semantic recovery from a successful request or completed attempt.
func (client *Client) CaptureRecovery(ctx context.Context, observation RecoveryObservation) {
	defer func() { _ = recover() }()
	if client == nil || !client.config.captureRecoveryObservations || !client.evidence.Supports("recovery_v1") {
		return
	}
	if !validEvidenceID(observation.FailureRef) || !validEvidenceID(observation.AttemptID) {
		return
	}
	switch observation.State {
	case "attempt_started", "attempt_completed", "attempt_failed", "recovery_observed", "failure_persisted":
	default:
		return
	}
	_, owner := captureContextValues(ctx)
	scope := client.resolveEvidenceScope(owner, observation.Scope)
	if scope == nil {
		return
	}
	payload := map[string]any{"version": 1, "kind": "recovery", "failure_ref": observation.FailureRef, "scope": scope, "attempt_id": observation.AttemptID, "state": observation.State}
	if observation.Operation != "" {
		if len(observation.Operation) > 64 {
			return
		}
		payload["operation"] = observation.Operation
	}
	if observation.StatusCode != 0 {
		if observation.StatusCode < 100 || observation.StatusCode > 599 {
			return
		}
		payload["status_code"] = observation.StatusCode
	}
	if observation.Duration != nil {
		if *observation.Duration < 0 {
			return
		}
		payload["duration_ms"] = float64(*observation.Duration) / float64(time.Millisecond)
	}
	client.capture(ctx, "diagnostic_context", payload)
}

// CaptureRecovery uses the installed default client, if any.
func CaptureRecovery(ctx context.Context, observation RecoveryObservation) {
	defaultClientMu.RLock()
	client := defaultClient
	defaultClientMu.RUnlock()
	if client != nil {
		client.CaptureRecovery(ctx, observation)
	}
}
