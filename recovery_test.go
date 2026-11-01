package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

func TestExplicitRecoverySharesOwnedFailureScopeAndNeverInfersSuccessFromHTTP(t *testing.T) {
	waitForErrorRenderer(t)
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, CaptureRecoveryObservations: true,
		EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1", "recovery_v1"}}})
	defer func() { _ = client.Close() }()
	failure, _ := freshOperationID()
	attempt, _ := freshOperationID()
	_ = client.RunWithOperationContext(context.Background(), OperationOptions{Kind: "job", Name: "queue.process"}, func(ctx context.Context) error {
		client.CaptureException(ctx, errors.New("recovery fixture"), WithFailureReference(failure))
		client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "attempt_completed", StatusCode: 200})
		client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "recovery_observed"})
		return nil
	})
	_ = client.Flush(context.Background())
	events := []EventEnvelope{}
	for _, request := range recorder.requests {
		for _, raw := range request.Events {
			var event EventEnvelope
			_ = json.Unmarshal(raw, &event)
			events = append(events, event)
		}
	}
	if len(events) != 3 {
		t.Fatalf("expected primary plus two observations, got %d", len(events))
	}
	reference := events[0].Context["evidence_reference"].(map[string]any)
	for _, event := range events {
		if !validBeforeSendEvent(event) {
			t.Fatal("noncanonical recovery event")
		}
		if event.EventType == "diagnostic_context" {
			if !reflect.DeepEqual(event.Payload["scope"], reference["scope"]) {
				t.Fatal("recovery changed failure scope")
			}
		}
	}
	if events[1].Payload["state"] != "attempt_completed" || events[2].Payload["state"] != "recovery_observed" {
		t.Fatal("HTTP status inferred semantic success")
	}
}

func TestRecoveryBudgetStaysChargedWhileOfflineAndHookIdentityChangesRemainOwned(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: 503, RetryAfter: time.Hour}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, CaptureRecoveryObservations: true, BatchSize: 1000, FlushInterval: time.Hour,
		EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"recovery_v1"}}, BeforeSend: func(event EventEnvelope) *EventEnvelope { event.EventID = newEventID(); return &event }})
	defer func() { _ = client.Close() }()
	failure, _ := freshOperationID()
	attempt, _ := freshOperationID()
	_ = client.RunWithOperationContext(context.Background(), OperationOptions{Kind: "command"}, func(ctx context.Context) error {
		for index := 0; index < 100; index++ {
			client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "attempt_started"})
		}
		return nil
	})
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 20 {
		t.Fatal("offline recovery exceeded its observation budget")
	}
	client.mu.Lock()
	count := client.diagnosticBudget.pendingCount()
	size := client.diagnosticBudget.bytes()
	client.mu.Unlock()
	if count != 20 || size > 65536 {
		t.Fatal("accepted hook identities lost diagnostic ownership")
	}
}

