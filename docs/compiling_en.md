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

#### 2.1 Multi-arch Build (linux/amd64 + linux/arm64)

**1. Environment Setup**

```bash
# Register QEMU user-mode emulation
docker run --rm --privileged tonistiigi/binfmt --install all

# Create multi-arch builder
docker buildx create --name multiarch \
    --driver docker-container \
    --driver-opt network=host \
    --use

# Verify (also triggers bootstrap)
docker buildx inspect multiarch --bootstrap
```

**2. Build and Push**

```bash
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    --network=host \
    -t <your-registry>/huatuo-bamai:latest \
    -f Dockerfile \
    --push .
```

**3. Verify Multi-arch Manifest**

```bash
docker buildx imagetools inspect <your-registry>/huatuo-bamai:latest
```

Expected output contains both platform entries:

```
Manifests:
  Platform:  linux/amd64
  Platform:  linux/arm64
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

BPF code can use the `bpf_dbg()` and `bpf_dbg_msg()` macros (defined in `bpf/include/bpf_dbg.h`) to print debug information from kernel space. This helps you trace the runtime logic of eBPF programs. The feature uses a two-stage switch. It is fully disabled by default and adds no overhead to the production path.

#### 4.1 Adding Trace Points in BPF Code

```c
#include "bpf_dbg.h"

// Declare a debug map in each .c file that contains a BPF program to trace
// (the map name must match the name used below).
BPF_DBG_MAP(native_cpu);

SEC("perf_event")
int prog(void *ctx)
{
        // Print a message only
        bpf_dbg_msg(ctx, native_cpu, "enter prog");

        // Print a message with up to 3 u64 arguments
        bpf_dbg(ctx, native_cpu, "pid and addr", pid, addr, 0);
        return 0;
}
```

When `BPF_DEBUG=0` (the default), these macros expand to no-ops. The debug perf event array, the on-stack event struct, `bpf_ktime_get_ns`, and `bpf_perf_event_output` are **not emitted**. The verifier never sees them, the `.o` file is smaller, and no extra file descriptor is consumed at load time.

#### 4.2 Enabling at Build Time (Stage 1: DEBUG_BPF)

Set `BPF_DEBUG=1` to pass `-DDEBUG_BPF` to clang and compile the debug code into the BPF object:

```bash
$ make BPF_DEBUG=1            # Or build only the BPF objects: make BPF_DEBUG=1 bpf-build
```

#### 4.3 Enabling at Runtime (Stage 2: log-bpf-debug)

Even when compiled into the object, debug output is still suppressed at runtime by default. To turn it on, pass `--log-bpf-debug` when you start the profiler (currently effective for the native profiler only):

```bash
$ ./profiler --type cpu --language native --log-bpf-debug ...
```

This works as follows: when the BPF object is loaded, a `BpfDbg` instance created by `bpf.NewDbg(true)` rewrites the `bpf_dbg_enabled` constant to 1 before `LoadBpf`. Without this rewrite, the verifier eliminates `if (bpf_dbg_enabled)` as dead code. Each BPF object holds its own `BpfDbg`, so the debug switches are independent of one another.

#### 4.4 Output

Debug events are printed by user space as Debug-level logs. Each entry contains:

- `file`: the BPF source file name where the trace point fired (`__FILE_NAME__`)
- `line`: the source line number
- `ts`: the event timestamp (`bpf_ktime_get_ns` converted to UTC wall-clock time)
- `msg`: the message string passed to the trace point
- `args`: optional, up to 3 `u64` arguments (omitted when all are 0)

Example:

```text
bpf_dbg: file=native_cpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0 0x0]
```

> Note: Compile with `BPF_DEBUG=1` **and** run with `--log-bpf-debug`.