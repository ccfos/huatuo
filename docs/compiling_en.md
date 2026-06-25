---
title: Compiling from Source
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

### 1. Container Build

Run the following command to compile the project and perform static analysis:
```bash
$ sh build/build-run-testing-image.sh
```

Or run each step individually:

**1. Prepare the build environment**
```bash
$ docker build --network host -t huatuo/huatuo-bamai-dev:latest -f ./Dockerfile.devel .
```

**2. Start the build container**
```bash
$ docker run -it --privileged --cgroupns=host --network=host -v $(pwd):/go/huatuo-bamai huatuo/huatuo-bamai-dev:latest sh
```

**3. Build inside the container**
```bash
$ make
```

### 2. Container Image Release

Use `docker build` to produce the latest binary container image.

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

### 3. Native Build

#### 3.1 Install Dependencies

Ubuntu 24.04:
```bash
apt install make git clang libbpf-dev linux-tools-common curl capnproto
```

Fedora 40:
```bash
dnf install make git clang libbpf-devel bpftool curl capnproto capnproto-devel glibc-static
```

```bash
go install mvdan.cc/gofumpt@v0.8.0
go install mvdan.cc/sh/v3/cmd/shfmt@v3.11.0
go install golang.org/x/tools/cmd/goimports@v0.36.0
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
go install github.com/vektra/mockery/v2@v2.53.6
go install capnproto.org/go/capnp/v3/capnpc-go@v3.1.0-alpha.2
```

#### 3.2 Build
```bash
$ make
```
