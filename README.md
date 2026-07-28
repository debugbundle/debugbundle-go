# DebugBundle Go SDK

DebugBundle for Go captures backend exceptions, request failures, structured logs, probe data, and browser relay traffic with instance-first APIs that fit standard Go services.

## Installation

```bash
go get github.com/debugbundle/debugbundle-go@latest
```

The root module ships the core client plus optional subpackages for `net/http`, Gin, Echo, `log/slog`, zap, zerolog, and the browser relay handler.

## Configuration Reference

Configuration sources and precedence:

1. Explicit `debugbundle.Config{...}` fields always win.
2. `Environment` falls back to `DEBUGBUNDLE_ENVIRONMENT`, `APP_ENV`, `ENVIRONMENT`, then `GO_ENV`.
3. `Service` falls back to the current executable basename.
4. Built-in defaults apply for endpoint, transport paths, batching, probe buffers, and log level.

Capture-policy fields are server-owned and are not accepted in local SDK config. The SDK learns capture policy through `GET /v1/sdk/config` and applies it locally before transport.

| Field | Default | Purpose |
| --- | --- | --- |
| `ProjectToken` | required for connected mode | Server-side write-only DebugBundle project token. |
| `Enabled` | `true` when `ProjectToken` is set | Global kill switch. |
| `Environment` | env auto-detect, then `development` | Service environment name. |
| `Service` | executable basename, then `go-service` | Service name used in event envelopes. |
| `Endpoint` | `https://api.debugbundle.com/v1/events` | Connected ingestion endpoint. |
| `ProjectMode` | `connected` | `connected` or `local-only`. |
| `LocalEventsDir` | `.debugbundle/local/events` | Local file transport destination. |
| `SpoolDir` | `.debugbundle/local/browser-relay-spool` | Durable browser relay spool destination. |
| `BatchSize` | `25` | Max events per flush batch. |
| `FlushInterval` | `5s` | Max delay before background flush. |
| `ProbesPollInterval` | `60s` | Remote config and probe polling interval. |
| `SampleRate` | `1.0` | Per-event sample rate. |
| `LogLevel` | `warning` | Minimum captured log severity. |
| `RequestTimeout` | `5s` | HTTP timeout for connected transport and remote config fetches. |
| `RedactFields` | built-in sensitive field list | Additional field names to redact before buffering or transport. |
| `MaxProbeLabels` | `50` | Max distinct probe labels held in memory. |
| `MaxProbeEntriesPerLabel` | `10` | Ring-buffer size per probe label. |
| `ProbeFlushOnError` | `true` | Flush probe buffers with exceptions. |
| `Transport` | internal default transport | Advanced override for tests or custom senders. |
| `RemoteConfigFetcher` | default HTTP fetcher in connected mode | Advanced override for tests or custom config fetches. |

## Install Examples by Mode

### Connected `net/http`

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundlehttp"
	"github.com/debugbundle/debugbundle-go/debugbundleslog"
)

