FROM golang:1.23 AS base
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        make clang libbpf-dev bpftool curl git binutils-gold musl-tools

ENV PATH=$PATH:/usr/lib/llvm15/bin

# build huatuo components
FROM base AS build
ARG BUILD_PATH=${BUILD_PATH:-/go/huatuo-bamai}
ARG RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
WORKDIR ${BUILD_PATH}
COPY . .
RUN make && mkdir -p ${RUN_PATH} && cp -rf ${BUILD_PATH}/_output/* ${RUN_PATH}/

# disable ES in huatuo-bamai.conf
RUN sed -i 's/"http:\/\/127.0.0.1:9200"/""/' ${RUN_PATH}/conf/huatuo-bamai.conf

# final public image
FROM debian:12-slim AS run
ARG RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl libelf1 libnuma1 && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build ${RUN_PATH} ${RUN_PATH}
WORKDIR ${RUN_PATH}
CMD ["./bin/huatuo-bamai", "--region", "example", "--config", "huatuo-bamai.conf"]
