package debugbundle

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"io"
	"sync/atomic"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/redaction"
)

// OperationOptions describes technical work. Limit is a configured bound, never remaining time.
type OperationOptions struct {
	Kind  string
	Name  string
	Limit time.Duration
}
type operationRegistry struct{ stopped atomic.Bool }
type operationLease struct {
	registry *operationRegistry
	parent   *operationLease
	ended    atomic.Bool
	id, kind string
	name     any
	limit    any
	started  time.Time
}
type operationContextValues struct {
	values map[string]any
	lease  *operationLease
}

func (lease *operationLease) active(registry *operationRegistry) bool {
	if lease == nil || lease.registry != registry || registry == nil || registry.stopped.Load() {
		return false
	}
	for parent, depth := lease, 0; parent != nil; parent, depth = parent.parent, depth+1 {
		if depth >= 32 || parent.ended.Load() {
			return false
		}
	}
	return true
}
func freshOperationID() (string, bool) {
	var bytes [16]byte
	if _, err := io.ReadFull(cryptorand.Reader, bytes[:]); err != nil {
		return "", false
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	value := hex.EncodeToString(bytes[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], true
}

// BeginOperation returns an independently owned context and idempotent completion.
// Native context copies share the revocable lease without exposing SDK state in ContextValues.
func (client *Client) BeginOperation(ctx context.Context, options OperationOptions) (scoped context.Context, end func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	scoped = ctx
	end = func() {}
	defer func() {
		if recover() != nil {
			scoped = ctx
			end = func() {}
		}
	}()
	if client == nil || !client.config.enabled || client.operations == nil || client.operations.stopped.Load() {
		return
	}
	values, parent := captureContextValues(ctx)
	if !parent.active(client.operations) {
		parent = nil
	}
	depth := 0
	for ancestor := parent; ancestor != nil; ancestor = ancestor.parent {
		depth++
	}
	if depth >= 31 {
		return
	}
	id, ok := freshOperationID()
	if !ok {
		return
	}
	kind := options.Kind
	switch kind {
	case "request", "command", "job", "unknown":
	default:
		kind = "unknown"
	}
	var name, limit any
	if options.Name != "" && len(options.Name) <= 4096 {
		if protected, err := redaction.ProtectTelemetry(options.Name, client.config.redactFields); err == nil {
			if value, ok := protected.(string); ok {
				name = clipEvidence(value, 128)
			}
		}
	}
	if options.Limit > 0 {
		limit = float64(options.Limit) / float64(time.Millisecond)
	}
	lease := &operationLease{registry: client.operations, parent: parent, id: id, kind: kind, name: name, limit: limit, started: time.Now()}
	scoped = context.WithValue(ctx, contextValuesKey, operationContextValues{values, lease})
	end = func() { lease.ended.Store(true) }
	return
}

// RunWithOperationContext preserves callback errors and panics while ending ownership.
func (client *Client) RunWithOperationContext(ctx context.Context, options OperationOptions, callback func(context.Context) error) error {
	scoped, end := client.BeginOperation(ctx, options)
	defer end()
	if callback == nil {
		return nil
	}
	return callback(scoped)
}
func (lease *operationLease) execution(registry *operationRegistry) map[string]any {
	if !lease.active(registry) {
		return nil
	}
	elapsed := float64(time.Since(lease.started)) / float64(time.Millisecond)
	if elapsed < 0 {
		elapsed = 0
	}
	return map[string]any{"version": float64(1), "kind": lease.kind, "name": lease.name, "elapsed_ms": elapsed, "limit_ms": lease.limit}
}
