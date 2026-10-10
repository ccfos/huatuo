ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

.DEFAULT_GOAL := build

BPF_DIR := $(ROOT_DIR)/bpf
BPF_COMPILE := $(ROOT_DIR)/build/clang.sh
BPF_INCLUDE := "-I$(BPF_DIR)/include"
BPF_SRCS := $(shell find $(BPF_DIR) -type f \( -name "*.c" -o -name "*.h" \))

# BPF_DEBUG=1 compiles bpf_dbg()/bpf_dbg_msg() into BPF objects (see
# bpf/include/bpf_dbg.h). Default off so the macros expand to no-ops,
# eliminating the debug perf event array, rodata constant, and per-call
# overhead at the source level (before the verifier ever runs).
# The flag is plumbed via BPF_EXTRA_CFLAGS, which build/clang.sh appends
# to its clang invocation, so per-file //go:generate directives are
# untouched.
BPF_DEBUG ?= 0
ifeq ($(BPF_DEBUG),1)
BPF_EXTRA_CFLAGS := -DDEBUG_BPF
else
BPF_EXTRA_CFLAGS :=
endif

APP_COMMIT ?= $(shell git describe --dirty --long --always)
APP_BUILD_TIME = $(shell date "+%Y%m%d%H%M%S")
APP_VERSION = "2.3.0"
APP_CMD_DIR := cmd
APP_CMD_OUTPUT := _output
APP_CMD_SUBDIRS := $(shell find $(APP_CMD_DIR) -mindepth 1 -maxdepth 1 -type d)
APP_CMD_BIN_TARGETS := $(patsubst %,$(APP_CMD_OUTPUT)/bin/%,$(notdir $(APP_CMD_SUBDIRS)))

IOCOST_TEST_BPF := $(APP_CMD_OUTPUT)/test-bpf/iocost_tracing_test.o
OETEST_OUTPUT := $(APP_CMD_OUTPUT)/oetest
OETEST_BIN_DIR := $(OETEST_OUTPUT)/bin
OETEST_DAEMON := $(OETEST_BIN_DIR)/huatuo-bamai
OETEST_RUNTIME_TEST := $(OETEST_BIN_DIR)/iocost-runtime.test
OETEST_PRODUCTION_BPF := bpf/iocost_tracing.o
OETEST_TEST_BPF := $(IOCOST_TEST_BPF)
OETEST_BUILD_HEAD := $(OETEST_OUTPUT)/.BUILD_HEAD
OETEST_BUILD_SHA256SUMS := $(OETEST_OUTPUT)/.BUILD_SHA256SUMS
OETEST_ARTIFACTS := \
	$(OETEST_DAEMON) \
	$(OETEST_RUNTIME_TEST) \
		$(OETEST_PRODUCTION_BPF) \
		$(OETEST_TEST_BPF)

GO_BUILD_FLAGS := CGO_ENABLED=1 go build -tags "netgo osusergo" -gcflags=all="-N -l"
GO_BUILD_LDFLAGS := \
	-s -w \
	-X main.AppVersion=$(APP_VERSION) \
	-X main.AppGitCommit=$(APP_COMMIT) \
	-X main.AppBuildTime=$(APP_BUILD_TIME)

GO_BUILD_STATIC := $(GO_BUILD_FLAGS) -ldflags "-extldflags -static $(GO_BUILD_LDFLAGS)"
GO_BUILD_NOSTATIC := $(GO_BUILD_FLAGS) -ldflags "$(GO_BUILD_LDFLAGS)"
FIND_EXCLUDE_PATHS := \
	! -path "./vendor/*" \
	! -path "./.git/*" \
	! -path "./.claude/*" \
	! -path "./third_party/*"

GO_SRCS := $(shell find . -name "*.go" \
	! -name "*_test.go" \
	$(FIND_EXCLUDE_PATHS)) \
	go.mod go.sum
GO_FORMAT_FILES := $(shell find . -name '*.go' \
	! -name '*.capnp.go' \
	! -name '*.gen.go' \
	! -name 'mock_*_test.go' \
	$(FIND_EXCLUDE_PATHS))

