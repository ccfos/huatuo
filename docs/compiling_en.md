---
title: Building from Source
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

This guide covers container and host builds. Run all commands from the repository root. See `go.mod` for the required Go version.

## Container Build

Run the script to build in a container and perform static checks:
```bash
$ sh build/build-run-testing-image.sh
```

Or run the steps separately:

**1. Prepare the build environment**
```bash
$ docker build --network host -t huatuo/huatuo-bamai-dev:latest -f ./Dockerfile.devel .
```

**2. Start the build container**
```bash
$ docker run -it --pid=host --privileged --cgroupns=host --network=host -v $(pwd):/go/huatuo-bamai huatuo/huatuo-bamai-dev:latest sh
```

**3. Build inside the container**
```bash
$ make
```

## Runtime Image

The following command builds a local runtime image. It does not push the image to a registry:

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

## Host Build

### Install Dependencies

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

### Build

```bash
$ make
```

## Memray Runtime Bundle

`make` and `make build` download the pinned Memray bundle into
`_output/tools/memray`. The bundle contains CPython 3.7–3.14 runtimes for
Linux glibc on x86-64 or AArch64. No Python build dependencies or Memray
source checkout are needed. The download requires `curl`, `tar`, and
`sha256sum`; each architecture has a pinned checksum.

Prepare just the runtime, or use a previously downloaded release archive:

```bash
make prep-memray-bundle
MEMRAY_BUNDLE_ARCHIVE=/path/to/memray-huatuo.tar.gz make prep-memray-bundle
```

Use `MEMRAY_BUNDLE_ENABLED=0 make build` when building without Python memory
support. The binary still builds, but memory attachment requires a runtime
bundle supplied later, either at the default path or through `--tool-path`.
To change the release, set `MEMRAY_BUNDLE_VERSION` and its matching
`MEMRAY_BUNDLE_SHA256`; use `MEMRAY_BUNDLE_REPOSITORY` for a release mirror.

Python memory collection needs `gdb` and ptrace permission in the running
profiler environment. The target Python process must use glibc; musl and
free-threaded CPython are outside this bundle's support matrix.

## BPF Debug Build

Set `BPF_DEBUG=1` to pass `-DDEBUG_BPF` to Clang and compile debug code into the BPF object:

```bash
$ make BPF_DEBUG=1     # Or regenerate artifacts only: make BPF_DEBUG=1 gen-build
```

See [Debugging](/docs/development/debugging_en.md) for instrumentation, runtime switches, and logs.
