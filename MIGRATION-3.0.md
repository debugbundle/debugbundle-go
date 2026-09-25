# Go SDK v3 safety migration

The v2 module stays at `github.com/debugbundle/debugbundle-go/v2`; upgrading requires an explicit v3 import path and tag. Keep a pinned v2 build available during rollout.

Update every root and adapter import to `github.com/debugbundle/debugbundle-go/v3` and pin one `v3.0.0` module requirement. Run the application and logger/relay smokes before deployment. Keep the existing v2 tag available for rollback.

The public method names and event envelope remain the same. The behavior changes that require a new major module path are:

- `CaptureLog`, `CaptureMessage`, and automatic logger adapters reject a record below the effective local and remote level before constructing its event or invoking `BeforeSend`. A hook can no longer promote a rejected record. Eligible standalone INFO remains available when both local level and server policy allow it.
- `BeforeSend` runs after admission on the bounded sender goroutine. It sees a sanitized canonical event and retains its drop, valid replacement, and safe-original fallback behavior. An accepted replacement is protected and policy-checked again before transport. Do not use this hook for request-thread side effects or assume it runs before `Capture*` returns.
- `New` does not wait for remote configuration or local file directory creation. Connected clients start with the minimal policy until the first successful configuration response. Explicit `RefreshRemoteConfigNow` remains available for controlled startup sequences; an overlapping refresh does not start another fetch.
- Pending plus in-flight events have a hard count and byte budget. ERROR exceptions can displace queued lower-priority logs; finite memory means even errors can be dropped under all-error overload. Queue and suppression pressure become bounded `error_suppressed` aggregate telemetry.
- A fully occupied queue now rejects an exception before invoking its application-defined `Error()` renderer when no pending lower-priority event can be displaced. 5xx request events receive the same eviction priority as exceptions under queue pressure. Brief capture-lock contention drops the event rather than waiting for a sender or another capture; its loss count is folded into the next sender pass.
- Automatic batch delivery remains background work. Explicit `Flush(ctx)` waits only up to the configured request timeout or the caller deadline and may return while a custom sender that ignores cancellation is still stalled. Close the client during application shutdown; a timed-out custom sender cannot be forcibly terminated by the SDK.
- A custom `error.Error()` renderer is isolated to at most one bounded worker call. If it stalls or panics, the event keeps the error type and stack and uses a placeholder message. Avoid side effects in error renderers.

For local-only mode, the destination directory is validated and created by the sender, not by `New`. A failed local destination never falls back to the connected HTTP endpoint. Check the configured path and the local delivery status during migration.

Validate a burst of filtered INFO logs, a stalled sender, queue pressure, retry acknowledgements, logger adapters, browser relay, and shutdown in the installed application's runtime.

Custom transport and remote-config callback panics are contained by the SDK and treated as ordinary retryable failures. They cannot escape a background SDK goroutine and terminate the application; pending ownership and restrictive config remain in force until a successful retry.
