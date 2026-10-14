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
  --duration 5 \
  --output-format collapsed --output-path irqtracing.folded
```

`--target-cpu` is optional and defaults to `-1`, which traces every CPU. The tool attaches only the `irq/softirq_raise` and `irq/softirq_entry` tracepoints. All-CPU mode aggregates sources and directly preempted victims across the system; selecting a non-negative CPU keeps only data from that CPU. Both modes collect for `--duration` seconds (default 3).

`--max-events-per-second-per-cpu N` optionally limits the combined number of source and victim stack samples collected per second on each traced CPU. Omitting it or setting it to `0` disables rate limiting; when enabled, `N` must be between 2 and 8589934590. The per-CPU budget is divided as evenly as possible between `softirq_raise` and `softirq_entry`; raise receives the extra sample when the value is odd. In all-CPU mode, the system-wide limit scales with the number of traced CPUs.

Both `--output-path` and `--output-format` must be supplied explicitly. Supported formats are `collapsed` (the displayed default), `flamegraph`, `svg`, `json`, and `remote-store`. Local output goes to the specified file, directory, or `stdout`. Warning: stdout may expose profiling data through the terminal, redirection, or captured logs; use it with care. A nonexistent path denotes a new file unless it ends in `/`, which creates a directory. Collapsed output contains folded stacks suitable for flame graph tools:

```text
source;source[raiser,NET_RX];raise_softirq_[k] 12
victim;victim[worker(123),NET_RX];work 4
```

Use `--output-format flamegraph` or `--output-format svg` to render an SVG flame graph
directly:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format flamegraph --output-path irqtracing.svg
```

The common collection pipeline logs one warning per window with sample loss, including the task ID and counts by reason. IRQ logs warnings and errors to stderr and writes results to the configured destination. A diagnostic log write failure does not prevent data export.

Use `remote-store` to upload the standard profile and drop counts through a toolstream Unix socket. Supply a unique task ID:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format remote-store --output-path /var/run/huatuo-toolstream.sock \
  --task-id irq-demo-1
```

Use `--output-format json --output-path irqtracing.json` to save the same window payload locally. The JSON contains:

```json
{
  "profile_type": "irqtracing:irq:count:irq:count",
  "profile": {
    "time_nanos": 1790000000000000000,
    "duration_nanos": 3000000000,
    "sample_type": [{"type": 1, "unit": 2}],
    "string_table": ["", "irq", "count"]
  },
  "dropped_samples": {"collection": 1234}
}
```

`profile` is the same standard profile carried by `types.ProfilingWindow` for the profiler tool. The example shows an empty window; non-empty windows also contain samples, locations and functions. Empty collections retain a valid profile with its start time and actual duration. Stacks preserve the `source` / `victim` roots and their process/vector grouping, followed by user-space and kernel (`_[k]`) frames. A selected target CPU is also recorded as a sample label.

`dropped_samples.collection` counts samples discarded by the kernel sample budget or full counts maps. The aggregate counter cannot distinguish these causes. A nonzero count means the profile is incomplete and also produces a stderr warning. Failure to read the counter fails collection. Profiler userspace queue loss uses the separate `dropped_samples.aggregation_queue` reason.

## Use from huatuo-bamai

The daemon's `AutoTracing.IRQTracing` tracer invokes the `irqtracing` binary located next to the daemon executable itself (`CoreBinDir` is derived from the daemon's own path; in the source build tree that is `_output/bin/irqtracing`) when its rules detect an irq/softirq spike or sustained high utilization on some CPU, passing `--bpf-path <CoreBpfDir>/irqtracing.o`, `--target-cpu <cpu>`, `--duration <RunTracingToolTimeout>` and `--max-events-per-second-per-cpu <MaxEventsPerSecond>`. `MaxEventsPerSecond` defaults to 1000, so raise and entry each default to 500 events/s. The CLI runs through the shared profiler runner and single-window pipeline. It sends `types.ProfilingWindow` over Toolstream. The handler registered by `internal/profiling.Register` writes each window through `pkg/profiling/store.SaveSync`; the same handler calls `Store.Commit` on an error-free commit request to publish the durable completion marker. Collection succeeds only after the profile is query-visible and publication succeeds. Elasticsearch must be configured; local tracing-file storage alone is insufficient. Trigger rules and hit-CPU utilization remain in daemon logs.

New IRQ results use `profile_data` in profiling storage, with `tracer_name=irqtracing` and `tracer_type=profiling`. Query them through the profiling APIs using `irqtracing:irq:count:irq:count`. Historical tracing documents remain available through their existing tracing queries; they are not rewritten. The old `flamedata`/`nmissed` wire format is removed, so upgrade the CLI and daemon together.

Remote output sends `end + commit` and waits for the daemon ACK confirming storage and publication. Plain End, collection failure and disconnect do not publish a completion marker. Protocol version 2 requires upgrading tools and daemon together. If the ACK is lost, the result may already be published; query by task ID to confirm.