BUILD_MODE ?= static

IMAGE_TAG := latest

ifeq ($(BUILD_MODE),static)
GO_BUILD_IMPL := $(GO_BUILD_STATIC)
IMAGE_REPO := huatuo/huatuo-bamai-static
else ifeq ($(BUILD_MODE),nostatic)
GO_BUILD_IMPL := $(GO_BUILD_NOSTATIC)
IMAGE_REPO := huatuo/huatuo-bamai
else
$(error unsupported BUILD_MODE=$(BUILD_MODE); use static or nostatic)
endif

IMAGE := $(IMAGE_REPO):$(IMAGE_TAG)

COMPOSE_DEV := docker compose \
	--project-directory $(ROOT_DIR)/build/docker \
	-f $(ROOT_DIR)/build/docker/docker-compose.yml \
	-f $(ROOT_DIR)/build/docker/docker-compose.dev.yml

BPF_BUILD_STAMP := $(APP_CMD_OUTPUT)/.bpf-build-stamp-$(BPF_DEBUG)

OPENAPI_COMMON_SPEC := apis/v1/components.yaml
OPENAPI_SERVER_SPEC := apis/v1/server/openapi.yaml
OPENAPI_NODE_SPEC := apis/v1/node/openapi.yaml
OPENAPI_CODEGEN := go tool oapi-codegen
OPENAPI_CODEGEN_JOBS := \
	apis/v1/types.cfg.yaml:apis/v1/types.gen.go:$(OPENAPI_COMMON_SPEC) \
	apis/v1/server/types.cfg.yaml:apis/v1/server/types.gen.go:$(OPENAPI_SERVER_SPEC) \
	apis/v1/server/client.cfg.yaml:apis/v1/server/client.gen.go:$(OPENAPI_SERVER_SPEC) \
	apis/v1/server/server.cfg.yaml:apis/v1/server/server.gen.go:$(OPENAPI_SERVER_SPEC) \
	apis/v1/node/types.cfg.yaml:apis/v1/node/types.gen.go:$(OPENAPI_NODE_SPEC) \
	apis/v1/node/client.cfg.yaml:apis/v1/node/client.gen.go:$(OPENAPI_NODE_SPEC) \
	apis/v1/node/server.cfg.yaml:apis/v1/node/server.gen.go:$(OPENAPI_NODE_SPEC)
OPENAPI_CODEGEN_FILES := $(foreach job,$(OPENAPI_CODEGEN_JOBS),$(word 2,$(subst :, ,$(job))))
OPENAPI_DERIVED_FILES := \
	apis/v1/error_codes.gen.go \
	apis/v1/http_status.gen.go \
	apis/v1/server/error_codes.gen.go \
	apis/v1/server/http_status.gen.go \
	apis/v1/server/openapi.gen.json \
	apis/v1/node/error_codes.gen.go \
	apis/v1/node/http_status.gen.go \
	apis/v1/node/openapi.gen.json
OPENAPI_GENERATED_FILES := $(OPENAPI_CODEGEN_FILES) $(OPENAPI_DERIVED_FILES)

