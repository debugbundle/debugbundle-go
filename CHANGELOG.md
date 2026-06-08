## [Unreleased]

## [1.1.0] - 2026-06-08

### Added

- Added path-scoped immediate client-error incident promotion support in the remote capture-policy handling so explicitly configured `4xx` routes can emit standalone `request_event` incident signals without widening the status globally.

### Changed

- Unpromoted client-error request telemetry now remains context-only under repeated traffic, while `5xx` handling and explicitly promoted client-error behavior are preserved.

## [1.0.0] - 2026-05-31

### Changed

- Marked the first stable Go SDK release after the client, relay, and tagged-module smoke paths settled across the full support matrix.

## [0.1.2] - 2026-05-29

### Fixed

- Added `OPTIONS /debugbundle/browser` preflight handling and matching CORS headers for explicitly allowed split-host browser relay traffic.

### Added

- Initial Go SDK repository scaffold under `sdks/debugbundle-go`.
- Core client, redaction, HTTP and local file transport, request capture middleware, browser relay handler, and `log/slog` integration.
- Remote config fetch + polling, capture-policy enforcement, remote probe activation, and request-scoped trigger-token support.
- Remote-config polling now keeps cached state and schedules fallback retries after poll failures.
- File transport now writes contract-aligned event arrays with service-based filenames, and relay durable mode now spools, forwards, and marks delivered batches.
- Optional `debugbundlezap` core wrapper and `debugbundlezerolog` level-writer integration for structured logger capture.
- CI now validates supported Go version lanes, race tests, module listing, and golangci-lint.
- Relay coverage now vendors the shared browser relay compliance fixture matrix and enforces the canonical browser relay schema, credential stripping, and delivery behavior.
- README and buildable example programs now cover `net/http`, Gin, Echo, browser relay, logging, probes, privacy defaults, and local-only versus connected usage.
- Added `make smoke-module` and a tagged GitHub release workflow for publishable-module validation and release creation.
