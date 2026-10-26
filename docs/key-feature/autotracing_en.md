---
title: Automatic Tracing
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

## Overview

AutoTracing starts collecting diagnostics when metrics meet configured conditions, helping investigate intermittent CPU, I/O, load, and memory anomalies. CPU spikes trigger the configured `perf` or `profiler` tool; other scenarios collect process states, call stacks, memory usage rankings, or runtime snapshots as needed.

| Problem | Tracer | Results |
| --- | --- | --- |
| Host system CPU spike | `cpusys` | Host CPU flame graph and triggering metrics |
| Container CPU spike | `cpuidle` | Container CPU flame graph and triggering metrics |
| Elevated container D-state load | `dload` | Process state statistics and kernel stacks of D-state processes |
| Disk I/O anomaly | `iotracing` | Process and file I/O rates, and I/O scheduling wait stacks |
| Host anonymous memory spike | `memburst` | Processes with the highest RSS and runtime memory snapshots |

Collection starts only after a threshold is triggered, so results cannot reconstruct the full execution history before the trigger. Correlate results with the anomaly time, resource metrics, and application requests.

## Configuration

Configure `AutoTracing` in `huatuo-bamai.conf`. For field defaults, units, and limits, see “Automatic tracing” in [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md).

| Tracer | Configuration section |
| --- | --- |
| `cpusys` | `AutoTracing.CPUSys` |
| `cpuidle` | `AutoTracing.CPUIdle` |
| `dload` | `AutoTracing.Dload` |
| `iotracing` | `AutoTracing.IOTracing` |
| `memburst` | `AutoTracing.MemoryBurst` |

Before use, check the global blacklist, target container filters, and collection tool availability. Cooldown periods and scope depend on the tracer. Default thresholds in this document are built-in values; the configuration file can override them.

Node Configuration also covers interrupt utilization anomalies and container memory snapshots triggered by thresholds. This page focuses on the five tracing results listed above.

## Results

Automatic tracing events are written to Elasticsearch or local files according to the enabled storage configuration. Filter by host, container, time range, and tracer, then correlate results with application requests.

For CPU collection, `perf` embeds flame graph data in the event. With `profiler`, the event retains trigger metrics; profiles are stored separately in Elasticsearch and linked by the same `tracer_id`. See [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md#7-automatic-tracing) for storage requirements and viewing instructions.

### Common fields

| Field | Purpose |
| --- | --- |
| `hostname` / `container_id` | Identify the node or container; container fields are provided only when association succeeds |
| `tracer_name` / `tracer_id` | Identify the tracer and the individual trace |
| `started_timestamp` | Correlate the trace with the anomaly time |
| `tracer_type` | Set to `autotracing` for automatic tracing records |
| `tracer_data` | Triggering metrics and collected results |

For the full record structure, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#common-fields).

### Host system CPU

Tracer: `cpusys`. Select the tool with `AutoTracing.CPUSys.Tool` (default: `perf`).

Host CPU sampling starts when both system CPU utilization and its increase between consecutive samples exceed their thresholds. The default thresholds are 45% and 20 percentage points, respectively, with a 30-minute cooldown.

Results include the triggering metrics and a CPU flame graph for identifying call paths with high system CPU usage.

For fields and examples, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#host-system-cpu).

### Container CPU

Tracer: `cpuidle`. Select the tool with `AutoTracing.CPUIdle.Tool` (default: `perf`).

Container-level CPU sampling starts when any one of user, system, or total CPU utilization exceeds both its utilization threshold and its increase threshold. The default cooldown is 30 minutes. `AutoTracing.CPUIdle.Filter` controls the target scope.

Results include utilization values, their increases, and a container CPU flame graph for examining CPU hotspots within the container.

For fields and examples, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#container-cpu).

### Container D-state load

Tracer: `dload`.

When the exponential moving average (EMA) of container D-state load exceeds the threshold, the tracer collects kernel stacks of D-state processes in the container and on the host. The default threshold is 5, with a 30-minute cooldown per container.

Results also include process state statistics. Use the stacks to investigate where processes block, and `AutoTracing.IssuesList` to mark known issues.

For fields and examples, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#container-d-state-load).

### Disk I/O

Tracer: `iotracing`.

The tracer reads `/proc/diskstats` every 5 seconds. Trigger conditions must hold for two consecutive sampling periods: non-NVMe devices are checked for utilization; NVMe devices must exceed both the utilization threshold and either the read or write throughput threshold. Average read and write wait times are also checked independently for each device.

Results include process and file I/O rates and I/O scheduling wait stacks, helping identify sources of I/O usage and waiting paths.

For fields and examples, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#disk-io).

### Host anonymous memory

Tracer: `memburst`.

By default, the tracer samples every 10 seconds and starts evaluating conditions after collecting 60 samples. It triggers when current anonymous memory usage reaches twice the oldest value in the window and at least 70% of total host memory. The default cooldown is 30 minutes.

By default, results list the top 10 processes by RSS and attempt runtime memory snapshots for the top three to narrow the search for memory consumption. Snapshot support depends on the target runtime. Failed or unavailable captures include a status explanation; they do not mean that the process uses no memory.

For fields and examples, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#host-anonymous-memory).
