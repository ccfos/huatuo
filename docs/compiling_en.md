---
title: Building from Source
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

### 1. Container Build

Run the following command to build the project and run static code checks.
```bash
$ sh build/build-run-testing-image.sh
```

Or run each step separately:

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

### 2. Publishing the Image

Use `docker build` to publish the latest binary container image.

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

### 3. Bare-Metal Build

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

### 4. BPF Debug Build

Set `BPF_DEBUG=1` to pass `-DDEBUG_BPF` to clang and compile the debug code into the BPF object:

```bash
$ make BPF_DEBUG=1            # Or build only the BPF objects: make BPF_DEBUG=1 bpf-build
```

See [Debugging](development/debugging_en.md) for trace points, runtime switches,
and log output.
