GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_IMAGE ?= golangci/golangci-lint:v2.13.2
SMOKE_GO_IMAGE ?= golang:1.27-bookworm
DOCKER_RUN ?= docker run --rm
GO_DOCKER_RUN = $(DOCKER_RUN) -v debugbundle-go-module-cache:/go/pkg/mod -v debugbundle-go-build-cache:/root/.cache/go-build

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-docker
test-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test ./...

TEST_FILTER ?= TestFilteredInfoBurstDoesNotInvokeHookOrRetainEvents
TEST_PACKAGE ?= .
.PHONY: test-focused-docker
test-focused-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test -run '$(TEST_FILTER)' $(TEST_PACKAGE)

.PHONY: test-race-docker
test-race-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test -race ./...

.PHONY: evidence-performance
evidence-performance:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go test -run '^$$' -bench '^BenchmarkEvidenceCapture$$' -benchmem -benchtime=100x -count=3 .

.PHONY: format-focused-docker
format-focused-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) gofmt -w $(FORMAT_FILES)

.PHONY: format-docker
format-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) gofmt -w debugbundle.go delivery.go acknowledgement.go http_acknowledgement_test.go transport/http_transport.go transport/http_transport_test.go debugbundle_test.go capture_safety_test.go redaction/redaction.go redaction/redaction_test.go

.PHONY: coverage
coverage:
	GO="$(GO)" sh scripts/check-coverage.sh

.PHONY: coverage-docker
coverage-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh scripts/check-coverage.sh

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: vet-docker
vet-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) go vet ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run

.PHONY: lint-docker
lint-docker:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(GOLANGCI_LINT_IMAGE) golangci-lint run

.PHONY: mod-check
mod-check:
	$(GO) list -m all >/dev/null

.PHONY: smoke
smoke:
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh smoke/run_app_driven_smoke.sh --source local

.PHONY: smoke-published
smoke-published:
	@if [ -z "$(VERSION)" ]; then echo "VERSION is required for smoke-published" >&2; exit 1; fi
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh smoke/run_app_driven_smoke.sh --source published --version $(VERSION)

.PHONY: smoke-module
smoke-module: smoke

.PHONY: prepare-module-artifact smoke-module-artifact
prepare-module-artifact:
	python3 scripts/prepare-module-candidate.py

smoke-module-artifact: prepare-module-artifact
	$(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace $(SMOKE_GO_IMAGE) sh smoke/run_app_driven_smoke.sh --source artifact

.PHONY: evidence-module-matrix
evidence-module-matrix: prepare-module-artifact
	mkdir -p .tmp/evidence-module-runtime
	@set -e; for version in 1.21 1.26 1.27; do \
	  $(GO_DOCKER_RUN) -v "$(CURDIR)":/workspace -w /workspace golang:$$version-bookworm sh smoke/run_app_driven_smoke.sh --source artifact > .tmp/evidence-module-runtime/installed-$$version.log 2>&1; \
	done

.PHONY: verify
verify: test coverage vet mod-check

.PHONY: verify-docker
verify-docker: test-docker coverage-docker vet-docker

.PHONY: verify-race
verify-race: test-race vet mod-check

# Sequential lanes share one source tree and caches; retain separate receipts.
.PHONY: evidence-runtime-matrix
evidence-runtime-matrix:
	mkdir -p .tmp/evidence-runtime
	@set -e; for version in 1.21 1.26 1.27; do \
	  $(MAKE) test-docker SMOKE_GO_IMAGE=golang:$$version-bookworm > .tmp/evidence-runtime/test-$$version.log 2>&1; \
	  $(MAKE) smoke SMOKE_GO_IMAGE=golang:$$version-bookworm > .tmp/evidence-runtime/installed-$$version.log 2>&1; \
	done
