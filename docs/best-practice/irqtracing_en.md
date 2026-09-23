---
title: IRQ Tracing
type: docs
description: ""
author: HUATUO Team
date: 2026-09-24
weight: 6
---

The standalone command collects irq/softirq stacks from every CPU or one selected CPU and builds a flame graph of the sources (who raised the softirq) and the victims (the tasks directly preempted by the softirq). When ksoftirqd services a softirq, it is the executor rather than a victim and is excluded from victim samples. It is the same tool the `huatuo-bamai` daemon shells out to from its `AutoTracing.IRQTracing` tracer; running it directly produces the same result without any storage backend or configuration file.

## Build

The normal project build discovers `cmd/irqtracing` automatically and compiles `bpf/irqtracing.c`:

```bash
make build
```

The command is written to `_output/bin/irqtracing` and the BPF object to `_output/bpf/irqtracing.o`; the object must be present at run time (`--bpf-path`).

## Run

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 > irqtracing.folded
```

`--target-cpu` is optional and defaults to `-1`, which traces every CPU. The tool attaches only the `irq/softirq_raise` and `irq/softirq_entry` tracepoints. All-CPU mode aggregates sources and directly preempted victims across the system; selecting a non-negative CPU keeps only data from that CPU. Both modes collect for `--duration` seconds (default 3).

`--max-events-per-second-per-cpu N` optionally limits the combined number of source and victim stack samples collected per second on each traced CPU. Omitting it or setting it to `0` disables rate limiting; when enabled, `N` must be between 2 and 8589934590. The per-CPU budget is divided as evenly as possible between `softirq_raise` and `softirq_entry`; raise receives the extra sample when the value is odd. In all-CPU mode, the system-wide limit scales with the number of traced CPUs.

Local output is written to stdout. `--output text` is the default and an alias
for `collapsed`; it emits folded stacks suitable for flame graph tools:

```text
source;source[raiser,NET_RX];raise_softirq_[k] 12
victim;victim[worker(123),NET_RX];work 4
```

Use `--output flamegraph` or `--output svg` to render an SVG flame graph
directly:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output flamegraph > irqtracing.svg
```

Use `--output json` when the platform profile payload and drop count are needed:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output json > irqtracing.json
```

The JSON contains:

```json
{
  "flamedata": { "...": "profile tree, null when nothing was collected" },
  "nmissed": 1234
}
```

`flamedata` is the same profile format the platform consumes (`ProfileType` `irqtracing:irq:count:irq:count`). Every stack is rooted at `source` or `victim`, followed by a `source[comm,VEC]` or `victim[comm(pid),VEC]` label frame, then the user-space frames and the kernel frames suffixed with `_[k]`. `VEC` is one of `HI`, `TIMER`, `NET_TX`, `NET_RX`, `BLOCK`, `IRQ_POLL`, `TASKLET`, `SCHED`, `HRTIMER`, `RCU`, or `VEC<n>` for unknown vectors.

`nmissed` is the total number of samples dropped during the collection window. Samples are dropped either by the first-N budgets enabled through `--max-events-per-second-per-cpu` or because a counts map is full. Either case means the flame graph is incomplete. When the drop count cannot be read, the tool fails instead of writing a result. A non-zero `nmissed` also produces a warning line on stderr; the JSON field is the persisted contract consumed by the daemon.

`--output-storage <socket>` sends the JSON payload over Toolstream instead of
writing local output and requires `--task-id`. An explicitly supplied
`--output` is ignored in that mode. Operational logs are discarded; local
results go to stdout and failures or drop warnings go to stderr.

The process exits when the duration elapses, when the caller cancels, or when it receives `SIGHUP`, `SIGQUIT`, `SIGINT`, or `SIGTERM`.

## Use from huatuo-bamai

The daemon's `AutoTracing.IRQTracing` tracer invokes the `irqtracing` binary located next to the daemon executable itself (`CoreBinDir` is derived from the daemon's own path; in the source build tree that is `_output/bin/irqtracing`) when its rules detect an irq/softirq spike or sustained high utilization on some CPU, passing `--bpf-path <CoreBpfDir>/irqtracing.o`, `--target-cpu <cpu>`, `--duration <RunTracingToolTimeout>` and `--max-events-per-second-per-cpu <MaxEventsPerSecond>`. `MaxEventsPerSecond` defaults to 1000, so raise and entry each default to 500 events/s. The CLI returns its result over Toolstream and the daemon merges it into the saved tracing data; the daemon additionally records `rule`, `trigger_cpu`, `trace_duration` and `hit_cpus`.
