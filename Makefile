GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_IMAGE ?= golangci/golangci-lint:v2.13.2
SMOKE_GO_IMAGE ?= golang:1.27-bookworm

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-docker
test-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test ./...

TEST_FILTER ?= TestFilteredInfoBurstDoesNotInvokeHookOrRetainEvents
.PHONY: test-focused-docker
test-focused-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test -run $(TEST_FILTER) .

.PHONY: test-race-docker
test-race-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test -race ./...

.PHONY: format-docker
format-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) gofmt -w debugbundle.go delivery.go debugbundle_test.go capture_safety_test.go redaction/redaction.go redaction/redaction_test.go

.PHONY: coverage
coverage:
	GO="$(GO)" sh scripts/check-coverage.sh

.PHONY: coverage-docker
coverage-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh scripts/check-coverage.sh

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: vet-docker
vet-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go vet ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run

.PHONY: lint-docker
lint-docker:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(GOLANGCI_LINT_IMAGE) golangci-lint run

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
verify: test coverage vet mod-check

.PHONY: verify-docker
verify-docker: test-docker coverage-docker vet-docker

.PHONY: verify-race
verify-race: test-race vet mod-check
