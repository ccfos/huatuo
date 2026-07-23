---
title: Development Debugging
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

## Full-Stack Integration

The development Compose configuration builds an image from the current
workspace and starts the collector, API Server, Elasticsearch, Prometheus, and
Grafana. The collector needs access to the host kernel and cgroups, so run the
command with root privileges from the repository root on a Linux host:

```bash
sudo make compose-dev-up
```

Compose aggregates all component logs in the foreground. After changing the
source, press `Ctrl+C` and run the command again. Docker reuses the toolchain
layers and Go build cache.

Remove the containers, data volumes, and development image after debugging:

```bash
sudo make compose-dev-down
```

This command removes the Elasticsearch data volume. Do not run it when the
integration data must be retained.

## BPF Debugging

BPF code can use the `bpf_dbg()` and `bpf_dbg_msg()` macros to emit debug
information from kernel space. The macros are defined in
`bpf/include/bpf_dbg.h`. Debugging has separate build-time and runtime switches
and is completely disabled by default.

### Add debug trace points

Each BPF source file that uses the macros must declare its own debug map:

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

`bpf_dbg_msg()` emits a message only. `bpf_dbg()` also accepts up to three
`u64` arguments.

### Build debug objects

Set `BPF_DEBUG=1` to pass `-DDEBUG_BPF` to Clang:

```bash
make BPF_DEBUG=1
```

To rebuild only the BPF objects:

```bash
make BPF_DEBUG=1 bpf-build
```

`BPF_DEBUG=0` is the default. In that mode the macros expand to no-ops, and the
debug perf event array, event structure, `bpf_ktime_get_ns`, and
`bpf_perf_event_output` are not emitted into the BPF object.

### Enable runtime output

After building the debug objects, pass `--log-bpf-debug` when starting the
profiler. The option currently applies only to the native profiler:

```bash
./profiler --type cpu --language native --log-bpf-debug ...
```

When loading the BPF object, `bpf.NewDbg(true)` rewrites the
`bpf_dbg_enabled` constant to 1 before `LoadBpf`. When it is disabled, the
verifier eliminates the branch as dead code. Each BPF object maintains an
independent switch.

### Read debug output

User space emits each debug event at Debug level with these fields:

- `file`: BPF source file.
- `line`: source line number.
- `ts`: event timestamp converted to UTC wall-clock time.
- `msg`: debug message.
- `args`: up to three `u64` arguments, omitted when all values are zero.

```text
bpf_dbg: file=native_cpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0]
```

Debug output requires both a build with `BPF_DEBUG=1` and the runtime
`--log-bpf-debug` option.
