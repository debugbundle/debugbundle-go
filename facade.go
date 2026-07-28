package debugbundle

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func Recover(ctx context.Context) {
	if recovered := recover(); recovered != nil {
		if client := getDefaultClient(); client != nil {
			client.CaptureException(ctx, fmt.Errorf("panic recovered: %v", recovered))
		}
	}
}

func Go(ctx context.Context, runner func(context.Context)) {
	go func() {
		defer Recover(ctx)
		runner(ctx)
	}()
}

func CapturePanics() {
	if client := getDefaultClient(); client != nil {
		client.SetContext("panic_capture", true)
	}
}

func getDefaultClient() *Client {
	defaultClientMu.RLock()
	defer defaultClientMu.RUnlock()
	return defaultClient
}

func CaptureException(err error, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureException(ctx, err)
	}
}

func CaptureError(err error, ctxs ...context.Context) {
	CaptureException(err, ctxs...)
}

func CaptureLog(message string, level LogLevel, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureLog(ctx, message, level, nil)
	}
}

func CaptureLogWithContext(message string, level LogLevel, fields map[string]any, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureLog(ctx, message, level, fields)
	}
}

func CaptureRequest(request *http.Request, response ResponseInfo, ctxs ...context.Context) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	if client := getDefaultClient(); client != nil {
		client.CaptureRequest(ctx, request, response)
	}
}

func CaptureMessage(message string, options ...MessageOption) {
	if client := getDefaultClient(); client != nil {
		client.CaptureMessage(context.Background(), message, options...)
	}
}

func SetContext(key string, value any) {
	if client := getDefaultClient(); client != nil {
		client.SetContext(key, value)
	}
}

func Probe(label string, data any, options ...ProbeOption) {
	if client := getDefaultClient(); client != nil {
		client.Probe(context.Background(), label, data, options...)
	}
}

func ProbeLazy(label string, data func() any, options ...ProbeOption) {
	if client := getDefaultClient(); client != nil {
		client.ProbeLazy(context.Background(), label, data, options...)
	}
}

func Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if client := getDefaultClient(); client != nil {
		return client.Flush(ctx)
	}
	return nil
}

func Status() SDKStatus {
	if client := getDefaultClient(); client != nil {
		return client.Status()
	}
	return StatusDisconnected
}

func LastEventAt() *time.Time {
	if client := getDefaultClient(); client != nil {
		return client.LastEventAt()
	}
	return nil
}

func Diagnostics() []string {
	if client := getDefaultClient(); client != nil {
		client.mu.Lock()
		defer client.mu.Unlock()
		return append([]string{}, client.diagnostics...)
	}
	return nil
}
