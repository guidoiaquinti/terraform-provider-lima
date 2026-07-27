BINARY      := terraform-provider-lima
VERSION     ?= 0.1.0-dev
GOFLAGS     ?=
LDFLAGS     := -ldflags "-s -w -X main.version=$(VERSION)"

# Local plugin directory used by `make install`. Change the namespace here and
# in main.go together if you fork this provider.
HOSTNAME    := registry.terraform.io
NAMESPACE   := guidoiaquinti
NAME        := lima
OS_ARCH     := $(shell go env GOOS)_$(shell go env GOARCH)
PLUGIN_DIR  := $(HOME)/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the provider binary
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY) .

.PHONY: install
install: build ## Install the provider into the local Terraform plugin directory
	mkdir -p "$(PLUGIN_DIR)"
	cp $(BINARY) "$(PLUGIN_DIR)/$(BINARY)_v$(VERSION)"
	@echo "Installed to $(PLUGIN_DIR)"
	@echo "For day-to-day development prefer a dev_overrides block; see README.md."

.PHONY: fmt
fmt: ## Format Go and Terraform sources
	gofmt -w -s .
	@command -v terraform >/dev/null 2>&1 && terraform fmt -recursive ./examples || \
		echo "terraform not found; skipping terraform fmt"

.PHONY: fmt-check
fmt-check: ## Fail if any file needs formatting
	@out="$$(gofmt -l -s .)"; \
	if [ -n "$$out" ]; then echo "these files need gofmt:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 || \
		{ echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

.PHONY: test
test: ## Run unit tests (no VM is created)
	go test ./... -timeout 5m

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test -race ./... -timeout 10m

.PHONY: cover
cover: ## Run unit tests and report coverage
	go test ./... -coverprofile=coverage.out -timeout 5m
	go tool cover -func=coverage.out | tail -1

.PHONY: testacc
testacc: ## Run acceptance tests (CREATES REAL VMs in an isolated LIMA_HOME)
	@echo "Acceptance tests create real Lima VMs in a temporary LIMA_HOME under /tmp."
	@echo "Your ~/.lima is not touched."
	TF_ACC=1 go test ./internal/provider/ -v -timeout 120m

.PHONY: generate
generate: ## Run go generate
	go generate ./...

.PHONY: docs-check
docs-check: ## Verify every resource and data source has a documentation page
	@# Documentation under docs/ is hand-written, not generated. It records
	@# why the provider behaves as it does, which tfplugindocs cannot derive
	@# from a schema. TestDocumentationCoverage enforces that a page exists
	@# for every registered type and that the schema descriptions are filled
	@# in; this target is the manual entry point to the same check.
	go test ./internal/provider/ -run 'TestDocumentation|TestMutability' -v

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: check
check: fmt-check vet test lint build docs-check ## Run every quality gate

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist
