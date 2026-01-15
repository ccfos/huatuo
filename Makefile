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

GO_BUILD_STATIC := CGO_ENABLED=1 go build -tags "netgo osusergo" -gcflags=all="-N -l"
GO_BUILD_STATIC_WITH_VERSION := $(GO_BUILD_STATIC) -ldflags "-extldflags -static \
	-X main.AppVersion=$(APP_VERSION) \
	-X main.AppGitCommit=$(APP_COMMIT) \
	-X main.AppBuildTime=$(APP_BUILD_TIME)"

IMAGE_LATEST := huatuo/huatuo-bamai:latest

all: gen sync build

gen:
	@BPF_DIR=$(BPF_DIR) BPF_COMPILE=$(BPF_COMPILE) BPF_INCLUDE=$(BPF_INCLUDE) go generate -x ./...

sync:
	@mkdir -p $(APP_CMD_OUTPUT)/conf $(APP_CMD_OUTPUT)/bpf
	@cp $(BPF_DIR)/*.o $(APP_CMD_OUTPUT)/bpf/
	@cp *.conf $(APP_CMD_OUTPUT)/conf/

build: $(APP_CMD_BIN_TARGETS)
$(APP_CMD_OUTPUT)/bin/%: $(APP_CMD_DIR)/% force
	$(GO_BUILD_STATIC_WITH_VERSION) -o $@ ./$<

docker-build:
	@docker build --network=host --no-cache -t $(IMAGE_LATEST) -f Dockerfile .

docker-clean:
	@docker rmi $(IMAGE_LATEST) || true

GO_FILES := $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -path "./.git/*")
check: imports fmt golangci-lint

imports:
	@goimports -w -local huatuo-bamai  ${GO_FILES}

fmt:
	@gofumpt -l -w $(GO_FILES);
	@gofmt -w -r 'interface{} -> any' $(GO_FILES)

golangci-lint:
	@golangci-lint run -v ./... --timeout=5m --config .golangci.yaml

vendor:
	@go mod tidy; go mod verify; go mod vendor

clean:
	@rm -rf _output $(shell find . -type f -name "*.o")

integration:
	@bash integration/integration.sh

force:;

.PHONY: all gen sync build check imports fmt golangci-lint vendor clean integration force docker-build docker-clean
