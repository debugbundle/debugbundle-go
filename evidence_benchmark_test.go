package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

// Compare the same exception/recovery calls with evidence disabled and enabled.
// Setup and queue seeding are excluded from Go's elapsed/allocation counters.
// No application formatter, hook or network work is included in the measurement.
func BenchmarkEvidenceCapture(b *testing.B) {
	for _, pressure := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			b.Run(fmt.Sprintf("pressure=%t/evidence=%t", pressure, enabled), func(b *testing.B) {
				benchmarkEvidenceCapture(b, pressure, enabled)
			})
		}
	}
}

func benchmarkEvidenceCapture(b *testing.B, pressure, enabled bool) {
	b.StopTimer()
	config := Config{
		ProjectToken: "synthetic", BatchSize: maxPendingEvents + 1, FlushInterval: time.Hour,
		Transport:                &recordingTransport{response: transport.Response{StatusCode: 202}},
		CaptureExceptionEvidence: &enabled, CaptureRecoveryObservations: enabled,
		EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1", "recovery_v1"}},
	}
	var seeded []queuedEvent
	seededBytes := 0
	if pressure {
		seed := New(config)
		for index := 0; index < maxPendingEvents; index++ {
			seed.CaptureLog(context.Background(), fmt.Sprintf("pressure event %d", index), LevelWarning, nil)
		}
		seed.mu.Lock()
		seeded = append([]queuedEvent(nil), seed.buffer...)
		seededBytes = seed.bufferBytes
		seed.mu.Unlock()
		_ = seed.Close()
		if len(seeded) != maxPendingEvents {
			b.Fatalf("expected a genuinely full queue, got %d", len(seeded))
		}
	}
	original := fmt.Errorf("outer: %w", errors.Join(errors.New("first cause"), errors.New("second cause")))
	reference := "00000000-0000-4000-8000-000000000001"
	scope := EvidenceScope{Kind: "operation", OperationID: "00000000-0000-4000-8000-000000000002"}
	observation := RecoveryObservation{FailureRef: reference, AttemptID: "00000000-0000-4000-8000-000000000003", Scope: &scope, State: "attempt_completed", StatusCode: 200}
	peakQueue, peakDiagnostics := 0, 0
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		client := New(config)
		client.mu.Lock()
		client.buffer = append(client.buffer, seeded...)
		client.bufferBytes = seededBytes
		client.pendingLowPriority = len(seeded)
		client.mu.Unlock()
		b.StartTimer()
		client.CaptureException(context.Background(), original, WithFailureReference(reference, scope))
		client.CaptureRecovery(context.Background(), observation)
		b.StopTimer()
		client.mu.Lock()
		queueBytes := client.bufferBytes + client.inFlightBytes
		diagnosticBytes := client.diagnosticBudget.bytes()
		if queueBytes > maxPendingBytes || len(client.buffer)+client.inFlightCount > maxPendingEvents || diagnosticBytes > diagnosticMaxBytes {
			b.Fatal("capture exceeded an existing queue/evidence ceiling")
		}
		found := false
		for _, queued := range client.buffer {
			var event EventEnvelope
			if json.Unmarshal(queued.encoded, &event) == nil && event.EventType == "backend_exception" {
				_, hasGraph := event.Payload["exception_evidence"]
				if hasGraph != enabled {
					b.Fatal("evidence mode was not exercised")
				}
				found = true
			}
		}
		if !found {
			b.Fatal("queue pressure displaced the primary failure")
		}
		peakQueue = max(peakQueue, queueBytes)
		peakDiagnostics = max(peakDiagnostics, diagnosticBytes)
		client.mu.Unlock()
		_ = client.Close()
	}
	b.ReportMetric(float64(peakQueue), "peak_queue_B")
	b.ReportMetric(float64(peakDiagnostics), "peak_diagnostic_B")
}
