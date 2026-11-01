package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

type contextCopyKey struct{}

func TestOperationOwnershipSurvivesNativeContextCopiesAndRevokesOnCompletion(t *testing.T) {
	waitForErrorRenderer(t)
	recorder := &recordingTransport{response: transport.Response{StatusCode: http.StatusAccepted}}
	client := New(Config{ProjectToken: "synthetic", Transport: recorder, EvidenceCapabilities: &EvidenceCapabilities{Version: 1, Features: []string{"exception_evidence_v1"}}})
	defer func() { _ = client.Close() }()
	base := ContextWithValue(context.Background(), "tenant", "fixture")
	scoped, end := client.BeginOperation(base, OperationOptions{Kind: "job", Name: "queue.process", Limit: time.Second})
	copied := ContextWithValue(context.WithValue(scoped, contextCopyKey{}, "native copy"), "detail", "kept")
	values := ContextValues(copied)
	if len(values) != 2 || values["tenant"] != "fixture" || values["detail"] != "kept" {
		t.Fatal("operation internals leaked or application context changed")
	}
	client.CaptureException(copied, errors.New("active operation"))
	end()
	end()
	client.CaptureException(copied, errors.New("completed operation"))
	_ = client.Flush(context.Background())
	events := []EventEnvelope{}
	for _, request := range recorder.requests {
		for _, raw := range request.Events {
			var event EventEnvelope
			_ = json.Unmarshal(raw, &event)
			events = append(events, event)
		}
	}
	if len(events) != 2 {
		t.Fatalf("expected two primary errors, got %d", len(events))
	}
	execution, ok := events[0].Payload["execution"].(map[string]any)
	if !ok || execution["kind"] != "job" || execution["name"] != "queue.process" || execution["limit_ms"] != float64(1000) {
		t.Fatal("active execution metadata missing")
	}
	if events[1].Payload["execution"] != nil {
		t.Fatal("completed owner revived through a copied context")
	}
}

func TestOperationCallbackPreservesResultsPanicsAndParentRevocation(t *testing.T) {
	client := New(Config{ProjectToken: "synthetic", Transport: &recordingTransport{}})
	defer func() { _ = client.Close() }()
	marker := errors.New("application result")
	if got := client.RunWithOperationContext(context.Background(), OperationOptions{Kind: "command"}, func(ctx context.Context) error { return marker }); got != marker {
		t.Fatal("callback error changed")
	}
	parent, endParent := client.BeginOperation(context.Background(), OperationOptions{Kind: "command"})
	child, endChild := client.BeginOperation(parent, OperationOptions{Kind: "job"})
	defer endChild()
	_, lease := captureContextValues(child)
	if lease == nil || !lease.active(client.operations) {
		t.Fatal("nested owner missing")
	}
	endParent()
	if lease.active(client.operations) {
		t.Fatal("completed parent did not revoke descendants")
	}
	func() {
		defer func() {
			if recover() != marker {
				t.Error("callback panic changed")
			}
		}()
		_ = client.RunWithOperationContext(context.Background(), OperationOptions{}, func(context.Context) error { panic(marker) })
	}()
}