func main() {
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: os.Getenv("DEBUGBUNDLE_TOKEN"),
		Service:      "checkout-api",
		Environment:  "production",
	})
	defer func() {
		_ = client.Flush(context.Background())
	}()

	logger := slog.New(debugbundleslog.NewHandler(client, slog.NewJSONHandler(os.Stdout, nil)))

	mux := http.NewServeMux()
	mux.HandleFunc("/checkout", func(writer http.ResponseWriter, request *http.Request) {
		client.CaptureException(request.Context(), errors.New("checkout failed"))
		logger.ErrorContext(request.Context(), "checkout failed", "route", request.URL.Path)
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})

	handler := debugbundlehttp.Middleware(client, debugbundlehttp.Options{RecoverPanics: true})(mux)
	_ = http.ListenAndServe(":8080", handler)
}
```

### Local-only mode

Use local-only mode for offline development, incident repros, or environments that should write event files to disk instead of sending them remotely.

```go
client := debugbundle.New(debugbundle.Config{
	ProjectToken:   "dbundle_proj_local",
	Service:        "checkout-api",
	Environment:    "development",
	ProjectMode:    debugbundle.ProjectModeLocalOnly,
	LocalEventsDir: ".debugbundle/local/events",
})
```

### Gin and Echo

```go
router := gin.New()
router.Use(debugbundlegin.Middleware(client))
```

```go
app := echo.New()
app.Use(debugbundleecho.Middleware(client))
```

### Logger integration

`log/slog` is built in through `debugbundleslog`. Zap and zerolog stay optional so the core package does not force those dependencies on every service.

```go
logger := slog.New(debugbundleslog.NewHandler(client, slog.NewJSONHandler(os.Stdout, nil)))
zapLogger := zap.New(debugbundlezap.NewCore(client, zapcore.NewCore(encoder, sink, zap.InfoLevel)))
zerologLogger := zerolog.New(debugbundlezerolog.NewWriter(client, os.Stdout)).With().Timestamp().Logger()
```

### Probes

```go
client.Probe(ctx, "checkout.state", map[string]any{
	"phase":      "authorize",
	"cart_items": 3,
})

client.ProbeLazy(ctx, "checkout.sql.plan", func() any {
	return expensiveExplainPlan()
}, debugbundle.ProbeOptions{Heavy: true})
```

## Runtime and Framework Support

| Label | Runtime / framework |
| --- | --- |
| Minimum compatibility version | Go `1.21` |
| Recommended production version | The current or previous officially supported Go release |
| Installed-base compatibility lanes | Go `1.21` through `1.24` remain supported for existing fleets; older lanes are compatibility support, not secure production recommendations once upstream EOL applies |
| Rolling CI lanes | Go `1.21`, `1.22`, `1.23`, `1.24`, `1.25`, and `1.26` |
| First-class framework support | `net/http`, Gin `1.x`, Echo `4.x` |
| Out of scope for V1 | Fiber, Chi, gRPC, Go kit, AWS Lambda Go |

The buildable examples under [`examples/`](examples/) compile as part of `go test ./...`.

## Dependency Alignment

This SDK ships as one Go module. Keep every imported subpackage on the same module version by pinning the root module once:

```bash
go get github.com/debugbundle/debugbundle-go@v1.3.0
```

Then import subpackages such as `debugbundlehttp`, `relay`, `debugbundleslog`, `debugbundlegin`, and `debugbundleecho` from that same module version. Do not mix snippets from different tags when copying examples between services.

## Browser Relay

Mount the browser relay on the same origin as your frontend:

```go
mux.Handle("/debugbundle/browser", debugbundlehttp.RelayHandler(client, relay.Options{}))
```

Relay options:

| Option | Default | Purpose |
| --- | --- | --- |
| `AllowedOrigins` | empty | When unset, the relay enforces same-origin requests only. Set explicit allowed origins for split frontend/backend hosts. |
| `MaxBodyBytes` | `256 KB` | Maximum accepted request body size. |
| `RateLimitPerMinute` | `60` | Per-IP rate limiting for relay requests. |
| `DurableWrite` | `false` unless explicitly enabled in `relay.Options` | In connected mode, write the relay batch to the spool before forwarding. |
| `TrustForwardedHeader` | `false` | Trust `X-Forwarded-For` for rate limiting and origin inference behind a trusted proxy only. |
| `ProjectMode` | inherited from the SDK client | Overrides local-only vs connected relay delivery mode. |
| `ProjectToken` | inherited from the SDK client | Server-side token used for connected forwarding. |
| `Endpoint` | inherited from the SDK client | Connected forwarding destination. |
| `LocalEventsDir` | inherited from the SDK client | Local-only relay write destination. |
| `SpoolDir` | inherited from the SDK client | Connected durable spool destination. |
| `Service` | empty | Optional override for browser event `service.name`. |
| `Environment` | empty | Optional override for browser event `service.environment`. |
| `Transport` | default HTTP or file sender | Advanced override for tests or custom forwarding. |

Relay behavior:

- same-origin is the safe default; use explicit allowed origins when the frontend and backend live on different hosts.
- Requests must use `Content-Type: application/json`.
- Browser payloads are schema-checked and only the supported browser event types are accepted.
- Browser-supplied DebugBundle credentials are stripped before delivery, preserving credential isolation.
- Local-only mode writes relay batches to `.debugbundle/local/events`.
- Connected durable mode writes relay batches to `.debugbundle/local/browser-relay-spool`, then forwards them with the server-side project token.
- Connected forwarding without durable writes sends directly to the configured endpoint.
- To disable the relay entirely, do not mount the route.
- A missing token or missing endpoint means connected relay forwarding cannot succeed; treat that as a configuration error and leave the route unmounted until the server-side config is fixed.

## Service Naming

Use distinct service names when one project receives events from multiple deployables:

- Browser frontend: `checkout-web`
- Backend API: `checkout-api`
- Worker: `checkout-worker`

The Go relay preserves the browser-provided service name and environment by default. Only set `relay.Options.Service` or `relay.Options.Environment` when you intentionally want the backend relay host to rewrite browser event identity.

## Safe Startup and Status

`debugbundle.New(...)` and `debugbundle.Init(...)` never panic the host application because of missing or invalid config. When connected mode is configured without a usable project token, the client stays disabled:

- `Status()` returns `disconnected`
- `LastEventAt()` stays `nil`
- `Flush()` becomes a no-op until the server-side token is fixed

Normal status values:

- `healthy` — idle or the last flush succeeded
- `degraded` — the ingestion API returned `429` or `5xx`, so buffered events are being retained for retry
- `disconnected` — the SDK was not initialized with a usable config, or repeated transport failures exhausted the connection path

## First-Event Verification

Use an explicit test exception or message during setup:

```go
client := debugbundle.New(debugbundle.Config{
	ProjectToken: os.Getenv("DEBUGBUNDLE_TOKEN"),
	Service:      "debugbundle-first-event",
	Environment:  "staging",
})

