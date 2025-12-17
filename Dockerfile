FROM golang:1.23.0-alpine AS base
RUN apk add --no-cache make clang15 libbpf-dev bpftool \
                       curl git binutils-gold musl-dev

ENV PATH=$PATH:/usr/lib/llvm15/bin

# build huatuo components
FROM base AS build
ARG BUILD_PATH=${BUILD_PATH:-/go/huatuo-bamai}
ARG RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
WORKDIR ${BUILD_PATH}
COPY . .
RUN make && mkdir -p ${RUN_PATH} && cp -rf ${BUILD_PATH}/_output/* ${RUN_PATH}/

# disable ES and kubelet fetching pods in huatuo-bamai.conf
RUN sed -i -e 's/# Address.*/Address=""/g' \
  -e '$a\    KubeletReadOnlyPort=0' \
  -e '$a\    KubeletAuthorizedPort=0' huatuo-bamai.conf

# final public image
FROM alpine:3.22.0 AS run
ARG RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
RUN apk add --no-cache curl
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}
CMD ["./bin/huatuo-bamai", "--region", "example", "--config", "huatuo-bamai.conf"]