func TestRecoveryReceiptsReleaseOnHookDropACKPolicyAndClose(t *testing.T) {
	for _, mode := range []string{"drop", "ack", "minimal", "close"} {
		t.Run(mode, func(t *testing.T) {
			recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
			config := Config{ProjectToken: "synthetic", Transport: recorder, CaptureRecoveryObservations: true, BatchSize: 1000, FlushInterval: time.Hour, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"recovery_v1"}}}
			if mode == "drop" {
				config.BeforeSend = func(EventEnvelope) *EventEnvelope { return nil }
			}
			client := New(config)
			defer func() { _ = client.Close() }()
			failure, _ := freshOperationID()
			attempt, _ := freshOperationID()
			_ = client.RunWithOperationContext(context.Background(), OperationOptions{}, func(ctx context.Context) error {
				client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "attempt_started"})
				return nil
			})
			if client.diagnosticBudget.pendingCount() != 1 {
				t.Fatal("missing capture receipt")
			}
			if mode == "minimal" {
				client.mu.Lock()
				client.capturePolicy = minimalCapturePolicy()
				client.mu.Unlock()
			}
			if mode == "close" {
				_ = client.Close()
			} else {
				_ = client.Flush(context.Background())
			}
			client.mu.Lock()
			pending := client.diagnosticBudget.pendingCount()
			client.mu.Unlock()
			if pending != 0 {
				t.Fatal("terminal path leaked receipt")
			}
			if (mode == "drop" || mode == "minimal" || mode == "close") && len(recorder.requests) != 0 {
				t.Fatal("withheld observation sent")
			}
		})
	}
}
func TestRecoveryDisabledOrUnknownDestinationDoesNotCapture(t *testing.T) {
	for _, config := range []Config{{ProjectToken: "synthetic", CaptureRecoveryObservations: true}, {ProjectToken: "synthetic", EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"recovery_v1"}}}} {
		recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
		config.Transport = recorder
		client := New(config)
		failure, _ := freshOperationID()
		attempt, _ := freshOperationID()
		_ = client.RunWithOperationContext(context.Background(), OperationOptions{}, func(ctx context.Context) error {
			client.CaptureRecovery(ctx, RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "recovery_observed"})
			return nil
		})
		_ = client.Flush(context.Background())
		_ = client.Close()
		if len(recorder.requests) != 0 {
			t.Fatal("recovery escaped opt-in/destination gate")
		}
	}
}

func TestRecoveryExplicitScopeValidationAndFacade(t *testing.T) {
	recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, CaptureRecoveryObservations: true, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"recovery_v1"}}})
	defer func() { _ = client.Close() }()
	failure, _ := freshOperationID()
	attempt, _ := freshOperationID()
	operation, _ := freshOperationID()
	scope := EvidenceScope{Kind: "operation", OperationID: operation}
	duration := time.Second
	valid := RecoveryObservation{FailureRef: failure, AttemptID: attempt, State: "attempt_completed", Scope: &scope, Operation: "queue.fixture", StatusCode: 200, Duration: &duration}
	client.CaptureRecovery(context.Background(), valid)
	invalid := []RecoveryObservation{}
	for _, change := range []func(*RecoveryObservation){func(v *RecoveryObservation) { v.FailureRef = "bad" }, func(v *RecoveryObservation) { v.AttemptID = "bad" }, func(v *RecoveryObservation) { v.State = "HTTP 200" }, func(v *RecoveryObservation) { v.Scope = &EvidenceScope{Kind: "page", PageID: "bad"} }, func(v *RecoveryObservation) {
		v.Scope = &EvidenceScope{Kind: "page", PageID: failure, OperationID: operation}
	}, func(v *RecoveryObservation) { v.Operation = string(make([]byte, 65)) }, func(v *RecoveryObservation) { v.StatusCode = 600 }, func(v *RecoveryObservation) { negative := -time.Second; v.Duration = &negative }} {
		copy := valid
		change(&copy)
		invalid = append(invalid, copy)
	}
	for _, observation := range invalid {
		client.CaptureRecovery(context.Background(), observation)
	}
	values, _ := client.mergedContextWithOwner(map[string]any{}, context.Background(), WithFailureReference(failure, scope))
	if values["evidence_reference"] == nil {
		t.Fatal("explicit primary reference lost")
	}
	values, _ = client.mergedContextWithOwner(map[string]any{}, context.Background(), WithFailureReference("invalid"))
	if values["evidence_reference"] != nil {
		t.Fatal("invalid primary reference retained")
	}
	if client.resolveEvidenceScope(nil, nil) != nil || client.resolveEvidenceScope(nil, &EvidenceScope{Kind: "page", PageID: failure}) == nil {
		t.Fatal("explicit scope validation mismatch")
	}
	defaultClientMu.Lock()
	previous := defaultClient
	defaultClient = client
	defaultClientMu.Unlock()
	defer func() { defaultClientMu.Lock(); defaultClient = previous; defaultClientMu.Unlock() }()
	CaptureRecovery(context.Background(), valid)
	_ = client.Flush(context.Background())
	if len(recorder.requests) != 1 || len(recorder.requests[0].Events) != 2 {
		t.Fatal("invalid observations retained or facade lost valid event")
	}
}
