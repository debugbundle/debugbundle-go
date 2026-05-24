GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOLANGCI_LINT ?= golangci-lint

.PHONY: test
test:
	$(GO) test ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run

.PHONY: mod-check
mod-check:
	$(GO) list -m all >/dev/null

.PHONY: smoke-module
smoke-module:
	@tmpdir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	printf '%s\n' \
		'module debugbundle-go-smoke' \
		'' \
		'go 1.21' \
		'' \
		'require github.com/debugbundle/debugbundle-go v0.0.0' \
		'' \
		'replace github.com/debugbundle/debugbundle-go => $(CURDIR)' > "$$tmpdir/go.mod"; \
	printf '%s\n' \
		'package main' \
		'' \
		'import (' \
		'    "context"' \
		'' \
		'    debugbundle "github.com/debugbundle/debugbundle-go"' \
		')' \
		'' \
		'func main() {' \
		'    client := debugbundle.New(debugbundle.Config{' \
		'        ProjectToken: "dbundle_proj_smoke",' \
		'        Service:      "smoke-api",' \
		'        Environment:  "test",' \
		'    })' \
		'    _ = client.Flush(context.Background())' \
		'}' > "$$tmpdir/main.go"; \
	cd "$$tmpdir" && $(GO) mod tidy && $(GO) build .

.PHONY: verify
verify: test vet mod-check

.PHONY: verify-race
verify-race: test-race vet mod-check
