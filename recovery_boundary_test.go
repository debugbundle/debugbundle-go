package debugbundle

import (
	"context"
	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"testing"
	"time"
)

func TestRecoveryCanonicalPrivacyBoundary(t *testing.T) {
	client := New(Config{ProjectToken: "synthetic", CaptureRecoveryObservations: true, ProjectMode: ProjectModeLocalOnly, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"recovery_v1"}}})
	defer func() { _ = client.Close() }()
	failure, _ := freshOperationID()
	attempt, _ := freshOperationID()
	ctx, end := client.BeginOperation(context.Background(), OperationOptions{})
	defer end()
	_, owner := captureContextValues(ctx)
	scope := client.resolveEvidenceScope(owner, nil)
	if scope == nil {
		t.Fatal("no owner scope")
	}
	payload := map[string]any{"version": 1, "kind": "recovery", "failure_ref": failure, "scope": scope, "attempt_id": attempt, "state": "attempt_started"}
	event := client.newEventEnvelope("diagnostic_context", time.Now(), payload)
	owned, err := evidence.CopyJSON(event)
	valid, checkErr := evidence.Check(owned)
	if !valid || err != nil || checkErr != nil {
		t.Fatalf("raw canonical failed %v %v %v", valid, err, checkErr)
	}
	if !client.config.captureRecoveryObservations || !client.capturePolicy.CaptureDiagnosticContext {
		t.Fatal("local flags false")
	}
	if client.protectEvent(event) == nil {
		t.Fatal("mandatory privacy dropped canonical recovery")
	}
	redacted := toObjectMap(client.redactor.Redact(payload))
	event.Payload = redacted
	if client.protectEvent(event) == nil {
		t.Fatalf("legacy redaction dropped recovery: %#v", redacted)
	}
	client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "attempt_started"})
	if len(client.buffer) != 1 {
		t.Fatalf("did not queue: %d", len(client.buffer))
	}
}
