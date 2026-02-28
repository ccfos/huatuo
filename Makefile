ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

BPF_DIR := $(ROOT_DIR)/bpf
BPF_COMPILE := $(ROOT_DIR)/build/clang.sh
BPF_INCLUDE := "-I$(BPF_DIR)/include"

APP_COMMIT ?= $(shell git describe --dirty --long --always)
APP_BUILD_TIME = $(shell date "+%Y%m%d%H%M%S")
APP_VERSION = "2.1.0"
APP_CMD_DIR := cmd
APP_CMD_OUTPUT := _output
APP_CMD_SUBDIRS := $(shell find $(APP_CMD_DIR) -mindepth 1 -maxdepth 1 -type d)
APP_CMD_BIN_TARGETS := $(patsubst %,$(APP_CMD_OUTPUT)/bin/%,$(notdir $(APP_CMD_SUBDIRS)))

GO_BUILD_FLAGS := CGO_ENABLED=1 go build -tags "netgo osusergo" -gcflags=all="-N -l"
GO_VERSION_LDFLAGS := \
	-X main.AppVersion=$(APP_VERSION) \
	-X main.AppGitCommit=$(APP_COMMIT) \
	-X main.AppBuildTime=$(APP_BUILD_TIME)

GO_BUILD_STATIC := $(GO_BUILD_FLAGS) -ldflags "-extldflags -static $(GO_VERSION_LDFLAGS)"
GO_BUILD_NOSTATIC := $(GO_BUILD_FLAGS) -ldflags "$(GO_VERSION_LDFLAGS)"

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

all: bpf-build sync build

build-nostatic:
	@$(MAKE) BUILD_MODE=nostatic all

bpf-build:
	@BPF_DIR=$(BPF_DIR) BPF_COMPILE=$(BPF_COMPILE) BPF_INCLUDE=$(BPF_INCLUDE) go generate -run "BPF_COMPILE" -x ./...

sync:
	@mkdir -p $(APP_CMD_OUTPUT)/conf $(APP_CMD_OUTPUT)/bpf
	@cp $(BPF_DIR)/*.o $(APP_CMD_OUTPUT)/bpf/
	@cp *.conf $(APP_CMD_OUTPUT)/conf/

build: $(APP_CMD_BIN_TARGETS)
$(APP_CMD_OUTPUT)/bin/%: $(APP_CMD_DIR)/% force
	$(GO_BUILD_IMPL) -o $@ ./$<

docker-build:
	@docker build --network=host --no-cache --build-arg BUILD_MODE=$(BUILD_MODE) -t $(IMAGE) -f Dockerfile .

docker-clean:
	@docker rmi $(IMAGE) || true

check: import-fmt golangci-lint
	@git diff --exit-code

import-fmt:
	$(eval GO_FILES := $(shell find . -name '*.go' ! \( -path "./vendor/*" -o -path "./.git/*" \)))
	@# goimports
	@goimports -w -local huatuo-bamai  ${GO_FILES}
	@# golang and shell fmt
	@gofumpt -l -w $(GO_FILES);
	@gofmt -w -r 'interface{} -> any' $(GO_FILES)
	@find . -name "*.sh" -not -path "./vendor/*" -exec shfmt -i 0 -w {} \;

golangci-lint: mock-build
	@golangci-lint run -v ./... --timeout=5m --config .golangci.yaml

vendor:
	@go mod tidy; go mod verify; go mod vendor

clean:
	@rm -rf _output $(shell find . -type f -name "*.o")

mock-build:
	@go generate -run "mockery.*" -x ./...

test: all mock-build
	@bash integration/run.sh
	@bash e2e/run.sh

integration: all mock-build
	@bash integration/run.sh

e2e: all
	@bash e2e/run.sh

force:;

.PHONY: all build-nostatic bpf-build mock-build sync build check import-fmt golangci-lint vendor clean integration force docker-build docker-clean
