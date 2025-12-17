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

GO_BUILD_STATIC := CGO_ENABLED=1 go build -tags "netgo osusergo" -gcflags=all="-N -l" \
	-ldflags "-extldflags -static
GO_BUILD_STATIC_WITH_VERSION := $(GO_BUILD_STATIC) \
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
$(APP_CMD_OUTPUT)/bin/%: $(APP_CMD_DIR)/% CMD_FORCE
	$(GO_BUILD_STATIC_WITH_VERSION) -o $@ ./$<

CMD_FORCE:;

docker-build:
	@docker build --network=host --no-cache -t $(IMAGE_LATEST) -f Dockerfile .

docker-clean:
	@docker rmi $(IMAGE_LATEST) || true

check: imports fmt golangci-lint

imports:
	@goimports -w -local huatuo-bamai  $(shell find . -type f -name '*.go' -not -path "./vendor/*")

fmt: fmt-rewrite-rules
	@gofumpt -l -w $(shell find . -type f -name '*.go' -not -path "./vendor/*")

fmt-rewrite-rules:
	@gofmt -w -r 'interface{} -> any' $(shell find . -type f -name '*.go' -not -path "./vendor/*")

golangci-lint:
	@golangci-lint run -v ./... --timeout=5m --config .golangci.yaml

vendor:
	@go mod tidy; go mod verify; go mod vendor

clean:
	rm -rf _output $(shell find . -type f -name "*.o")

.PHONY: all gen sync build check imports golint fmt golangci-lint vendor clean CMD_FORCE docker-build docker-clean
