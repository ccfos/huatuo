---
title: Debugging
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

Choose a debugging workflow: [Full Stack](#full-stack), [BPF Debugging](#bpf-debugging), or [GitHub Actions VM Debugging](#vm-debugging).

## Full Stack

The development Compose setup builds an image from the current workspace and starts the collector, API Server, Elasticsearch, Prometheus, and Grafana. The collector needs access to the host kernel and cgroups. Run from the repository root on Linux with root privileges:

```bash
sudo make compose-dev-up
```

Compose aggregates component logs in the foreground. After changing source code, stop the environment with `Ctrl+C` and rerun the command. Docker reuses the toolchain and Go build caches.

After debugging, remove the containers, data volumes, and development image:

```bash
sudo make compose-dev-down
```

This command deletes the Elasticsearch data volume. Do not run it if you need to retain the data.

## Go Debugging

Normal `make build` binaries use Go's default optimizations and inlining in both
static and nostatic modes. For source-level debugging, build a separate binary
with optimizations disabled and debug information retained, for example:

```bash
make gen-build
CGO_ENABLED=1 go build -tags "netgo osusergo" -gcflags=all="-N -l" \
  -o _output/bin/profiler-debug ./cmd/profiler
```

These compiler flags apply to Go code. `BPF_DEBUG` controls BPF diagnostics
independently.

## BPF Debugging

BPF code can emit kernel debug messages through the `bpf_dbg()` and `bpf_dbg_msg()` macros defined in `bpf/include/bpf_dbg.h`. Debugging has both compile-time and runtime switches and is fully disabled by default.

### Add Instrumentation

Each BPF source file using debug macros must declare its own debug map:

```c
#include "bpf_dbg.h"

BPF_DBG_MAP(native_cpu);

SEC("perf_event")
int prog(void *ctx)
{
        bpf_dbg_msg(ctx, native_cpu, "enter prog");
        bpf_dbg(ctx, native_cpu, "pid and addr", pid, addr, 0);
        return 0;
}
```

`bpf_dbg_msg()` emits a message only. `bpf_dbg()` can also include up to three `u64` arguments.

### Build Debug Objects

Set `BPF_DEBUG=1` to pass `-DDEBUG_BPF` to Clang:

```bash
make BPF_DEBUG=1
```

To regenerate artifacts without rebuilding the binaries:

```bash
make BPF_DEBUG=1 gen-build
```

`BPF_DEBUG=0` is the default. In this mode, macros expand to no-ops; the debug perf event array, event structure, `bpf_ktime_get_ns`, and `bpf_perf_event_output` are excluded from the BPF object.

### Enable Runtime Output

After building debug objects, also pass `--log-bpf-debug` when starting the profiler. Only the native profiler currently supports this flag:

```bash
./profiler --type cpu --language native --log-bpf-debug ...
```

During BPF loading, `bpf.NewDbg(true)` rewrites the `bpf_dbg_enabled` constant to 1 before `LoadBpf`. When disabled, the verifier eliminates the corresponding branches as dead code. Each BPF object has an independent switch.

### Read Debug Output

User space logs debug events at Debug level:

- `file`: BPF source filename.
- `line`: source line number.
- `ts`: event timestamp converted to UTC wall time.
- `msg`: debug message.
- `args`: up to three `u64` arguments; omitted when all are zero.

```text
bpf_dbg: file=native_oncpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0]
```

Debug output requires both a build with `BPF_DEBUG=1` and the runtime flag `--log-bpf-debug`.

## VM Debugging

Use the `OS Distro QEMU CI` workflow to test a branch in supported Linux virtual machines without preparing a local VM.

### Prepare a Fork

1. Fork the repository and enable workflows on the fork's **Actions** page.
2. Add your SSH public key to your [GitHub account](https://github.com/settings/keys). Only the user who starts the workflow can connect.
3. Push the branch to debug to your fork.

### Start a VM

1. Open **Actions > OS Distro QEMU CI > Run workflow** in your fork.
2. Select the branch to debug.
3. Keep the default `ubuntu24.04`, enter multiple distributions separated by commas, or use `all` to select all supported distributions.
4. Under **Debug VM**, choose when and how long to retain the VM.
5. Start the workflow and open its `Test in VM` job.

If **Run workflow** is missing, sync your fork's default branch first. GitHub only displays manual workflows that exist on the default branch.

Choose **Before test** to inspect or change the VM before testing, **After test on failure** to investigate a failed test, or **After test always** to retain the VM after testing regardless of the outcome.

### Connect to the VM

Open the job's `Debug VM: Retain ...` step and follow the log instructions:

1. Run the complete SSH command from the log in a local terminal to connect to the GitHub runner.
2. On the runner, run the logged `ssh -i ... root@<VM_IP>` command to enter the VM.

Copy the commands directly; they already contain the correct key path and VM address.

### End the Session

Exit the VM, then release the workflow from the runner:

```bash
cd "$GITHUB_WORKSPACE"
touch continue
```

The workflow then continues with testing or cleanup. Disconnecting SSH alone does not release the workflow. Without this command, it waits until the selected retention period expires.
