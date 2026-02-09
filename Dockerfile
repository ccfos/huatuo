# Base docker image
FROM golang:1.24-alpine3.22 AS base
#
# To accelerate the build process, you may uncomment this section.
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
#
RUN apk add --no-cache build-base make clang15 libbpf-dev curl git
ENV PATH=$PATH:/usr/lib/llvm15/bin

# Build release version
FROM base AS build
ARG BUILD_MODE=static
ARG BUILD_PATH="/go/huatuo-bamai"
ARG RUN_PATH="/home/huatuo-bamai"
WORKDIR ${BUILD_PATH}
COPY . .
RUN make BUILD_MODE=${BUILD_MODE} && mkdir -p ${RUN_PATH} && cp -rf ${BUILD_PATH}/_output/* ${RUN_PATH}/
# Disable the elasticsearch and kubelet fetching pods.
RUN sed -i -e 's/# Address.*/Address=""/g' \
  -e '$a\    KubeletReadOnlyPort=0' \
  -e '$a\    KubeletAuthorizedPort=0' ${RUN_PATH}/conf/huatuo-bamai.conf

# Release docker image
FROM alpine:3.22.0 AS run
ARG RUN_PATH="/home/huatuo-bamai"
RUN apk add --no-cache curl
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}
CMD ["./bin/huatuo-bamai", "--region", "example", "--config", "huatuo-bamai.conf"]