client.CaptureException(context.Background(), errors.New("debugbundle first-event verification"))
_ = client.Flush(context.Background())
```

Then confirm the event through your mock ingestion endpoint, a staging project, or the DebugBundle CLI verification flow. This repository also ships a clean-install smoke harness that proves both backend and relay delivery through the public module surface:

```bash
make smoke
```

For an already published tag, the release path can also rerun the same app-driven smoke against the published module install:

```bash
make smoke-published VERSION=1.3.0
```

## Validation

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
make smoke
```

## Release

Stable releases are cut with `vX.Y.Z` tags. The release workflow runs `make verify`, `make verify-race`, `golangci-lint`, `make smoke`, and `make smoke-published VERSION=<tag>` before creating the GitHub release.

## Privacy Defaults

- Request and response bodies are off by default.
- Sensitive keys such as `authorization`, `cookie`, `password`, `token`, and related variants are redacted before events are stored or sent.
- Request headers use an allowlist by default.
- Add project-specific redaction keys with `Config.RedactFields` rather than capturing broad payloads.

## Examples

- net/http app: [examples/nethttp/main.go](examples/nethttp/main.go)
- Gin app: [examples/gin/main.go](examples/gin/main.go)
- Echo app: [examples/echo/main.go](examples/echo/main.go)

## Documentation

- Go SDK docs: https://debugbundle.com/docs/sdks/go
- SDK overview: https://debugbundle.com/docs/sdks
- Browser relay: https://debugbundle.com/docs/sdks/browser-relay
- Repository: https://github.com/debugbundle/debugbundle-go

## License

AGPL-3.0-only. See LICENSE.
