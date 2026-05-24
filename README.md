# DebugBundle Go SDK

DebugBundle for Go captures backend exceptions, request failures, structured logs, and browser relay traffic with instance-first APIs that fit standard Go services.

## Scope

- Core client with singleton and instance APIs.
- Connected HTTP transport and secure local-only file transport.
- Default redaction, duplicate suppression, bounded buffering, and loop protection.
- `net/http`, Gin, and Echo middleware.
- Browser relay handling for `POST /debugbundle/browser`, including local-only writes, connected durable spool writes, connected forwarding, and shared relay compliance fixtures.
- Remote config polling, capture-policy enforcement, always-on probes, remote probes, and request-scoped trigger tokens.
- `log/slog` integration plus optional zap and zerolog adapters.
- CI validation across supported Go minors, race coverage, and golangci-lint.

## Install

```bash
go get github.com/debugbundle/debugbundle-go
```

Prefer instance clients in real services and tests. The examples under `examples/` compile as part of `go test ./...`.

## net/http

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundlehttp"
	"github.com/debugbundle/debugbundle-go/debugbundleslog"
	"github.com/debugbundle/debugbundle-go/relay"
)

func main() {
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_...",
		Service:      "checkout-api",
		Environment:  "production",
	})
	defer func() {
		_ = client.Flush(context.Background())
	}()

	logger := slog.New(debugbundleslog.NewHandler(client, slog.NewJSONHandler(os.Stdout, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("/checkout", func(writer http.ResponseWriter, request *http.Request) {
		client.Probe(request.Context(), "checkout.state", map[string]any{"phase": "authorize"})
		logger.ErrorContext(request.Context(), "checkout failed", "route", request.URL.Path)
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})
	mux.Handle("/debugbundle/browser", debugbundlehttp.RelayHandler(client, relay.Options{}))

	handler := debugbundlehttp.Middleware(client, debugbundlehttp.Options{RecoverPanics: true})(mux)
	_ = http.ListenAndServe(":8080", handler)
}
```

See `examples/nethttp/main.go` for the full buildable example.

## Gin

```go
router := gin.New()
router.Use(gin.Recovery())
router.Use(debugbundlegin.Middleware(client))
```

See `examples/gin/main.go` for a buildable local-only example.

## Echo

```go
app := echo.New()
app.Use(debugbundleecho.Middleware(client))
```

See `examples/echo/main.go` for a buildable connected-mode example.

## Browser relay

Mount the framework-neutral relay handler on the same origin as your frontend:

```go
mux.Handle("/debugbundle/browser", debugbundlehttp.RelayHandler(client, relay.Options{}))
```

The relay enforces the V1 contract for origin validation, `Content-Type: application/json`, body size limits, schema validation, credential stripping, rate limiting, local-only file writes, durable spool writes, and connected forwarding.

## Project modes

Connected mode is the default. It batches events to the configured endpoint and uses durable relay spooling unless you disable it on the relay options.

```go
client := debugbundle.New(debugbundle.Config{
	ProjectToken: "dbundle_proj_...",
	Service:      "checkout-api",
	Environment:  "production",
	Endpoint:     "https://api.debugbundle.com/v1/events",
})
```

Use local-only mode for offline development, incident repros, and privacy-sensitive environments that should write event files directly to disk.

```go
client := debugbundle.New(debugbundle.Config{
	ProjectToken:   "dbundle_proj_local",
	Service:        "checkout-api",
	Environment:    "development",
	ProjectMode:    debugbundle.ProjectModeLocalOnly,
	LocalEventsDir: ".debugbundle/local/events",
})
```

## Logging

`log/slog` is built in through `debugbundleslog`.

```go
logger := slog.New(debugbundleslog.NewHandler(client, slog.NewJSONHandler(os.Stdout, nil)))
```

Zap and zerolog stay optional so the core package does not force those dependencies on every service.

```go
zapLogger := zap.New(debugbundlezap.NewCore(client, zapcore.NewCore(encoder, sink, zap.InfoLevel)))
zerologLogger := zerolog.New(debugbundlezerolog.NewWriter(client, os.Stdout)).With().Timestamp().Logger()
```

## Probes

Always-on probes buffer per-label diagnostic snapshots and flush them with exception events by default.

```go
client.Probe(ctx, "checkout.state", map[string]any{
	"phase":      "authorize",
	"cart_items": 3,
})
```

Heavy probes stay dormant unless a matching remote activation or request-scoped trigger token enables them.

```go
client.ProbeLazy(ctx, "checkout.sql.plan", func() any {
	return expensiveExplainPlan()
}, debugbundle.ProbeOptions{Heavy: true})
```

## Privacy defaults

- Request and response bodies are off by default.
- Sensitive keys such as `authorization`, `cookie`, `password`, `token`, and related variants are redacted before events are stored or sent.
- Relay handling strips browser-supplied DebugBundle credentials and preserves only supported correlation fields.
- Add project-specific redaction keys with `Config.RedactFields` rather than capturing broad payloads.

```go
client := debugbundle.New(debugbundle.Config{
	ProjectToken: "dbundle_proj_...",
	Service:      "billing-api",
	Environment:  "production",
	RedactFields: []string{"patient_id", "insurance_number", "account_secret"},
})
```

## Validation

```bash
go test ./...
go test -race ./...
go vet ./...
go list -m
make smoke-module
golangci-lint run
```

## Release

Stable releases are cut with `vX.Y.Z` tags. The release workflow runs `make verify`, `make verify-race`, golangci-lint, and `make smoke-module` before creating the GitHub release for that tag.
