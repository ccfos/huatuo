# basic image
FROM golang:1.23.0-alpine AS base
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
RUN apk add --no-cache make clang15 libbpf-dev git
ENV GOPATH="/go"
ENV GOROOT="/usr/local/go"
ENV GOPROXY="https://goproxy.cn,https://goproxy.io,direct"
ENV PATH="${GOPATH}/bin:${GOROOT}/bin:/usr/lib/llvm15/bin:${PATH}"

# build huatuo
FROM base AS build
ARG BUILD_PATH="/go/huatuo-bamai"
ARG RUN_PATH="/home/huatuo-bamai"
WORKDIR ${BUILD_PATH}
COPY . .
RUN make && mkdir -p ${RUN_PATH} && cp -rf ${BUILD_PATH}/_output/* ${RUN_PATH}/
# disable es and kubelet fetching pods in huatuo-bamai.conf
RUN sed -i -e 's/# Address.*/Address=""/g' \
  -e '$a\    KubeletReadOnlyPort=0' \
  -e '$a\    KubeletAuthorizedPort=0' ${RUN_PATH}/conf/huatuo-bamai.conf

# release huatuo
FROM alpine:3.22.0 AS run
ARG RUN_PATH="/home/huatuo-bamai"
RUN apk add --no-cache curl
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}
CMD ["./bin/huatuo-bamai", "--region", "example", "--config", "huatuo-bamai.conf"]

# for compile, lint, vendor
FROM base AS devel
RUN apk add --no-cache musl-dev binutils-gold # for golangci-lint
# For golang 1.23.0
# gofumpt@v0.8.0, https://github.com/mvdan/gofumpt/blob/master/CHANGELOG.md
# goimports@v0.36.0
# golangci-lint https://github.com/golangci/golangci-lint/blob/main/CHANGELOG-v1.md
RUN go install mvdan.cc/gofumpt@v0.8.0 && \
    go install golang.org/x/tools/cmd/goimports@v0.36.0 && \
    go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2

FROM devel AS lint-vendor-check
