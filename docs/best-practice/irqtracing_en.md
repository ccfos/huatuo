---
title: IRQ Tracing
type: docs
description: ""
author: HUATUO Team
date: 2026-09-24
weight: 6
---

`irqtracing` collects softirq call stacks on all CPUs or a selected CPU and generates flame graphs. `source` identifies what triggered the softirq; `victim` identifies the task directly preempted by it. When ksoftirqd handles the softirq, it is the executor and is not recorded as a victim.

The tool can produce local output independently or be invoked by `huatuo-bamai` through `AutoTracing.IRQTracing`. Local output requires no storage backend or configuration file.

## Build

The standard project build discovers `cmd/irqtracing` and compiles `bpf/irqtracing.c` automatically:

```bash
make build
```

The binary is written to `_output/bin/irqtracing` and the BPF object to `_output/bpf/irqtracing.o`. Specify the object with `--bpf-path` at runtime.

## Run

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format collapsed --output-path irqtracing.folded
```

The optional `--target-cpu` defaults to `-1`, tracing all CPUs. The tool attaches only to the `irq/softirq_raise` and `irq/softirq_entry` tracepoints. All-CPU mode aggregates sources and directly preempted victims across CPUs; a nonnegative CPU ID retains only that CPU's data. Both modes collect for `--duration` seconds (default: 3).

The optional `--max-events-per-second-per-cpu N` limits the total source and victim stack samples collected per tracked CPU per second. Omitting it or setting it to `0` disables the limit. When enabled, `N` must be between 2 and 8589934590. The per-CPU budget is split as evenly as possible between `softirq_raise` and `softirq_entry`; for odd budgets, raise gets the extra sample. In all-CPU mode, the system-wide budget grows with the number of tracked CPUs.

## Output

Explicitly specify both `--output-path` and `--output-format`. Supported formats are `collapsed` (the default shown in help), `flamegraph`, `svg`, `json`, and `remote-store`. Local results go to the specified file, directory, or `stdout`. With `stdout`, terminals, redirection, or log systems may retain collected data. A nonexistent path is treated as a new file unless it ends with `/`, which creates a directory. `collapsed` emits folded stacks for flame graph tools:

```text
source;source[raiser,NET_RX];raise_softirq_[k] 12
victim;victim[worker(123),NET_RX];work 4
```

Use `--output-format flamegraph` or `--output-format svg` to produce an SVG flame graph directly:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format flamegraph --output-path irqtracing.svg
```

The shared collection framework logs a warning when a window contains dropped samples, including the task ID and counts by reason. IRQ logs at warning level or above go to stderr; results go to the specified destination. Failure to write diagnostic logs does not prevent data export.

### Remote Storage

To upload a standard profile and dropped-sample counts, use `remote-store` with a toolstream Unix socket and a unique task ID:

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format remote-store --output-path /var/run/huatuo-toolstream.sock \
  --task-id irq-demo-1
```

### JSON

Use `--output-format json --output-path irqtracing.json` to save the same window data locally. JSON content:

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

`profile` uses the same `types.ProfilingWindow` format as the profiler tool. The example is an empty window; populated windows also contain sample, location, and function data. An empty window still returns a valid profile with its start time and actual collection duration. Stacks retain `source` / `victim` roots and process and interrupt-vector grouping, followed by user-space frames and kernel frames with the `_[k]` suffix. The selected target CPU is also recorded in sample labels.

`dropped_samples.collection` counts samples dropped because the kernel sampling budget or counts map is full; the current counter cannot distinguish these causes. A nonzero value means the profile is incomplete and produces a stderr warning. Failure to read the counter fails collection. A full profiler user-space queue waits for aggregation to make space or for the processing context to be canceled; `dropped_samples.aggregation_queue` is retained only for historical data compatibility.

## Profile the tool's Go runtime

`--enable-pprof` defaults to false. Enable it to listen on `127.0.0.1:6000`; an occupied port fails startup. These profiles describe the irqtracing process itself, not IRQ/softIRQ stacks or huatuo-bamai. Keep the tool running throughout collection.

## Invocation by huatuo-bamai

When its rules detect a spike or sustained high irq/softirq utilization on a CPU, the daemon's `AutoTracing.IRQTracing` tracer invokes the `irqtracing` binary beside the daemon executable. `CoreBinDir` is derived from the daemon's path; in a source build, the tool is `_output/bin/irqtracing`.

It passes `--bpf-path <CoreBpfDir>/irqtracing.o`, `--target-cpu <cpu>`, `--duration <RunTracingToolTimeout>`, and `--max-events-per-second-per-cpu <MaxEventsPerSecond>`. `MaxEventsPerSecond` defaults to 1000, giving raise and entry a default limit of 500/s each.

This path requires Elasticsearch configuration. Tracing local-file storage alone cannot save IRQ profiles. Trigger rules and the triggering CPU's utilization are recorded in daemon logs.

### Results and Compatibility

IRQ results are written to `profile_data` in profiling storage with `tracer_name=irqtracing` and `tracer_type=profiling`. Query them through the profiling API using `irqtracing:irq:count:irq:count`. Historical tracing documents remain accessible through the original tracing queries and are not rewritten. The old `flamedata`/`nmissed` transport format has been removed; protocol version 3 requires the CLI and daemon to be upgraded together.

### Completion

A task succeeds only after the profile is queryable, publication succeeds, and the tool receives the daemon's acknowledgment. Ending data transmission alone, collection failure, or disconnection does not publish a task completion marker. If the acknowledgment is lost, results may already be published; query by task ID to confirm. See [Profiling Window Commit](/docs/development/storage_en.md#profiling-window-commit) for implementation details.
