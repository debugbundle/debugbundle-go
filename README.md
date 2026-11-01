# DebugBundle Go SDK

DebugBundle for Go captures backend exceptions, request failures, structured logs, probe data, and browser relay traffic with instance-first APIs that fit standard Go services.

## Evidence in 3.1.0

Version 3.1.0 adds `Config.CaptureExceptionEvidence` (optional
boolean pointer, default true) and `Config.EvidenceCapabilities` (optional
`*debugbundle.EvidenceCapabilities`). A descriptor pin uses `Version: 1` and
`Features: []string{"exception_evidence_v1", ...}` and asserts a verified complete
delivery and reader path. Discovery on existing native config/ACK requests can
revoke support; no extra discovery request or poll is introduced. Unknown/older
paths retain the ordinary event shape. Native local files require a verified
reader pin; existing policy/probe polling remains available and its hosted
capabilities do not qualify an unrelated local reader.

Native causes and Go joined errors use bounded `Unwrap` inspection on the existing
single error renderer. All `Error`/`Unwrap` callbacks share the existing total
2-ms capture wait; an accessor that stalls cannot create replacement workers.
Already-retained primary messages survive later cause timeouts. Ordinary Go
errors do not retain throw stacks: evidence from `debug.Stack` is labelled
`capture`, and child traces remain unavailable. No arbitrary formatter, field dump,
local variable or argument collection is added. Application accessor allocations
and a permanently stalled renderer remain native/custom-callback limitations.

Complete owned hook envelopes are checked against the shipped canonical schema.
Plain Go struct/tag and raw JSON compatibility remains; application serializers
are withheld without running them. A canonical hook may change any allowed
field, including event ID and type. Validation exhaustion and valid unfit results
are withheld, and optional metadata reduction retains the latest accepted primary.
Review custom redaction hooks for the new fields before adoption, or disable capture.

Native ingestion/config replies are bounded to 256 KiB with identity encoding and
valid UTF-8; existing HTTP/context deadlines cover streaming reads. Native request
batches use actual encoded bytes. Unsent finalized tails remain charged and hooks
are not repeated. Only rejected enriched originals receive a compatible downgrade
when complete ACK evidence revokes support. Native file/custom transport behaviour
remains subject to its existing explicit profile. Version 3.1.0 passed the coverage, runtime and installed-artifact release gates.

`Config.CaptureRecoveryObservations` defaults to false. `BeginOperation(ctx,
OperationOptions{Kind: "request" | "command" | "job", Name: "technical.name",
Limit: configuredDuration})` returns a context and idempotent `end` function.
`RunWithOperationContext(ctx, options, callback)` ends the same revocable scope
and preserves the callback error or panic. Context copies share ownership; completed
parents and closed clients cannot claim execution or an automatic diagnostic scope.
HTTP/Gin/Echo middleware owns each request. `Limit` is configured time, never remaining.

Use a fresh UUID v4 with `WithFailureReference(failureRef)` on the primary capture,
then `CaptureRecovery(ctx, RecoveryObservation{FailureRef: failureRef, AttemptID:
attemptRef, State: "attempt_completed", StatusCode: 200})`. The instance and default
facade methods return no result. Supported states are `attempt_started`,
`attempt_completed`, `attempt_failed`, `recovery_observed` and `failure_persisted`.
HTTP 200 and completion leave recovery unknown. Only an application observation
asserts recovery. Optional `Scope *EvidenceScope` selects an explicit page/operation
UUID; page scope also needs ordinary session correlation. Optional `Operation`,
`StatusCode` and `Duration *time.Duration` are technical evidence.

Recovery remains subject to sampling, privacy, proof and capture policy; the minimal
preset disables standalone observations even if overridden. One client retains at
most 50 windows, five attempts and 20 observations per failure, with a combined
64-KiB metadata/pending allowance and five-minute capture window. Offline pending
receipts cannot renew the window. Hook replacements retain charged ownership until
ACK/drop; context-only drops do not generate synthetic queue-pressure incidents.

`CapturePanic(ctx, value, options...)` observes a panic without recovering or
rethrowing it. Adapters keep their existing recover/rethrow semantics and legacy
primary identity. This automatic path bypasses hooks and limits new metadata to
two nodes, eight frames and 4 KiB including execution/reference data. Foreign
Stringer/Formatter objects are not rendered or dumped. Native unrecoverable runtime
failures, including out-of-memory termination, cannot be captured safely.

Relays preserve closed evidence records and protected context. `relay.Options.
EvidenceCapabilities` pins the entire reader/delivery path, including local files.
Existing native ACKs discover or revoke downstream support. Opt in with the existing
`X-DebugBundle-Evidence: 1` header or `debugbundle_evidence=1` query to receive
capabilities and indexed `evidence_errors`; legacy string errors/statuses remain.
Native forwarding injects server credentials before byte fitting/chunking and
shares one five-second context deadline across chunks and compatible downgrades.
A custom sender may retain its existing status-only acceptance, but a delivered
spool marker requires a complete canonical ACK. Marker creation is exclusive and
private. A stalled custom sender owns one bounded worker until it returns.


