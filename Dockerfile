ARG BUILD_MODE

# Build docker images
#
# Install development environment
# Disable the elasticsearch and kubelet fetching pods.
#
FROM golang:1.24 AS build
ARG BUILD_PATH="/go/huatuo-bamai"
ARG RUN_PATH="/home/huatuo-bamai"
ARG BUILD_MODE
WORKDIR ${BUILD_PATH}
ENV PATH=$PATH:/usr/lib/llvm15/bin

# If the network is slow, uncomment the mirror lines below
# RUN go env -w GOPROXY=https://goproxy.cn,https://goproxy.io,direct

COPY . .
# RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
#     sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list 2>/dev/null || true
RUN set -x; \
    apt-get update && apt-get install -y --no-install-recommends \
    make clang libbpf-dev bpftool curl git binutils-gold musl-tools &&\
    make BUILD_MODE=${BUILD_MODE} &&\
    mkdir -p ${RUN_PATH} &&\
    cp -rf ${BUILD_PATH}/_output/* ${RUN_PATH}/ &&\
    sed -i -e 's/# Address.*/Address=""/g' \
    -e '$a\    KubeletReadOnlyPort=0' \
    -e '$a\    KubeletAuthorizedPort=0' ${RUN_PATH}/conf/huatuo-bamai.conf

# Release static docker image
#
# a minimal Docker image based on Alpine Linux
#
FROM alpine:3.22.0 AS run-static
ARG RUN_PATH="/home/huatuo-bamai"
# If the network is slow, uncomment the mirror lines below
# RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories
RUN apk add --no-cache curl
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}

# Release nostatic docker image
#
# golang:1.24 based on debian
# https://hub.docker.com/layers/library/golang/1.24/images/sha256-9138c01eea9effb74a8fe9ae32329d7e37b56c35ea4e1ce5b0fc913de4bb84f3
#
FROM golang:1.24 AS run-nostatic
ARG RUN_PATH="/home/huatuo-bamai"
# If the network is slow, uncomment the mirror lines below
# RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
#     sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list 2>/dev/null || true
RUN apt-get update && apt-get install -y --no-install-recommends curl libelf1 libnuma1 &&\
    rm -rf /var/lib/apt/lists/*
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}

FROM run-${BUILD_MODE:-static}
CMD ["./bin/huatuo-bamai", "--region", "example", "--config", "huatuo-bamai.conf"]
