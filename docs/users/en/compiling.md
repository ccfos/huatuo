---
title: Compile
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

### 1. Build with the Official Image

To isolate the developer’s local environment and simplify the build process, we provide a containerized build method. You can directly use `docker build` to produce an image containing the core collector **huatuo-bamai**, BPF objects, tools, and more. Run the following in the project root directory:

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

### 2. Build a Custom Image

`Dockerfile.dev`:

```Dockerfile
FROM golang:1.23.0-alpine AS base
# Speed up Alpine package installation if needed
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
RUN apk add --no-cache \
                make \
                clang15 \
                libbpf-dev \
                bpftool \
                curl \
                git

ENV PATH=$PATH:/usr/lib/llvm15/bin

# build huatuo components
FROM base AS build
ARG BUILD_PATH=${BUILD_PATH:-/go/huatuo-bamai}
ARG RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}
WORKDIR ${BUILD_PATH}
```

#### 2.1 Build the Dev Image

```bash
docker build --network host -t huatuo/huatuo-bamai-dev:latest -f ./Dockerfile.dev .
```

#### 2.2 Run the Dev Container

```bash
docker run -it --privileged --cgroupns=host --network=host \
  -v /path/to/huatuo:/go/huatuo-bamai \
  huatuo/huatuo-bamai-dev:latest sh
```

#### 2.3 Compile Inside the Container

Run:

```bash
make
```

Once the build completes, all artifacts are generated under `./_output`.

### 3. Build on a Physical Machine or VM

The collector depends on the following tools. Install them based on your local environment:

- make
- git
- clang15
- libbpf
- bpftool
- curl

> Due to significant differences across local environments, build issues may occur.  
> To avoid environment inconsistencies and simplify troubleshooting, we strongly recommend using the **Docker build approach** whenever possible.
