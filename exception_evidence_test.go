package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/debugbundle/debugbundle-go/v3/transport"
	"net/http"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
)

func TestExceptionEvidenceRequiresVerifiedDestinationAndPreservesLegacyFields(t *testing.T) {
	for _, known := range []bool{false, true} {
		waitForErrorRenderer(t)
		recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
		var pin *EvidenceCapabilities
		if known {
			pin = &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1"}}
		}
		client := New(Config{ProjectToken: "synthetic", Transport: recorder, EvidenceCapabilities: pin})
		original := fmt.Errorf("outer: %w", errors.Join(errors.New("cause one"), errors.New("cause two")))
		client.CaptureException(context.Background(), original)
		if err := client.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		var event EventEnvelope
		if len(recorder.requests) != 1 {
			t.Fatalf("primary failure was lost, known=%v", known)
		}
		if err := json.Unmarshal(recorder.requests[0].Events[0], &event); err != nil {
			t.Fatal(err)
		}
		if event.Payload["message"] != original.Error() {
			t.Fatal("primary scalar message changed")
		}
		_, hasGraph := event.Payload["exception_evidence"]
		if hasGraph != known {
			t.Fatalf("graph=%v verified=%v", hasGraph, known)
		}
		if !validBeforeSendEvent(event) {
			t.Fatal("produced event is not canonical")
		}
		_ = client.Close()
	}
}

func TestNativeGraphRemainsCanonicalThroughExistingPrivacyStages(t *testing.T) {
	waitForErrorRenderer(t)
	err := fmt.Errorf("outer: %w", errors.Join(errors.New("one"), errors.New("two")))
	client := New(Config{ProjectToken: "synthetic", Transport: &recordingTransport{}})
	defer func() { _ = client.Close() }()
	snapshot := safeErrorSnapshot(err, string(debug.Stack()), true, false)
	payload := map[string]any{"name": "Failure", "message": snapshot.message, "stack": "retained", "handled": true,
		"request": emptyRequestPayload(), "response": emptyResponsePayload(), "runtime": buildRuntimeFacts(), "exception_evidence": snapshot.graph}
	event := client.newEventEnvelope("backend_exception", time.Now().UTC(), payload)
	if !validBeforeSendEvent(event) {
		t.Fatal("producer graph not canonical before privacy")
	}
	event.Payload = toObjectMap(client.redactor.Redact(payload))
	if !validBeforeSendEvent(event) {
		t.Fatal("ordinary redactor changed the closed producer graph")
	}
	if client.protectEvent(event) == nil {
		t.Fatal("mandatory privacy changed the closed producer graph")
	}
}

func TestNativeErrorEvidenceRetainsCausesAndJoinedErrorsWithCaptureProvenance(t *testing.T) {
	waitForErrorRenderer(t)
	original := fmt.Errorf("outer: %w", errors.Join(errors.New("first cause"), errors.New("second cause")))
	snapshot := safeErrorSnapshot(original, string(debug.Stack()), true, false)
	if snapshot.message != original.Error() {
		t.Fatal("legacy message changed")
	}
	graph := snapshot.graph
	if graph == nil || len(graph["nodes"].([]any)) != 4 {
		t.Fatal("native cause/join graph missing")
	}
	root := graph["nodes"].([]any)[0].(map[string]any)
	if root["trace_origin"] != "capture" || len(root["frames"].([]any)) == 0 {
		t.Fatal("capture stack mislabelled or lost")
	}
	if evidence.Size(graph) > 32768 {
		t.Fatal("graph exceeds metadata budget")
	}
}

type heldUnwrapError struct {
	entered chan struct{}
	release chan struct{}
}

func (held *heldUnwrapError) Error() string { return "held unwrap fixture" }
func (held *heldUnwrapError) Unwrap() error { close(held.entered); <-held.release; return nil }
func TestNativeCauseAccessorCannotHoldTheCaptureCallerOrCreateReplacementWorkers(t *testing.T) {
	waitForErrorRenderer(t)
	held := &heldUnwrapError{make(chan struct{}), make(chan struct{})}
	defer func() {
		close(held.release)
		deadline := time.Now().Add(time.Second)
		for len(errorRendererSlot) > 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}()
	start := time.Now()
	snapshot := safeErrorSnapshot(held, string(debug.Stack()), true, false)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("native cause accessor blocked capture")
	}
	select {
	case <-held.entered:
	case <-time.After(time.Second):
		t.Fatal("cause not inspected on worker")
	}
	if snapshot.message != "held unwrap fixture" {
		t.Fatal("cause timeout discarded the already retained primary message")
	}
	if snapshot.graph == nil || snapshot.graph["unavailable_reason"] != "unsafe" {
		t.Fatal("timed-out evidence must be labelled unavailable")
	}
	for index := 0; index < 50; index++ {
		if safeErrorSnapshot(errors.New("while held"), "", true, false).message != "[application error renderer busy]" {
			t.Fatal("held renderer spawned another accessor worker")
		}
	}
}

func waitForErrorRenderer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(errorRendererSlot) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(errorRendererSlot) > 0 {
		t.Fatal("previous renderer owner did not release its slot")
	}
}

func TestEvidenceProofIsLearnedOnExistingAckAndRejectedOriginalDowngradesWithoutRepeatingHook(t *testing.T) {
	waitForErrorRenderer(t)
	proof := []byte(`{"accepted":1,"rejected":0,"errors":[],"evidence_capabilities":{"version":1,"features":["exception_evidence_v1"]}}`)
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted, Body: proof}}
	hooks := 0
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, FlushInterval: time.Hour, BatchSize: 1000, BeforeSend: func(event EventEnvelope) *EventEnvelope { hooks++; return &event }})
	defer func() { _ = client.Close() }()
	client.CaptureMessage(context.Background(), "existing-request discovery")
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder.response.Body = []byte(`{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"invalid_event"}],"evidence_capabilities":{"version":1,"features":[]}}`)
	client.CaptureException(context.Background(), errors.New("preserve accepted original"))
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 2 || !strings.Contains(string(recorder.requests[1].Events[0]), "exception_evidence") {
		t.Fatal("existing ACK did not enable qualified evidence")
	}
	client.mu.Lock()
	client.retryUntil = time.Time{}
	client.mu.Unlock()
	recorder.response.Body = []byte(`{"accepted":1,"rejected":0,"errors":[],"evidence_capabilities":{"version":1,"features":[]}}`)
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 3 {
		t.Fatal("rejected enriched original was not retained for compatible downgrade")
	}
	var first, last EventEnvelope
	_ = json.Unmarshal(recorder.requests[1].Events[0], &first)
	_ = json.Unmarshal(recorder.requests[2].Events[0], &last)
	if first.EventID != last.EventID || first.Payload["message"] != last.Payload["message"] || last.Payload["exception_evidence"] != nil || hooks != 2 {
		t.Fatal("downgrade changed primary identity, retained unsupported metadata or repeated hook")
	}
}