## Installation

Version 3 changes capture and delivery timing. Existing v2 installations remain on the `github.com/debugbundle/debugbundle-go/v2` module. Install the v3 module with:

```bash
go get github.com/debugbundle/debugbundle-go/v3@v3.0.1
```

The v1 and v2 import paths and tags remain available. See [MIGRATION-3.0.md](MIGRATION-3.0.md) before updating an installed service.

The root module ships the core client plus optional subpackages for `net/http`, Gin, Echo, `log/slog`, zap, zerolog, and the browser relay handler.

Version 3 rejects logs below the effective threshold before formatting and bounds queued plus in-flight telemetry. Under a full queue, warning traffic yields to exceptions and 5xx request incidents; an all-error overload returns before calling application error renderers. Capture callers drop on brief SDK state-lock contention instead of waiting. See the migration guide for the lossy overload and callback timing contract.

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
| `RedactFields` | mandatory baseline plus `[]` additional fields | Additional field names to redact before buffering or transport; these cannot replace the mandatory baseline. |
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

	debugbundle "github.com/debugbundle/debugbundle-go/v3"
	"github.com/debugbundle/debugbundle-go/v3/debugbundlehttp"
	"github.com/debugbundle/debugbundle-go/v3/debugbundleslog"
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
go get github.com/debugbundle/debugbundle-go/v3@v3.0.1
```

Then import subpackages such as `debugbundlehttp`, `relay`, `debugbundleslog`, `debugbundlegin`, and `debugbundleecho` from that same module version. Do not mix snippets from different tags when copying examples between services.

For an existing v2 service, change each `github.com/debugbundle/debugbundle-go/v2` import to `github.com/debugbundle/debugbundle-go/v3` only when adopting the published v3 module. Review hook timing, asynchronous initial policy loading, and bounded overload behavior in the migration guide. Keep v2 pinned until the upgraded app passes its framework smoke.

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

To qualify an unpublished candidate as an installed module, use
`make smoke-module-artifact`. It builds a deterministic module ZIP and installs it
from a local file proxy into a fresh module cache without a source replacement.
The smoke verifies the exact SDK version, native HTTP evidence and recovery,
middleware and browser relay delivery. `.tmp/evidence-artifact-manifest.json`
records the source and archive hashes; it does not establish publication.
Use `SMOKE_GO_IMAGE=golang:1.21-bookworm` to repeat on the supported Go floor.

For an already published tag, the release path can also rerun the same app-driven smoke against the published module install:

```bash
make smoke-published VERSION=3.0.1
```

## Validation

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
make smoke
```

`make evidence-performance` compares the same exception and recovery calls with
evidence disabled/enabled, both with an empty queue and a full warning queue.
It reports time, allocation and retained-byte measurements and checks existing
queue and diagnostic ceilings plus original-exception priority. Queue setup and
cleanup are excluded from the measurements.

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

Apache-2.0. See LICENSE.

## Delivery acknowledgement and retry limits

The built-in HTTP transport requires the canonical ingestion acknowledgement (`accepted`, `rejected`, and `errors`). Empty responses, unrelated JSON, malformed counts, or invalid rejection indices retain the full batch with backoff. Valid acknowledgements remove accepted and terminally rejected events and retry only the indexed retryable rejections; an all-rejected batch does not advance `lastEventAt`.

File transports and explicitly supplied custom/legacy transports retain their documented bodyless success fallback. A custom transport that returns acknowledgement fields must return the complete canonical shape. Retry hints (delay seconds or HTTP dates) are bounded to five minutes and measured from response receipt, including partial/protocol acknowledgements and retryable server failures. Without a server hint, existing retry timing is preserved. Failures remain contained within the SDK's existing delivery path.

## Probe checkpoint guidance

Application instrumentation supplies probe labels and bounded data. Deploy a new
checkpoint before activating it. Remote activation affects future matching calls
when policy permits; it does not inject code or recover unretained measurements.
Built-in runtime/error/device collectors keep their documented SDK settings.

Use fixed labels and allowlisted technical states: `media.preview.checkpoint`
(stage/input kind/attempt), `auth.session.checkpoint` (observed session presence
and result), `job.dependency.checkpoint` (named dependency/attempt/result), or
`cache.preview.checkpoint` (hit/miss/coarse age bucket). Omit customer identifiers,
tokens/cookies, cache keys, URLs and raw payloads. HTTP 200 or attempt completion
alone does not establish semantic recovery.

Empty probe data means no matching retained observation for the selected sample
window. It does not confirm activation delivery to every SDK or execution of a
label. Check the deployed SDK/wrapper version, instrumentation, scope, expiry,
policy, sampling and delivery receipts. See the [probe recipes](https://debugbundle.com/docs/probes#bounded-checkpoint-recipes).
