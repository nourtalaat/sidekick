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
SQLC_VERSION := v1.31.1

# Helm is not a Go module install, so the Makefile downloads the release
# archive and checks it against these checksums, from
# https://get.helm.sh/helm-<version>-<os>-<arch>.tar.gz.sha256sum.
HELM_VERSION := v4.3.0
HELM_SHA256_darwin_arm64 := d3870437e1e95b67f8edbde964156c84a26503f560821d40c542441658934fba
HELM_SHA256_darwin_amd64 := 347a784877e0e20eac865e8d1c36a80f6bb0861d6f29abd34defb6570ef95d92
HELM_SHA256_linux_amd64 := 86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb
HELM_SHA256_linux_arm64 := 31c5794dd55c66a51e6b7d2e2ac7a114ae8b1de41ff1d9ba51748ac973b06a08
HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH := $(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
HELM := $(TOOLS_DIR)/helm-$(HELM_VERSION)/helm

GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
MOCKGEN_DIR := $(TOOLS_DIR)/mockgen-$(MOCKGEN_VERSION)
MOCKGEN := $(MOCKGEN_DIR)/mockgen
GOVULNCHECK := $(TOOLS_DIR)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck
SQLC := $(TOOLS_DIR)/sqlc-$(SQLC_VERSION)/sqlc

# golangci-lint keeps one cache per user by default, so worktrees that lint
# at the same time can get each other's results. Each checkout gets its own.
GOLANGCI_LINT_CACHE := $(BIN_DIR)/cache/golangci-lint

VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

IMAGE_REPOSITORY ?= ghcr.io/nourtalaat/sidekick
IMAGE_TAG ?= $(VERSION)
IMAGE ?= $(IMAGE_REPOSITORY):$(IMAGE_TAG)
IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
CONTAINER_TOOL ?= $(if $(shell command -v docker 2>/dev/null),docker,podman)
comma := ,

CI_TARGETS :=
GENERATE_TARGETS :=

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

CI_TARGETS += lint
.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint and check that go.mod and go.sum are tidy
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" $(GOLANGCI_LINT) run
	$(GO) mod tidy -diff

.PHONY: fmt
fmt: $(GOLANGCI_LINT) ## Format the Go code
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" $(GOLANGCI_LINT) fmt

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

GENERATE_TARGETS += sqlc-generate
.PHONY: sqlc-generate
sqlc-generate: $(SQLC) ## Generate internal/store/db from the queries and migrations
	$(SQLC) generate

# NOTICE lists the modules linked into the binary, so a dependency change
# that does not regenerate it fails verify-generated.
GENERATE_TARGETS += notice
.PHONY: notice
notice: ## Write NOTICE, the third-party notices that ship with the binary and image
	sh scripts/notice.sh > NOTICE.tmp
	mv NOTICE.tmp NOTICE

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

# The image is built with Docker when it is installed, else with Podman. With
# Podman the platforms go into one local manifest list named $(IMAGE); with
# Docker, buildx keeps the result in its cache unless IMAGE_PLATFORMS names
# one platform, which it loads into the local images.
.PHONY: image
image: ## Build the container image for linux/amd64 and linux/arm64 (IMAGE_PLATFORMS=linux/arm64 for one)
ifeq ($(CONTAINER_TOOL),podman)
	-podman manifest rm $(IMAGE) 2>/dev/null
	podman build --platform $(IMAGE_PLATFORMS) --manifest $(IMAGE) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .
else
	docker buildx build --platform $(IMAGE_PLATFORMS) -t $(IMAGE) \
		$(if $(findstring $(comma),$(IMAGE_PLATFORMS)),,--load) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .
endif

CI_TARGETS += helm-lint
.PHONY: helm-lint
helm-lint: $(HELM) ## Lint the Helm chart, values schema included, with each example values file
	$(HELM) lint --strict charts/sidekick -f charts/sidekick/quickstart-values.yaml
	for values in charts/sidekick/ci/*.yaml; do $(HELM) lint --strict charts/sidekick -f "$$values" || exit 1; done

CI_TARGETS += helm-test
.PHONY: helm-test
helm-test: $(HELM) ## Compare helm template of the quick-start values with its golden file, and validate each rendered sidekick.yaml
	sh scripts/helm-test.sh $(HELM)

.PHONY: helm-path
helm-path: $(HELM) ## Install the pinned Helm and print its path, for scripts
	@echo $(HELM)

.PHONY: helm-golden
helm-golden: $(HELM) ## Rewrite the golden file of the quick-start values after an intended chart change
	sh scripts/helm-test.sh $(HELM) --update

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK) ## Check dependencies and code for known vulnerabilities
	$(GOVULNCHECK) ./...

$(HELM):
	sh scripts/install-helm.sh $(HELM_VERSION) "$(HELM_SHA256_$(HOST_OS)_$(HOST_ARCH))" $(dir $@)

$(GOLANGCI_LINT):
	GOBIN="$(dir $@)" $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(MOCKGEN):
	GOBIN="$(dir $@)" $(GO) install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

$(GOVULNCHECK):
	GOBIN="$(dir $@)" $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(SQLC):
	GOBIN="$(dir $@)" $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

.PHONY: generate
generate: $(GENERATE_TARGETS) ## Run every code generator

.PHONY: ci
ci: $(CI_TARGETS) ## Run everything CI runs; run it before every push