define generate-openapi
	output_root="$(1)"; \
	for job in $(OPENAPI_CODEGEN_JOBS); do \
		config=$${job%%:*}; \
		remainder=$${job#*:}; \
		output=$${remainder%%:*}; \
		spec=$${remainder#*:}; \
		mkdir -p "$$output_root/$$(dirname "$$output")"; \
		$(OPENAPI_CODEGEN) -config "$$config" \
			-o "$$output_root/$$output" "$$spec"; \
	done; \
	go run ./build/openapi/errorcodes -spec-root . -output-root "$$output_root"; \
	go run ./build/openapi/bundle -spec-root . -output-root "$$output_root"
endef

define generate-non-openapi
	go run ./build/bpfabi-tool; \
	go generate -run "mockery.*" -x ./...; \
	go generate -run "capnp.*" ./...
endef

define check-openapi
	set -eu; \
	api_tmp=$$(mktemp -d); \
	trap 'rm -rf "$$api_tmp"' EXIT; \
	$(call generate-openapi,$$api_tmp); \
	for api_file in $(OPENAPI_GENERATED_FILES); do \
		if ! diff -u "$$api_file" "$$api_tmp/$$api_file"; then \
			echo "generated file is stale: $$api_file; run 'make gen-build'" >&2; \
			exit 1; \
		fi; \
	done
endef

define format-sources
	goimports -w -local github.com/ccfos/huatuo $(GO_FORMAT_FILES); \
	gofumpt -l -w $(GO_FORMAT_FILES); \
	gofmt -w -r 'interface{} -> any' $(GO_FORMAT_FILES); \
	find . -name "*.sh" $(FIND_EXCLUDE_PATHS) \
		-exec shfmt -i 0 -bn -sr -w {} \;
endef


$(BPF_BUILD_STAMP): $(BPF_SRCS) $(BPF_COMPILE) # parallel
	@mkdir -p $(APP_CMD_OUTPUT)
	@rm -f $(APP_CMD_OUTPUT)/.bpf-build-stamp*
	@find . -name "*.go" \
		$(FIND_EXCLUDE_PATHS) \
		-exec grep -l "^[[:space:]]*//go:generate.*BPF_COMPILE" {} \; | \
		xargs -n1 dirname | sort -u | \
			xargs -P $(shell nproc) -I {} sh -c ' \
			export BPF_DIR=$(BPF_DIR); \
			export BPF_COMPILE=$(BPF_COMPILE); \
			export BPF_INCLUDE=$(BPF_INCLUDE); \
			export BPF_EXTRA_CFLAGS="$(BPF_EXTRA_CFLAGS)"; \
			go generate -run BPF_COMPILE {}'
	@touch $@

iocost-test-build:
	@mkdir -p $(ROOT_DIR)/$(dir $(IOCOST_TEST_BPF))
	BPF_EXTRA_CFLAGS="$(BPF_EXTRA_CFLAGS)" $(BPF_COMPILE) $(BPF_INCLUDE) \
		-s $(BPF_DIR)/iocost_tracing_test.c \
		-o $(ROOT_DIR)/$(IOCOST_TEST_BPF)

build: $(APP_CMD_BIN_TARGETS)
	@mkdir -p $(APP_CMD_OUTPUT)/conf $(APP_CMD_OUTPUT)/bpf
	@cp $(BPF_DIR)/*.o $(APP_CMD_OUTPUT)/bpf/
	@cp *.conf $(APP_CMD_OUTPUT)/conf/

install-tools:
	@build/install-tools.sh

$(APP_CMD_BIN_TARGETS): gen-build $(GO_SRCS)
$(APP_CMD_OUTPUT)/bin/%:
	@mkdir -p $(APP_CMD_OUTPUT)/bin
	$(GO_BUILD_IMPL) -o $@ ./$(APP_CMD_DIR)/$*

# Reject source changes which would make the artifact-to-HEAD record false.
oetest-source-clean:
	@set -eu; \
	git diff --quiet -- || { echo "tracked source tree is dirty" >&2; exit 1; }; \
	git diff --cached --quiet -- || { echo "staged source tree is dirty" >&2; exit 1; }; \
	untracked="$$(git ls-files --others --exclude-standard)"; \
	test -z "$$untracked" || { \
		echo "untracked OETest build inputs:" >&2; \
		echo "$$untracked" >&2; \
		exit 1; \
	}

# Build transportable native IOCOST qualification artifacts.
# Compile the production object explicitly instead of trusting the global BPF
# stamp, then record the exact artifacts produced by this invocation.
oetest-build:
	@$(MAKE) --no-print-directory oetest-source-clean
	@$(MAKE) --no-print-directory gen-build
	$(BPF_COMPILE) $(BPF_INCLUDE) \
		-s $(BPF_DIR)/iocost_tracing.c \
		-o $(BPF_DIR)/iocost_tracing.o
	@$(MAKE) --no-print-directory iocost-test-build
	@mkdir -p $(OETEST_BIN_DIR)
	CGO_ENABLED=1 go build -tags "netgo osusergo" \
		-gcflags=all="-N -l" \
		-ldflags "$(GO_BUILD_LDFLAGS)" \
		-o $(OETEST_DAEMON) ./cmd/huatuo-bamai
	CGO_ENABLED=1 go test -c -tags "netgo osusergo" \
		-o $(OETEST_RUNTIME_TEST) ./core/metrics
	@git rev-parse HEAD > $(OETEST_BUILD_HEAD)
	@sha256sum $(OETEST_ARTIFACTS) > $(OETEST_BUILD_SHA256SUMS)

# Bind the transported artifacts to an exact, clean source tree. Ignored build
# outputs are allowed; tracked changes and untracked build inputs are not.
oetest-manifest:
	@set -eu; \
	$(MAKE) --no-print-directory oetest-source-clean; \
	for artifact in $(OETEST_ARTIFACTS); do \
		test -f "$$artifact" || { echo "missing OETest artifact: $$artifact" >&2; exit 1; }; \
	done; \
	test -f $(OETEST_BUILD_HEAD) || { echo "missing OETest build HEAD" >&2; exit 1; }; \
	test -f $(OETEST_BUILD_SHA256SUMS) || { echo "missing OETest build checksums" >&2; exit 1; }; \
	current_head="$$(git rev-parse HEAD)"; \
	test "$$(cat $(OETEST_BUILD_HEAD))" = "$$current_head" || { \
		echo "OETest artifacts were built from another HEAD" >&2; exit 1; \
	}; \
	sha256sum -c $(OETEST_BUILD_SHA256SUMS); \
	mkdir -p $(OETEST_OUTPUT); \
	cp $(OETEST_BUILD_HEAD) $(OETEST_OUTPUT)/HEAD; \
	cp $(OETEST_BUILD_SHA256SUMS) $(OETEST_OUTPUT)/SHA256SUMS

docker-build:
	@docker build --network=host --no-cache --build-arg BUILD_MODE=$(BUILD_MODE) -t $(IMAGE) -f Dockerfile .

docker-clean:
	@docker rmi $(IMAGE) || true

compose-dev-up:
	@$(COMPOSE_DEV) build huatuo-apiserver
	@$(COMPOSE_DEV) up

compose-dev-down:
	@$(COMPOSE_DEV) down --remove-orphans --volumes
	@docker image rm huatuo/huatuo-bamai:dev || true

check: vendor $(BPF_BUILD_STAMP)
	@$(check-openapi)
	@set -eu; $(generate-non-openapi)
	@set -eu; $(format-sources)
	@golangci-lint run -v ./... --timeout=5m --config .golangci.yaml
	@git diff --exit-code

vendor:
	@set -eu; go mod tidy; go mod verify; go mod vendor

clean:
	# Keep tracked OpenAPI artifacts: Go builds embed the specs and check compares them.
	@rm -rf $(APP_CMD_OUTPUT)
	@find . \( -name "*.o" -o -name "mock_*.go" -o -name "*.capnp.go" \) \
		$(FIND_EXCLUDE_PATHS) -delete

gen-build: $(BPF_BUILD_STAMP)
	@set -eu; $(call generate-openapi,.)
	@set -eu; $(generate-non-openapi)

test: unit integration e2e

unit: gen-build iocost-test-build
	@go test -v ./... -coverprofile=$(APP_CMD_OUTPUT)/unit-coverage.txt -timeout=5m
	@go tool cover -html=$(APP_CMD_OUTPUT)/unit-coverage.txt -o $(APP_CMD_OUTPUT)/unit-coverage.html

integration: build
	@bash integration/run.sh

e2e: build
	@bash e2e/run.sh

.PHONY: build install-tools gen-build iocost-test-build oetest-source-clean oetest-build oetest-manifest check vendor clean test unit integration e2e docker-build docker-clean compose-dev-up compose-dev-down
