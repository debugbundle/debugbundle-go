package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/transport"
	"sync/atomic"
	"testing"
	"time"
)

type hostilePanicStringer struct{ calls *atomic.Int32 }

func (value hostilePanicStringer) String() string { value.calls.Add(1); return "should never render" }
func TestNativePanicEvidenceBypassesHookAndBoundsFatalMetadata(t *testing.T) {
	waitForErrorRenderer(t)
	recorder := &recordingTransport{response: transport.Response{StatusCode: 202}}
	hooks := 0
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, CaptureRecoveryObservations: true, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1", "recovery_v1"}}, BeforeSend: func(event EventEnvelope) *EventEnvelope { hooks++; return &event }})
	defer func() { _ = client.Close() }()
	failure, _ := freshOperationID()
	_ = client.RunWithOperationContext(context.Background(), OperationOptions{Kind: "job", Name: "panic.fixture"}, func(ctx context.Context) error {
		client.CapturePanic(ctx, errors.Join(errors.New("first"), errors.New("second")), WithFailureReference(failure))
		return nil
	})
	_ = client.Flush(context.Background())
	if hooks != 0 || len(recorder.requests) != 1 {
		t.Fatal("panic used hook or did not deliver")
	}
	var event EventEnvelope
	_ = json.Unmarshal(recorder.requests[0].Events[0], &event)
	if event.Payload["handled"] != false || !validBeforeSendEvent(event) {
		t.Fatal("panic event not canonical unhandled failure")
	}
	graph := event.Payload["exception_evidence"].(map[string]any)
	nodes := graph["nodes"].([]any)
	if len(nodes) > 2 {
		t.Fatal("fatal node budget exceeded")
	}
	frames := 0
	for _, raw := range nodes {
		frames += len(raw.(map[string]any)["frames"].([]any))
	}
	if frames > 8 {
		t.Fatal("fatal frame budget exceeded")
	}
	metadata := map[string]any{"exception_evidence": graph, "execution": event.Payload["execution"], "evidence_reference": event.Context["evidence_reference"]}
	if evidence.Size(metadata) > 4096 {
		t.Fatal("fatal combined metadata exceeded")
	}
}
func TestNativePanicDoesNotInvokeForeignStringer(t *testing.T) {
	calls := &atomic.Int32{}
	client := New(Config{ProjectToken: "synthetic", Transport: &recordingTransport{response: transport.Response{StatusCode: 202}}})
	defer func() { _ = client.Close() }()
	started := time.Now()
	client.CapturePanic(context.Background(), hostilePanicStringer{calls})
	if calls.Load() != 0 || time.Since(started) > 100*time.Millisecond {
		t.Fatal("capture rendered foreign panic")
	}
}
