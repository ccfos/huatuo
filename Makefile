ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

BPF_DIR := $(ROOT_DIR)/bpf
BPF_COMPILE := $(ROOT_DIR)/build/clang.sh
BPF_INCLUDE := "-I$(BPF_DIR)/include"
BPF_SRCS := $(shell find $(BPF_DIR) -type f \( -name "*.c" -o -name "*.h" \))

APP_COMMIT ?= $(shell git describe --dirty --long --always)
APP_BUILD_TIME = $(shell date "+%Y%m%d%H%M%S")
APP_VERSION = "2.2.0"
APP_CMD_DIR := cmd
APP_CMD_OUTPUT := _output
APP_CMD_SUBDIRS := $(shell find $(APP_CMD_DIR) -mindepth 1 -maxdepth 1 -type d)
APP_CMD_BIN_TARGETS := $(patsubst %,$(APP_CMD_OUTPUT)/bin/%,$(notdir $(APP_CMD_SUBDIRS)))

GO ?= go
GO_BUILD_FLAGS := CGO_ENABLED=1 $(GO) build -tags "netgo osusergo" -gcflags=all="-N -l"
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

BUILD_MODE ?= static

IMAGE_TAG := latest

ifeq ($(BUILD_MODE),nostatic)
GO_BUILD_IMPL := $(GO_BUILD_NOSTATIC)
IMAGE_REPO := huatuo/huatuo-bamai
else
GO_BUILD_IMPL := $(GO_BUILD_STATIC)
IMAGE_REPO := huatuo/huatuo-bamai-static
endif

IMAGE := $(IMAGE_REPO):$(IMAGE_TAG)

BPF_BUILD_STAMP := $(APP_CMD_OUTPUT)/.bpf-build-stamp

all: bpf-build build sync

build-nostatic:
	@$(MAKE) BUILD_MODE=nostatic all

bpf-build: $(BPF_BUILD_STAMP)
$(BPF_BUILD_STAMP): $(BPF_SRCS) $(BPF_COMPILE) # parallel
	@find . -name "*.go" \
		$(FIND_EXCLUDE_PATHS) \
		-exec grep -l "^[[:space:]]*//go:generate.*BPF_COMPILE" {} \; | \
		xargs -n1 dirname | sort -u | \
		xargs -P $(shell nproc) -I {} sh -c ' \
			export BPF_DIR=$(BPF_DIR); \
			export BPF_COMPILE=$(BPF_COMPILE); \
			export BPF_INCLUDE=$(BPF_INCLUDE); \
			$(GO) generate {}'
	@mkdir -p $(APP_CMD_OUTPUT) && touch $@

sync:
	@mkdir -p $(APP_CMD_OUTPUT)/conf $(APP_CMD_OUTPUT)/bpf
	@cp $(BPF_DIR)/*.o $(APP_CMD_OUTPUT)/bpf/
	@cp *.conf $(APP_CMD_OUTPUT)/conf/

build: gen-build $(APP_CMD_BIN_TARGETS)
$(APP_CMD_BIN_TARGETS): $(GO_SRCS)
$(APP_CMD_OUTPUT)/bin/%:
	@mkdir -p $(APP_CMD_OUTPUT)/bin
	$(GO_BUILD_IMPL) -o $@ ./$(APP_CMD_DIR)/$*

PLATFORMS ?= linux/amd64,linux/arm64

docker-build:
	@docker build \
		--network=host \
		--no-cache \
		--build-arg BUILD_MODE=$(BUILD_MODE) \
		-t $(IMAGE) \
		-f Dockerfile .

docker-buildx:
	@docker buildx build \
		--platform $(PLATFORMS) \
		--network=host \
		--no-cache \
		--build-arg BUILD_MODE=$(BUILD_MODE) \
		-t $(IMAGE) \
		-f Dockerfile \
		--push .

docker-buildx-check:
	@docker buildx build \
		--platform $(PLATFORMS) \
		--network=host \
		--no-cache \
		--build-arg BUILD_MODE=$(BUILD_MODE) \
		-f Dockerfile \
		--output type=cacheonly .

docker-clean:
	@docker rmi $(IMAGE) || true

check: import-fmt golangci-lint
	@git diff --exit-code

import-fmt:
	$(eval GO_FILES := $(shell find . -name '*.go' \
		! -name '*.capnp.go' \
		! -name 'mock_*_test.go' \
		$(FIND_EXCLUDE_PATHS)))
	@goimports -w -local huatuo-bamai $(GO_FILES)
	@# golang and shell fmt
	@gofumpt -l -w $(GO_FILES);
	@gofmt -w -r 'interface{} -> any' $(GO_FILES)
	@find . -name "*.sh" \
		$(FIND_EXCLUDE_PATHS) \
		-exec shfmt -i 0 -bn -sr -w {} \;

golangci-lint: gen-build
	@# gen-build ensures mock/capnp files exist for typecheck to resolve imports.
	@golangci-lint run -v ./... --timeout=5m --config .golangci.yaml

vendor:
	@go mod tidy; go mod verify; go mod vendor

clean:
	@rm -rf _output
	@find . \( -name "*.o" -o -name "mock_*.go" -o -name "*.capnp.go" \) \
		$(FIND_EXCLUDE_PATHS) \
		-delete

gen-build:
	@go generate -run "mockery.*" -x ./...
	@go generate -run "capnp.*" ./...

test: unit integration e2e

unit: bpf-build gen-build
	@go test -v ./... -coverprofile=$(APP_CMD_OUTPUT)/unit-coverage.txt -timeout=5m
	@go tool cover -html=$(APP_CMD_OUTPUT)/unit-coverage.txt -o $(APP_CMD_OUTPUT)/unit-coverage.html

integration: all
	@bash integration/run.sh

e2e: all
	@bash e2e/run.sh

.PHONY: all build-nostatic bpf-build gen-build sync build check import-fmt golangci-lint vendor clean test unit integration e2e docker-build docker-buildx docker-buildx-check docker-clean
