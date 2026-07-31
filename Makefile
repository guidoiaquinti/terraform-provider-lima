BINARY      := terraform-provider-lima
VERSION     ?= 0.0.1-dev
GOFLAGS     ?=
LDFLAGS     := -ldflags "-s -w -X main.version=$(VERSION)"
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
	@# Verify the configuration before using it. `run` tolerates settings that
	@# do not match golangci-lint's own schema — the goconst exclusions were a
	@# string where an array is required, and worked anyway — so a config that a
	@# future release will reject outright otherwise goes unnoticed.
	golangci-lint config verify
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
	@# -count=1 defeats the build cache: Go can serve a cached pass for a suite
	@# whose whole value is that it touched real hardware.
	TF_ACC=1 go test ./internal/provider/ -count=1 -v -timeout 120m

# RUN is a `go test -run` pattern. Defaults to the whole acceptance suite so a
# bare `make testacc-run` is not silently a no-op.
RUN ?= TestAcc

.PHONY: testacc-run
testacc-run: ## Run acceptance tests matching RUN=<pattern> (CREATES REAL VMs)
	@# The targeted entry point. It matters most on macOS, where the `vz` driver
	@# has no CI coverage — no free GitHub runner can boot a VM on macOS — so the
	@# suite is run by hand before a release and iterating on one test beats
	@# waiting out all of them.
	@echo "Running acceptance tests matching '$(RUN)'."
	TF_ACC=1 go test ./internal/provider/ -run '$(RUN)' -count=1 -v -timeout 120m

# SWEEP_HOME is the LIMA_HOME to clean. It defaults to the same value CI uses,
# which is also the one `make testacc` picks when LIMA_PROVIDER_ACC_HOME is set.
# There is deliberately no default of "" — an empty home means Lima's own
# ~/.lima, and the sweep refuses it.
SWEEP_HOME ?= $(if $(LIMA_PROVIDER_ACC_HOME),$(LIMA_PROVIDER_ACC_HOME),/tmp/ltfci)

.PHONY: sweep
sweep: ## Remove leftover VMs and disks from an interrupted acceptance run
	@# Recovery path for a killed `make testacc`: Ctrl-C skips every t.Cleanup,
	@# so real VMs and their disks survive. Instances go before disks because a
	@# running instance holds a lock on anything attached to it.
	@#
	@# -v is required, not cosmetic: without it `go test` buffers the binary's
	@# output and prints it only on failure, so a successful sweep would report
	@# nothing about what it removed.
	@echo "Sweeping LIMA_HOME $(SWEEP_HOME) (override with SWEEP_HOME=...)"
	go test ./internal/provider/ -v -sweep="$(SWEEP_HOME)" -timeout 15m

.PHONY: sweep-tmp
sweep-tmp: ## Sweep every leftover acceptance LIMA_HOME under /tmp
	@# `make testacc` with no LIMA_PROVIDER_ACC_HOME creates /tmp/ltfaccXXXXXX
	@# per run, so an interrupted run leaves a directory whose name nobody
	@# recorded. This finds them.
	@set -e; \
	found=0; \
	for dir in /tmp/ltfacc* /tmp/ltfci; do \
		[ -d "$$dir" ] || continue; \
		found=1; \
		echo "==> $$dir"; \
		go test ./internal/provider/ -v -sweep="$$dir" -timeout 15m || true; \
		rm -rf "$$dir"; \
	done; \
	if [ "$$found" = 0 ]; then echo "no acceptance LIMA_HOME found under /tmp"; fi

.PHONY: generate
generate: ## Run go generate
	go generate ./...

.PHONY: docs
docs: ## Regenerate docs/ from templates/ and the schema
	@# docs/ is generated; templates/ is the source.
	@#
	@# The split matters: the attribute reference comes from the schema, so a
	@# description can no longer drift from the code, while the narrative
	@# sections — why an attribute replaces, what a timeout means, how import
	@# adopts an instance — stay hand-written in templates/, because no
	@# generator derives reasoning from a schema.
	@#
	@# tfplugindocs is pinned as a tool dependency in go.mod, so this runs the
	@# same version everywhere and its checksum is in go.sum.
	go tool tfplugindocs generate --provider-name lima

