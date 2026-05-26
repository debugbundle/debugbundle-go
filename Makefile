GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOLANGCI_LINT ?= golangci-lint
SMOKE_GO_IMAGE ?= golang:1.26-bookworm

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

.PHONY: smoke
smoke:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh smoke/run_app_driven_smoke.sh --source local

.PHONY: smoke-published
smoke-published:
	@if [ -z "$(VERSION)" ]; then echo "VERSION is required for smoke-published" >&2; exit 1; fi
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh smoke/run_app_driven_smoke.sh --source published --version $(VERSION)

.PHONY: smoke-module
smoke-module: smoke

.PHONY: verify
verify: test vet mod-check

.PHONY: verify-race
verify-race: test-race vet mod-check
