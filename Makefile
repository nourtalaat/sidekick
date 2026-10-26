# Each target that belongs in `make ci` adds itself to CI_TARGETS next to its
# own rule, and each generator adds itself to GENERATE_TARGETS. A new target
# joins CI with one `CI_TARGETS += <name>` line. The `ci` and `generate` rules
# sit at the end of the file because make reads those lists when it reaches
# them.

GO ?= go
MODULE := github.com/nourtalaat/sidekick
BIN_DIR := $(CURDIR)/bin
TOOLS_DIR := $(BIN_DIR)/tools

GOLANGCI_LINT_VERSION := v2.12.2
MOCKGEN_VERSION := v0.6.0
GOVULNCHECK_VERSION := v1.9.0

GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
MOCKGEN_DIR := $(TOOLS_DIR)/mockgen-$(MOCKGEN_VERSION)
MOCKGEN := $(MOCKGEN_DIR)/mockgen
GOVULNCHECK := $(TOOLS_DIR)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck

VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

CI_TARGETS :=
GENERATE_TARGETS :=

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

CI_TARGETS += lint
.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint and check that go.mod and go.sum are tidy
	$(GOLANGCI_LINT) run
	$(GO) mod tidy -diff

.PHONY: fmt
fmt: $(GOLANGCI_LINT) ## Format the Go code
	$(GOLANGCI_LINT) fmt

CI_TARGETS += test
.PHONY: test
test: ## Run the unit tests
	$(GO) test -race ./...

# Integration tests are behind the `integration` build tag and use the
# Postgres at SIDEKICK_TEST_DATABASE_URL when it is set.
CI_TARGETS += test-integration
.PHONY: test-integration
test-integration: ## Run unit and integration tests against Postgres
	$(GO) test -race -tags integration ./...

GENERATE_TARGETS += generate-mocks
.PHONY: generate-mocks
generate-mocks: $(MOCKGEN) ## Run the mockgen directives (go generate)
	PATH="$(MOCKGEN_DIR):$$PATH" $(GO) generate ./...

CI_TARGETS += verify-generated
.PHONY: verify-generated
verify-generated: ## Fail if `make generate` changes any file
	MAKE="$(MAKE)" sh scripts/verify-generated.sh

.PHONY: migrate-new
migrate-new: ## Create the next goose migration: make migrate-new name=add_jobs
	sh scripts/migrate-new.sh "$(name)"

CI_TARGETS += build
.PHONY: build
build: ## Build bin/sidekick with the version and commit set
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/sidekick ./cmd/sidekick

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK) ## Check dependencies and code for known vulnerabilities
	$(GOVULNCHECK) ./...

$(GOLANGCI_LINT):
	GOBIN="$(dir $@)" $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(MOCKGEN):
	GOBIN="$(dir $@)" $(GO) install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

$(GOVULNCHECK):
	GOBIN="$(dir $@)" $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: generate
generate: $(GENERATE_TARGETS) ## Run every code generator

.PHONY: ci
ci: $(CI_TARGETS) ## Run everything CI runs; run it before every push