.PHONY: docs-check
docs-check: ## Fail if docs/ is not what templates/ and the schema produce
	@# The real anti-drift gate: a schema description change nobody regenerated,
	@# or a hand edit to docs/, stops the build instead of shipping.
	@#
	@# Deliberately compares docs/ against a fresh regeneration rather than
	@# against git. A `git diff` gate conflates two different things — "docs are
	@# stale" and "docs are correctly regenerated but not yet committed" — so it
	@# fails on a developer's working tree in the middle of exactly the change
	@# that fixes it. Snapshot-and-compare answers only the question asked, in
	@# any git state, including a dirty tree or a detached HEAD in CI.
	@set -e; \
	before="$$(mktemp -d)"; \
	trap 'rm -rf "$$before"' EXIT; \
	cp -R docs/. "$$before/"; \
	$(MAKE) --no-print-directory docs >/dev/null; \
	if ! diff -r -u "$$before" docs >/tmp/docs-drift.diff 2>&1; then \
		echo "docs/ is not what templates/ and the schema produce:"; \
		echo; \
		cat /tmp/docs-drift.diff; \
		echo; \
		echo "Run 'make docs' and commit the result. Do not edit docs/ by hand;"; \
		echo "edit the matching file under templates/."; \
		exit 1; \
	fi; \
	echo "docs/ matches templates/ and the schema"
	@# These cover what generation cannot: the mutability table matching the plan
	@# modifiers, the documented timeout defaults matching the constants, and
	@# every type having a template at all.
	go test ./internal/provider/ -run 'TestDocument|TestMutability|TestEveryType|TestEveryInstanceAttribute|TestTimeouts' -v

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: check
check: fmt-check vet test lint build docs-check notices-check ## Run every quality gate

.PHONY: tools
tools: ## Show the pinned versions of the tools this repo drives
	@echo "tfplugindocs: $$(go tool tfplugindocs --version 2>/dev/null || echo 'run: go mod download')"
	@echo "golangci-lint: $$(golangci-lint version 2>/dev/null || echo 'not installed')"

.PHONY: notices
notices: ## Regenerate THIRD-PARTY-NOTICES.md from the module graph
	@# Apache-2.0 §4, the BSD licences and MIT all require reproducing copyright
	@# notices in binary redistributions. A Go provider is one statically linked
	@# binary containing all of them, so shipping only our own LICENSE satisfies
	@# our licence and none of theirs.
	./scripts/gen-notices.sh

.PHONY: notices-check
notices-check: ## Fail if THIRD-PARTY-NOTICES.md is stale
	@# Same snapshot-and-compare shape as docs-check, and for the same reason:
	@# answers "is this file what the module graph produces" in any git state.
	@set -e; \
	before="$$(mktemp)"; \
	trap 'rm -f "$$before"' EXIT; \
	cp THIRD-PARTY-NOTICES.md "$$before"; \
	./scripts/gen-notices.sh >/dev/null; \
	if ! diff -u "$$before" THIRD-PARTY-NOTICES.md; then \
		echo; \
		echo "THIRD-PARTY-NOTICES.md is stale. Run 'make notices' and commit the result."; \
		exit 1; \
	fi; \
	echo "THIRD-PARTY-NOTICES.md matches the module graph"

.PHONY: release-check
release-check: ## Validate .goreleaser.yml and build every shipped target
	@# The same rehearsal CI runs. Worth having locally too, because the failure
	@# it prevents is only otherwise discoverable by tagging: a release builds
	@# platforms that ordinary development never compiles.
	@command -v goreleaser >/dev/null 2>&1 || \
		{ echo "goreleaser not installed: https://goreleaser.com/install/"; exit 1; }
	goreleaser check
	@# --snapshot skips publishing and signing, so this needs no GPG key and
	@# cannot release anything.
	goreleaser build --snapshot --clean
	@echo
	@echo "Built:"
	@find dist -type f -name 'terraform-provider-lima_v*' | sort

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist
