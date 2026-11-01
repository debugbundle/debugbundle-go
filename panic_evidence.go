package debugbundle

import (
	"context"
	"reflect"
	"runtime/debug"
	"strconv"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
)

// panicCaptureError deliberately avoids Stringer/Formatter and object inspection.
// Error/Unwrap accessors execute only on the existing bounded error renderer.
type panicCaptureError struct{ value any }

func (panicError *panicCaptureError) Error() string {
	message := "[application panic value unavailable]"
	if err, ok := panicError.value.(error); ok {
		message = err.Error()
	} else {
		value := reflect.ValueOf(panicError.value)
		if value.IsValid() {
			switch value.Kind() {
			case reflect.String:
				message = value.String()
			case reflect.Bool:
				message = strconv.FormatBool(value.Bool())
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				message = strconv.FormatInt(value.Int(), 10)
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				message = strconv.FormatUint(value.Uint(), 10)
			case reflect.Float32, reflect.Float64:
				message = strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits())
			}
		}
	}
	return "panic recovered: " + clipEvidence(message, 4096-len("panic recovered: "))
}
func (panicError *panicCaptureError) Unwrap() error { err, _ := panicError.value.(error); return err }

// CapturePanic records an observed panic without recovering or rethrowing it.
// Adapters retain their existing panic behaviour. Unrecoverable runtime failures
// (including out-of-memory termination) cannot be observed safely by Go SDK code.
func (client *Client) CapturePanic(ctx context.Context, value any, options ...EventOption) {
	defer func() { _ = recover() }()
	if client == nil || value == nil || !client.mayPrepareCapture(true) {
		return
	}
	stack := clipEvidence(string(debug.Stack()), 16384)
	snapshot := safeErrorSnapshot(&panicCaptureError{value}, stack, client.config.captureExceptionEvidence && client.evidence.Supports("exception_evidence_v1"), true)
	payload := map[string]any{"name": "*errors.errorString", "message": snapshot.message, "handled": false, "request": emptyRequestPayload(), "response": emptyResponsePayload(), "runtime": buildRuntimeFacts(), "stack": stack}
	if snapshot.graph != nil {
		snapshot.graph["nodes"].([]any)[0].(map[string]any)["name"] = "*errors.errorString"
		payload["exception_evidence"] = snapshot.graph
	}
	client.captureWithMode(ctx, "backend_exception", payload, true, options...)
}
func fitFatalEvidence(payload, context map[string]any) {
	graph, _ := payload["exception_evidence"].(map[string]any)
	if graph == nil {
		return
	}
	metadata := map[string]any{"exception_evidence": graph}
	if execution, exists := payload["execution"]; exists {
		metadata["execution"] = execution
	}
	if reference, exists := context["evidence_reference"]; exists {
		metadata["evidence_reference"] = reference
	}
	overhead := evidence.Size(metadata) - evidence.Size(graph)
	fitErrorEvidence(graph, max(256, 4096-overhead))
}
