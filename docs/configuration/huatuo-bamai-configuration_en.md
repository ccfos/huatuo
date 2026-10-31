---
title: Node Configuration
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 4
---

### 1. Overview

`huatuo-bamai.conf` configures huatuo-bamai's collection scope, tracing rules, resource limits, storage, and logging.

This document quotes `huatuo-bamai.conf` from the repository and describes its fields in tables. It distinguishes three kinds of values:

- **Built-in defaults**: Values supplied by the program when a field is omitted. “Default” below refers to these values.
- **Explicit configuration values**: Uncommented assignments that override built-in defaults when the file is loaded.
- **Commented examples**: Assignments prefixed with `#`. They are not loaded and may differ from built-in defaults.

You do not need to uncomment a field to use its built-in default. Restart after editing the configuration file manually. For updates through the Node API, see Section 5.1 for when changes take effect.

### 2. Global blacklist

```toml
# Global tracing and metrics configuration.
# Default comments describe built-in values used when a field is omitted.
# Uncommented assignments override those values; commented assignments are
# inactive examples and may differ from the built-in defaults.
#
# - BlackList
# Global blacklist for tracing and metrics.
# Default: [] (empty). The list below explicitly disables these modules.
#
BlackList = ["netdev_hw", "netdev_qdisc", "net_conntrack", "metax_gpu", "ascend_npu", "diskio", "tcp_retransmit", "mthreads_gpu", "mthreads_xid"]
```

| Field | Description |
| --- | --- |
| `BlackList` | Array of tracer and metric collector names to disable. The built-in default is `[]`. To enable a module, remove its name, satisfy its dependencies and enablement conditions, and restart to register it. |

The configuration file explicitly disables these modules:

| Blacklist entry | Function |
| --- | --- |
| `netdev_hw` | Network interface hardware statistics |
| `netdev_qdisc` | Queue statistics |
| `net_conntrack` | Connection tracking statistics |
| `metax_gpu` | Metax GPU metrics |
| `ascend_npu` | Ascend NPU metrics |
| `diskio` | Disk I/O metrics |
| `tcp_retransmit` | TCP retransmission tracing |
| `mthreads_gpu` | Moore Threads GPU metrics |
| `mthreads_xid` | Moore Threads GPU XID events |

### 3. Logging

```toml
# Log Configuration
[Log]
    # - Level
    # The log level for huatuo-bamai: Debug, Info, Warn, Error, Panic.
    # Default: Info
    #
    # - File
    # Store logs to where the logging file is. If it is empty, don't write log
    # to any file.
    # Default: empty
    #
    # Level = "Info"
    # File = ""
```

| Field | Description |
| --- | --- |
| `Level` | Log level. Default: `Info`. Options: `Debug`, `Info`, `Warn`, `Error`, and `Panic`. Use `Info` or `Warn` in production; `Debug` produces more output and is intended for troubleshooting. |
| `File` | Log file path. Default: `""`, which disables file output. Set this field to write logs to a file. |

### 4. Resource limits

Huatuo does not create its own cgroup by default. These settings take effect only when `--enable-cgroup` is explicitly supplied. Kubernetes and systemd deployments should use their respective resource controls.

```toml
# Runtime limits for the huatuo-bamai process.
[Runtime]
    # - StartupCPULimitCores
    # CPU limit during startup, in cores.
    # Default: 0.5
    #
    # - CPULimitCores
    # CPU limit after startup, in cores.
    # Default: 2.0
    #
    # - MemoryLimitMiB
    # Memory limit in MiB.
    # Default: 2048
    #
    # StartupCPULimitCores = 0.5
    # CPULimitCores = 2.0
    # MemoryLimitMiB = 2048
```

| Field | Description |
| --- | --- |
| `StartupCPULimitCores` | CPU limit during initialization. Default: `0.5` cores. |
| `CPULimitCores` | CPU limit after startup. Default: `2.0` cores. |
| `MemoryLimitMiB` | Process memory limit. Default: `2048` MiB. |

### 5. HTTP service

The HTTP service provides configuration updates, on-demand operation management, and event subscriptions.

```toml
# HTTP server configuration.
[HTTPServer]
    # - ListenAddress
    # Listen address in "host:port" form.
    # Default: ":19704"
    #
    # - MaxEventStreamClients
    # Maximum number of concurrent clients allowed to hold an open
    # /v1/events/watch SSE connection. Once the limit is reached, new requests
    # are rejected with HTTP 429 until an existing client disconnects.
    # Default: 100
    #
    # - EventStreamKeepAliveIntervalSeconds
    # Interval in seconds at which the server sends an SSE comment ping to each
    # connected client. The ping keeps the connection alive through load
    # balancers and proxies that would otherwise time out idle connections.
    # Default: 30
    #
    # ListenAddress = ":19704"
    # MaxEventStreamClients = 100
    # EventStreamKeepAliveIntervalSeconds = 30

    [HTTPServer.Auth]
        # Shared service credential used by huatuo-apiserver.
        # Default: "" (invalid). Replace the placeholder before starting.
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
```

| Field | Description |
| --- | --- |
| `ListenAddress` | Listen address in `host:port` format. Default: `":19704"`. An empty host listens on all interfaces. |
| `MaxEventStreamClients` | Maximum concurrent connections to `POST /v1/events/watch`. Default: `100`; must be a positive integer. New connections receive HTTP 429 when the limit is reached. |
| `EventStreamKeepAliveIntervalSeconds` | SSE comment heartbeat interval in seconds. Default: `30`; must be a positive integer. Set it below the upstream proxy's idle timeout. |
| `Auth.BearerToken` | Service authentication credential. The built-in default is `""`; a nonempty value without whitespace is required. It must match the Node credential configured in huatuo-apiserver. Replace the placeholder before deployment. |

Configuration updates, Operations, and event subscriptions require `Authorization: Bearer <token>`. `/readyz`, metrics, version information, and `GET /openapi.json` remain public. Retrieve the complete API definition through `GET /openapi.json`.

#### 5.1 Update configuration through the API

`PUT /v1/config` accepts a nonempty `config` object. Keys use dot-separated paths matching the TOML structure; values retain their JSON types:

```bash
curl -i -X PUT 'http://127.0.0.1:19704/v1/config' \
  -H 'Authorization: Bearer REPLACE_WITH_RANDOM_HEX' \
  -H 'Content-Type: application/json' \
  --data '{
    "config": {
      "BlackList": ["dropwatch", "netdev_hw"],
      "Runtime.CPULimitCores": 1.5,
      "Runtime.MemoryLimitMiB": 1024
    }
  }'
```

A successful update returns `204 No Content`. The Node Agent validates the complete configuration, atomically replaces the configuration file, and then updates the in-memory configuration. If validation or persistence fails, it continues using the previous configuration.
Unknown keys, invalid value types, and empty update objects return `400 Bad Request`.

Components that read configuration dynamically can use the new values without restarting. Settings read only at startup, such as HTTP listening and authentication, storage initialization, and cgroup settings, are written to the file but take effect only after restarting `huatuo-bamai`.

#### 5.2 On-demand task management (Operations)

`Operations` provides shared management for Profiling and Tracing on a node, including concurrency limits, launch, stop, finalization, and terminal record retention. On-demand Tracing currently has no executor: new start requests that pass parameter validation return `501` without creating an operation.

```toml
# Shared lifecycle policy for on-demand operations.
[Operations]
    # MaxConcurrent = 10
    # LaunchTimeoutSeconds = 10
    # StopGracePeriodSeconds = 5
    # FinalizationTimeoutSeconds = 30
    # TerminalRetentionPeriodSeconds = 600
```

| Field | Description |
| --- | --- |
| `MaxConcurrent` | Limit on unfinished operations, shared by Profiling and Tracing. Default: `10`; must be a positive integer. New operations are rejected rather than queued when the limit is reached. |
| `LaunchTimeoutSeconds` | Operation executor launch timeout in seconds. Default: `10`; must be a positive integer. |
| `StopGracePeriodSeconds` | Wait deadline when stopping an operation, in seconds. Default: `5`; must be a positive integer. |
| `FinalizationTimeoutSeconds` | Executor finalization timeout in seconds. Default: `30`; must be a positive integer. |
| `TerminalRetentionPeriodSeconds` | Time to retain an operation's terminal record in node memory after completion, in seconds. Default: `600`; must be a positive integer. The record is no longer available through the operation API after it expires. |

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/operations` | Create an operation. Returns `202` for a new operation, `200` for an existing idempotent operation, or `429` when the concurrency limit is reached. |
| `GET /v1/operations/{request_id}` | Query operation status. Returns `404` if the record does not exist or has expired. |
| `POST /v1/operations/{request_id}/stop` | Request an operation to stop. Returns `202` when the stop request is accepted, or `200` if the operation is already stopping or terminal. |

Start requests select `profiling` or `tracing` through `kind` and specify the execution duration through `duration_seconds`. The launch, stop, and finalization timeouts apply to their respective stages; they do not replace the execution duration.

#### 5.3 Profiling

Start profiling through `POST /v1/operations`. Provide `request_id`, `duration_seconds`, and `scope`; set `kind = "profiling"`; and include `type`, `language`, and `mode` in `spec`. Container scope also requires `container_id`. Use the shared operation endpoints in Section 5.2 to query or stop the operation.

```toml
# Node-local on-demand profiling settings.
[Profiling]
    # AggregationIntervalSeconds = 10
    # MaxConcurrentProcesses = 10
    # CommandOutputLimitBytes = 65536
    # Java uses bin/asprof and lib/libasyncProfiler.so; Python uses py-spy.
    # ToolDir defaults to ""; the path below is an example.
    # ToolDir = "/opt/huatuo/tools"
```

| Field | Description |
| --- | --- |
| `AggregationIntervalSeconds` | Sample aggregation interval in seconds. Default: `10`; must be a positive integer. The effective interval does not exceed the request duration. Python uses the entire request duration as its aggregation window. |
| `MaxConcurrentProcesses` | Maximum concurrent profiling subprocesses within one profiler execution. Default: `10`; must be a nonnegative integer. `0` means unlimited. Total operation concurrency remains subject to `Operations.MaxConcurrent`. |
| `CommandOutputLimitBytes` | Maximum profiler stdout bytes retained by the Node. Default: `65536`; must be a positive integer. When exceeded, the most recent output is retained for error diagnostics. This setting does not limit profiling result size. |
| `ToolDir` | Root directory for external profiling tools. The built-in default is `""`; `/opt/huatuo/tools` is an inactive commented example. Passed to the profiler's `--tool-path`. |

Java uses `bin/asprof` and `lib/libasyncProfiler.so` under `ToolDir`; Python uses `py-spy`. Native profiling does not require these external tools. Only tools required by the requested language are checked. Missing tools cause the request to be rejected without creating an operation.

`Operations` and `Profiling` settings are read at service startup. Changes made through the API require restarting `huatuo-bamai` to take effect.

### 6. Data storage

#### 6.1 Elasticsearch/OpenSearch

```toml
# Storage configuration
[Storage]
    # Elasticsearch and OpenSearch Storage
    #
    # Store the tracing and events data of linux kernel to ES/OS.
    #
    # - Address
    # Port 9200 is commonly used for Elasticsearch/OpenSearch HTTP APIs.
    # e.g.
    # http://127.0.0.1:9200
    # https://127.0.0.1:9200
    #
    # Address, Username, and Password must be either all empty (disabled) or
    # all configured (enabled). Partial connection settings are invalid.
    #
    # - Index
    # Elasticsearch or OpenSearch index, a logical namespace that holds a collection of
    # documents for huatuo-bamai.
    # Default: huatuo_bamai
    #
    # - Username
    # - Password
    # There are no default connection credentials.
    #
    [Storage.Elasticsearch]
        # Address = "http://127.0.0.1:9200"
        # Index = "huatuo_bamai"
        # Username = "elastic"
        # Password = "REPLACE_WITH_PASSWORD"
```

| Field | Description |
| --- | --- |
| `Address` | Elasticsearch/OpenSearch service address, supporting HTTP/HTTPS. Empty by default. This backend is disabled when `Address`, `Username`, and `Password` are all empty. To enable it, configure all three; a partial configuration prevents startup. |
| `Index` | Index for tracing and event data. Default: `huatuo_bamai`. |
| `Username` | Basic Auth username. Empty by default. |
| `Password` | Basic Auth password. Empty by default. Use a strong password and enable HTTPS in production. |

#### 6.2 Local files

```toml
# LocalFile Storage
#
# Store data to local directory for troubleshooting on the host machine.
#
# - Path
# The directory for storing data. If the Path is empty, LocalFile will be disabled.
# Default: "huatuo-local"
#
# - RotationSizeMiB
# The maximum size in MiB of a record file before it gets rotated
# for per linux kernel tracer.
# Default: 100 MiB
#
# - MaxRotatedFiles
# The maximum number of old log files to retain for per tracer.
# Default: 10
#
[Storage.LocalFile]
    # Path = "huatuo-local"
    # RotationSizeMiB = 100
    # MaxRotatedFiles = 10
```

| Field | Description |
| --- | --- |
| `Path` | Local data directory. Default: `huatuo-local`. An empty string disables local file storage. Use an absolute path when enabled. |
| `RotationSizeMiB` | Rotation size for each tracer's record file, in MiB. Default: `100`. |
| `MaxRotatedFiles` | Maximum historical files retained per tracer. Default: `10`. The oldest files are deleted when this limit is exceeded. |

### 7. Automatic tracing

Automatic tracing triggers performance collection based on resource usage and configured thresholds.

`AutoTracing.CPUIdle.Tool` and `AutoTracing.CPUSys.Tool` independently accept `perf` or `profiler`, defaulting to `perf`. Empty strings and other values are invalid. Updates affect the next collection; running collections retain their selected tool.

- `perf`: preserves inline event `flamedata`; visualization does not require huatuo-apiserver.
- `profiler`: uses native CPU on-CPU collection, stores profiles separately in Elasticsearch, and displays them through the huatuo-apiserver Pyroscope-compatible interface. Enable bamai Elasticsearch storage and do not use `--disable-storage`. Events retain trigger metrics with `flamedata: null`.

Both tools use `RunTracingToolTimeout` as the collection duration in seconds. Failures do not switch tools.

To view profiler results, open the Continuous Profiling host or container dashboard for the target. Set Task ID to the event `tracer_id`, set `type` to `process_cpu:cpu:nanoseconds:cpu:nanoseconds`, and select a time range covering collection. See [View the Flame Graph](/docs/best-practice/continuous-profiling_en.md#5-view-the-flame-graph) for dashboard access and interactions.

#### 7.1 Container CPU spikes

```toml
# cpuidle
#
# For a high cpu usage all of a sudden in containers.
#
# - UserThreshold
# User CPU usage must exceed this threshold and its increase must exceed
# DeltaUserThreshold.
# Default: 75%
#
# - SysThreshold
# System CPU usage must exceed this threshold and its increase must exceed
# DeltaSysThreshold.
# Default: 45%
#
# - UsageThreshold
# Total CPU usage (system + user) must exceed this threshold and
# its increase must exceed DeltaUsageThreshold.
# Default: 90%
#
# - DeltaUserThreshold
# User CPU usage increase from the previous sample, in percentage points.
# Default: 45 percentage points
#
# - DeltaSysThreshold
# System CPU usage increase from the previous sample, in percentage points.
# Default: 20 percentage points
#
# - DeltaUsageThreshold
# Total CPU usage increase from the previous sample, in percentage points.
# Default: 55 percentage points
#
# - Interval
# The sample interval of the cpu usage for all containers.
# Default: 10s
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing to prevent
# damage to the system.
# Default: 1800s
#
# - RunTracingToolTimeout
# The executing time of this tracing program.
# Default: 10s
#
# NOTE:
# Running this performance tool, when:
# 1. UserThreshold and DeltaUserThreshold are true, or
# 2. SysThreshold and DeltaSysThreshold are true, or
# 3. UsageThreshold and DeltaUsageThreshold
#
[AutoTracing.CPUIdle]
    # Tool: perf (default) or profiler. Both use RunTracingToolTimeout.
    # Tool = "perf"
    # UserThreshold = 75
    # SysThreshold = 45
    # UsageThreshold = 90
    # DeltaUserThreshold = 45
    # DeltaSysThreshold = 20
    # DeltaUsageThreshold = 55
    # Interval = 10
    # IntervalTracing = 1800
    # RunTracingToolTimeout = 10
```

| Field | Description |
| --- | --- |
| `UserThreshold` | User-mode CPU utilization threshold. Default: `75%`. |
| `SysThreshold` | System-mode CPU utilization threshold. Default: `45%`. |
| `UsageThreshold` | Total CPU utilization threshold. Default: `90%`. |
| `DeltaUserThreshold` | User-mode CPU utilization increase over the previous sample. Default: `45` percentage points. |
| `DeltaSysThreshold` | System-mode CPU utilization increase over the previous sample. Default: `20` percentage points. |
| `DeltaUsageThreshold` | Total CPU utilization increase over the previous sample. Default: `55` percentage points. |
| `Interval` | Container CPU utilization sampling interval in seconds. Default: `10`. |
| `IntervalTracing` | Minimum interval between automatic traces, in seconds. Default: `1800` (30 minutes). |
| `RunTracingToolTimeout` | Collection duration in seconds. Default: `10`. |

**Trigger conditions**: Once the tracing interval has elapsed, a container becomes a tracing candidate if both utilization and its increase strictly exceed the corresponding thresholds in any one of these pairs:

- `UserThreshold` and `DeltaUserThreshold`;
- `SysThreshold` and `DeltaSysThreshold`;
- `UsageThreshold` and `DeltaUsageThreshold`.

**Container filtering**: Use the `Included` and `Excluded` rule arrays to control the monitoring scope.

```toml
# CPUIdle-specific filtering rules (array form, supports multiple rules)
# Use [[double-bracket]] syntax to define each rule entry.
#
# Supported fields:
# - container_host_namespace: namespace label of the container
# - container_hostname: hostname of the container
# - container_qos: qos level (guaranteed, burstable, besteffort)
#
# Pattern uses regex matching.
#
# Logic:
# - No rules: all containers are monitored
# - Excluded only: blacklist, matched containers are skipped
# - Included only: whitelist, only matched containers are monitored
# - Both: containers must match Included AND not match Excluded
#
# Examples:
# # Blacklist: ignore besteffort containers
# [[AutoTracing.CPUIdle.Filter.Excluded]]
#     Field = "container_qos"
#     Pattern = "besteffort"
#
# # Whitelist: only monitor application namespaces
# [[AutoTracing.CPUIdle.Filter.Included]]
#     Field = "container_host_namespace"
#     Pattern = "^application-"
#
# # Combine: monitor application namespaces except kube-system
# [[AutoTracing.CPUIdle.Filter.Included]]
#     Field = "container_host_namespace"
#     Pattern = ".*"
# [[AutoTracing.CPUIdle.Filter.Excluded]]
#     Field = "container_host_namespace"
#     Pattern = "^kube-system$"
```

| Field | Description |
| --- | --- |
| `Filter` | Container filtering rules. No rules are configured by default, so all containers are monitored. Define rules using TOML arrays of tables (`[[...]]`), each containing a target field `Field` and a regular expression `Pattern`. |

- `Excluded` only: Exclude matching containers.
- `Included` only: Monitor only matching containers.
- Both: Monitor containers that match `Included` and do not match `Excluded`.

#### 7.2 Host system CPU spikes

```toml
# cpusys
#
# For a high system cpu usage all of a sudden on host machine.
#
# - SysThreshold
# System CPU usage must exceed this threshold and its increase must exceed
# DeltaSysThreshold.
# Default: 45%
#
# - DeltaSysThreshold
# System CPU usage increase from the previous sample, in percentage points.
# Default: 20 percentage points
#
# - Interval
# The sample interval of the cpu usage for host machine.
# Default: 10s
#
# - IntervalTracing
# Minimum time between profiling runs.
# Default: 1800s
#
# - RunTracingToolTimeout
# The executing time of this tracing program.
# Default: 10s
#
# NOTE:
# Running this performance tool, when:
# SysThreshold and DeltaSysThreshold are true.
#
[AutoTracing.CPUSys]
    # Tool: perf (default) or profiler. Both use RunTracingToolTimeout.
    # Tool = "perf"
    # SysThreshold = 45
    # DeltaSysThreshold = 20
    # Interval = 10
    # IntervalTracing = 1800
    # RunTracingToolTimeout = 10
```

| Field | Description |
| --- | --- |
| `SysThreshold` | Host system-mode CPU utilization threshold. Default: `45%`. |
| `DeltaSysThreshold` | System-mode CPU utilization increase over the previous sample. Default: `20` percentage points. |
| `Interval` | Host CPU utilization sampling interval in seconds. Default: `10`. |
| `IntervalTracing` | Minimum interval between traces, in seconds. Default: `1800`. |
| `RunTracingToolTimeout` | Collection duration in seconds. Default: `10`. |

**Trigger conditions**: Once the tracing interval has elapsed, tracing starts when system-mode CPU utilization strictly exceeds `SysThreshold` and its increase strictly exceeds `DeltaSysThreshold`.

#### 7.3 Container D-state load

This tracer requires the cgroup v1 load statistics interface and is skipped on cgroup v2.

```toml
# dload
#
# linux tasks D state profiling for containers.
#
# - ThresholdLoad
# D-state load average threshold for each container. Profiling is triggered
# when D-state load exceeds this value and IntervalTracing has elapsed.
# Default: 5
#
# - Interval
# The sample interval of the load for all containers.
# Default: 10s
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing to prevent
# damage to the system.
# Default: 1800s
#
# - EnableDebug
# Bypass ThresholdLoad and IntervalTracing; include task stacks regardless
# of task state. For debugging only.
# Default: false
#
[AutoTracing.Dload]
    # ThresholdLoad = 5
    # Interval = 10
    # IntervalTracing = 1800
    # EnableDebug = false
```

| Field | Description |
| --- | --- |
| `ThresholdLoad` | Load average threshold for container tasks in D state (uninterruptible sleep). Default: `5`. Tracing starts when D-state load strictly exceeds this value and `IntervalTracing` has elapsed. The decision does not use total load, which includes runnable tasks. |
| `Interval` | Container load sampling interval in seconds. Default: `10`. |
| `IntervalTracing` | Minimum interval between automatic traces, in seconds. Default: `1800` (30 minutes). |
| `EnableDebug` | Debug switch. Default: `false`. Bypasses load threshold and minimum tracing interval checks and attempts to collect stacks for tasks in all states in the container and on the host. May increase overhead; use only for debugging. |

#### 7.4 Container I/O performance

```toml
# iotracing
#
# io profiling for containers.
#
# - WbpsThreshold
# NVMe write throughput threshold in MiB/s. Both throughput and utilization
# must exceed their thresholds for two consecutive samples.
# Default: 1500 MiB/s
#
# - RbpsThreshold
# NVMe read throughput threshold in MiB/s. Both throughput and utilization
# must exceed their thresholds for two consecutive samples.
# Default: 2000 MiB/s
#
# - UtilThreshold
# Disk busy time percentage. Two consecutive samples must exceed this value.
# NVMe devices also require RbpsThreshold or WbpsThreshold to be exceeded.
# Default: 90%
#
# - AwaitThreshold
# Average read or write IO wait time in ms. Two consecutive samples exceeding
# this threshold trigger tracing independently of utilization and throughput.
# Default: 100ms
#
# - RunTracingToolTimeout
# The executing time of this tracing tool.
# Default: 10s
#
# - MaxProcDump
# The number of processes displayed by iotracing tool.
# Default: 10
#
# - MaxFilesPerProcDump
# The number of files per process displayed by iotracing tool.
# Default: 5
#
[AutoTracing.IOTracing]
    # WbpsThreshold = 1500
    # RbpsThreshold = 2000
    # UtilThreshold = 90
    # AwaitThreshold = 100
    # RunTracingToolTimeout = 10
    # MaxProcDump = 10
    # MaxFilesPerProcDump = 5
```

| Field | Description |
| --- | --- |
| `WbpsThreshold` | NVMe write throughput threshold in MiB/s. Default: `1500`. Write throughput and disk utilization must both exceed their thresholds for two consecutive samples. |
| `RbpsThreshold` | NVMe read throughput threshold in MiB/s. Default: `2000`. Read throughput and disk utilization must both exceed their thresholds for two consecutive samples. |
| `UtilThreshold` | Disk busy-time percentage threshold. Default: `90`. Non-NVMe devices trigger tracing after two consecutive samples exceed it; NVMe devices must also meet the read or write throughput condition. |
| `AwaitThreshold` | Average read or write I/O wait time threshold in ms. Default: `100`. Read wait or write wait exceeding this threshold for two consecutive samples triggers tracing independently of utilization and throughput. |
| `RunTracingToolTimeout` | I/O tracing tool execution timeout in seconds. Default: `10`. |
| `MaxProcDump` | Maximum processes shown in I/O tracing results. Default: `10`. |
| `MaxFilesPerProcDump` | Maximum files shown per process. Default: `5`. |

#### 7.5 Host anonymous memory spikes

This module detects host anonymous memory spikes, records process information ranked by RSS, and attempts runtime memory snapshots. Anonymous memory is the sum of `Active(anon)` and `Inactive(anon)` in `/proc/meminfo`. Once the history window is full, both the growth ratio and the share of total memory must reach their thresholds, and the tracing interval must have elapsed, before a report is emitted.

```toml
# memory burst
#
# Detect host anonymous-memory bursts and capture snapshots of top RSS processes.
#
# - Interval
# The sample interval of Active(anon) + Inactive(anon) from /proc/meminfo.
# Default: 10s
#
# - DeltaMemoryBurst
# Anonymous-memory growth relative to the oldest sample in the full history
# window. 100% means doubling, e.g., from 200 MiB to 400 MiB.
# Default: 100%
#
# - DeltaAnonThreshold
# Active(anon) + Inactive(anon) as a percentage of host MemTotal.
# Both this threshold and DeltaMemoryBurst must be reached to report a burst.
# Default: 70%
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing
# to prevent damage to the system.
# Default: 1800s
#
# - DumpProcessMaxNum
# Maximum RSS-ranked processes retained in the event.
# Runtime snapshots follow this ranking, capped at 3 processes.
# Default: 10
#
[AutoTracing.MemoryBurst]
    # DeltaMemoryBurst = 100
    # DeltaAnonThreshold = 70
    # Interval = 10
    # IntervalTracing = 1800
    # DumpProcessMaxNum = 10
```

| Field | Description |
| --- | --- |
| `DeltaMemoryBurst` | Anonymous memory growth threshold relative to the oldest sample in the history window. Default: `100%`, meaning at least a doubling, for example from 200 MiB to 400 MiB. |
| `DeltaAnonThreshold` | Anonymous memory as a percentage of host `MemTotal`. Default: `70%`; range: `0–100`. This is a share of total memory, not a growth ratio. |
| `Interval` | Host anonymous memory sampling interval in seconds. Default: `10`. |
| `IntervalTracing` | Minimum interval between memory burst traces, in seconds. Default: `1800` (30 minutes). |
| `DumpProcessMaxNum` | Maximum processes retained in descending RSS order. Default: `10`. Runtime snapshots are attempted for the first `min(DumpProcessMaxNum, 3)` processes; values above `3` only increase the ranking size. |

For snapshot support, capture budgets, and output truncation, see [Automatic Tracing Fields](/docs/reference/autotracing_en.md#host-anonymous-memory).

#### 7.6 Container memory threshold snapshots

Attempts to capture a Go, HotSpot, or CPython runtime snapshot when container memory meets the trigger conditions. Enabled by default; add `memory_threshold_snapshot` to the global `BlackList` and restart the `huatuo-bamai` service to disable it. Runtime support is experimental and requires validation on the target version. Capture is not guaranteed to finish before OOM.

```toml
# Capture Go/Java/Python snapshots under container memory pressure.
# Add memory_threshold_snapshot to the global BlackList and restart to disable it.
# v1 uses a usage threshold; v2 checks usage after native high/max events.
# Registration and hard-limit changes also trigger a usage check.
# memory.high is never changed; v2 cannot notify at arbitrary percentages.
# IntervalTracing is the node-wide minimum interval in seconds after each
# successful or failed capture attempt.
# RunTracingToolTimeout is the capture timeout in seconds for all runtimes.
# Timeouts cannot interrupt synchronous syscalls; capture may delay shutdown.
[AutoTracing.MemoryThresholdSnapshot]
    # ThresholdPercent = 90
    # IntervalTracing = 300
    # RunTracingToolTimeout = 2
    # MaxMemoryObjectEntries = 10
```

| Field | Description |
| --- | --- |
| `ThresholdPercent` | Container memory usage as a percentage of its limit. Default: `90`; range: `1–100`. Trigger behavior depends on the cgroup version, as described below. |
| `IntervalTracing` | Node-wide minimum interval after a successful or failed capture attempt finishes, in seconds. Default: `300`; must be positive. |
| `RunTracingToolTimeout` | Runtime capture timeout in seconds. Default: `2`; must be positive. Cancellation is cooperative, excludes runtime identification and storage time, and cannot interrupt synchronous system calls. It is not an upper bound on the entire operation. |
| `MaxMemoryObjectEntries` | Maximum ranked memory object entries retained per snapshot. Default: `10`; range: `1–100`. Final JSON output is limited to 512 KiB and is trimmed if it exceeds this limit. |

**Trigger conditions**: cgroup v1 registers a memory threshold corresponding to the configured percentage. cgroup v2 checks the current usage-to-limit ratio after `high` or `max` events; it does not set `memory.high` and cannot guarantee notification at exactly the configured percentage. Both versions check usage at registration and when the hard limit changes.

**Requirements**: Linux, memory cgroups, an enabled Pod manager, kubelet metadata, host PID/procfs/cgroup views, kernel BTF, and permission to use BPF and access target process memory. `--procfs-prefix` does not redirect process memory reads for snapshots.

Monitoring covers running regular containers and restartable init sidecars. Each container requires its own memory cgroup. Up to 4096 containers can be monitored. Registration failures and invalidated watches are not retried automatically; check the logs, fix the cause, and restart the `huatuo-bamai` service.

#### 7.7 Interrupt utilization anomalies

This module detects per-CPU irq+softirq utilization anomalies and invokes `irqtracing` to collect softirq source and victim call stacks. IRQ results use profiling storage, which requires Elasticsearch. An operation succeeds only after its window is queryable and its publication marker is saved. LocalFile alone cannot store these results.

```toml
# irqtracing
#
# Detect per-cpu irq+softirq utilization spikes. When enough cpus rise at
# the same time (see Rules), an eBPF program is attached for
# RunTracingToolTimeout seconds to capture the softirq source/victim stacks
# and a flame graph.
#
# - Interval
# The sample interval of /proc/stat per-cpu irq+softirq utilization.
# Default: 2s
#
# - RunTracingToolTimeout
# The executing time of this tracing program.
# Default: 3s
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing to prevent
# damage to the system.
# Default: 300s
#
# - MaxEventsPerSecond
# Maximum source and victim stack samples collected per second on the
# traced CPU. The budget is divided between softirq raise and entry probes.
# Default: 1000
#
# - Rules
# Trigger conditions. Evaluated in order; the first match wins.
#
# Spike rule: fires when >= MinCPUs cpus simultaneously rise by >=
# DeltaUsageThreshold percentage points and >= RelativeIncreaseThreshold%
# relative to the previous sample.
#
# Sustained rule: fires when a single CPU's irq+softirq util stays >=
# UsageThreshold for SustainedIntervals consecutive samples.
#
[AutoTracing.IRQTracing]
    # Interval = 2
    # RunTracingToolTimeout = 3
    # IntervalTracing = 300
    # MaxEventsPerSecond = 1000
    # MinCPUs = 3
    # DeltaUsageThreshold = 20
    # RelativeIncreaseThreshold = 30
    # SustainedIntervals = 10
    # UsageThreshold = 80
```

| Field | Description |
| --- | --- |
| `Interval` | Per-CPU irq+softirq utilization sampling interval in seconds, using `/proc/stat`. Default: `2`. |
| `RunTracingToolTimeout` | Duration of one `irqtracing` capture in seconds. Default: `3`. |
| `IntervalTracing` | Minimum interval between triggers, in seconds. Default: `300`. |
| `MaxEventsPerSecond` | Combined limit on source and victim stack samples per second across the traced CPUs. Default: `1000`; range: `2–8589934590`. The quota is divided as evenly as possible between `softirq_raise` and `softirq_entry`, with `500` samples per second each by default. |
| `MinCPUs`, `DeltaUsageThreshold`, and `RelativeIncreaseThreshold` | Control the rule for irq+softirq utilization spikes across multiple CPUs. The thresholds specify the increase in percentage points and the percentage increase relative to the previous sample. |
| `SustainedIntervals` and `UsageThreshold` | Set the consecutive sample count and utilization threshold for sustained high irq+softirq utilization on a single CPU. |

#### 7.8 SchedBlame CPU Contention Attribution

SchedBlame estimates external CPU contention for selected container CPU
cgroups and emits `sched-blame-external` events when the current one-second
ratio exceeds the target's adaptive historical threshold.

```toml
[AutoTracing.SchedBlame]
    # TargetContainerScope = "normal"
    # TargetQos = []
    # HighlightContainer = ""
    # SliceDropPercent = 0
    # ExternalAnomalyK = 1.0
    # SliceBatchSize = 128
    # PerfEventPerCPUBufferBytes = 524288
    # PerfEventWatermarkBytes = 8192
    # PerfEventQueueRecords = 8192
    # PollIntervalMs = 100
    # ExternalRatioDebugFile = ""
```

- **TargetContainerScope**: `normal` selects normal application containers;
  `all` selects every discovered container. Default `normal`.
- **TargetQos**: Optional candidate QoS filter. Values are case-insensitive
  `guaranteed`, `burstable`, or `besteffort`; an empty list accepts all QoS
  classes. Default `[]`.
- **HighlightContainer**: Optional hostname, full container ID, or unique ID
  prefix for one target's debug log, ratio CSV, and SuperMonitor comparison.
  Default empty.
- **SliceDropPercent**: Percentage of completed scheduler slices discarded
  before transport. Valid range `0..99`; default `0`.
- **ExternalAnomalyK**: Nonnegative multiplier applied to the target's
  historical P99 external-contention ratio. Default `1.0`.
- **SliceBatchSize**: Maximum slices per perf record. Valid range `1..128`;
  default `128`.
- **PerfEventPerCPUBufferBytes**: Requested perf-ring data capacity per CPU.
  Default `524288`.
- **PerfEventWatermarkBytes**: Positive reader wakeup watermark, smaller than
  the effective per-CPU ring capacity. Default `8192`.
- **PerfEventQueueRecords**: Shared decoded-record queue capacity. Default
  `8192`.
- **PollIntervalMs**: Interval for bounded queue draining and evaluation
  checks. Valid range `1..1000`; default `100`.
- **ExternalRatioDebugFile**: Optional CSV path for per-target one-second
  ratio diagnostics. Default empty. See [SchedBlame Analysis](../best-practice/sched-blame-analysis_en.md)
  for interactive charts and SuperMonitor comparisons.

#### 7.9 Known issue labeling

```toml
# Autotracing configuration.
#
# - IssuesList
# Known issue filters for autotracing.
#
[AutoTracing]
    IssuesList = []
```

| Field | Description |
| --- | --- |
| `IssuesList` | Known issue filter in the form `[["issue name", "regex"], ...]`. Matching captured stacks are labeled with the corresponding issue name. Default: `[]`. Currently used by dload tracing. Example: `IssuesList = [["known_issue1", "softlockup"], ["known_issue2", "alloc_pages.*failed"]]`. |

### 8. Event tracing

This section configures collection conditions for scheduling, memory reclaim, network, and hardware events.

#### 8.1 Scheduler tick intervals

```toml
# scheduler tick interval
#
# Trace long scheduler tick intervals.
#
# - IntervalThreshold
# When the scheduler tick interval reaches the threshold, huatuo-bamai
# will collect kernel context.
# Default: 10000000 in nanoseconds, 10ms
#
[EventTracing.SchedTick]
    # IntervalThreshold = 10000000
```

| Field | Description |
| --- | --- |
| `IntervalThreshold` | Scheduler tick interval threshold in nanoseconds. Default: `10000000` (10 ms). An event is collected when the threshold is reached. An unusually long tick interval can indicate a CPU stall, but does not by itself prove that softirqs were disabled. |

#### 8.2 Memory reclaim blocking

```toml
# memreclaim
#
# The memory reclaim may block the process, if one process is blocked
# for a long time, reporting the events to userspace.
#
# - BlockedThreshold
# The blocked time when memory reclaiming.
# Default: 900000000ns, 900ms
#
[EventTracing.MemoryReclaim]
    # BlockedThreshold = 900000000
```

| Field | Description |
| --- | --- |
| `BlockedThreshold` | Memory reclaim blocking threshold in nanoseconds. Default: `900000000` (900 ms). An event is reported with context when a process is blocked by memory reclaim for longer than this threshold. |

#### 8.3 Network receive latency

```toml
# networking rx latency
#
# linux net stack rx latency for every tcp skbs.
#
# - Driver2NetRx
# The latency from driver to net rx, e.g., netif_receive_skb.
# Default: 5ms
#
# - Driver2TCP
# The latency from driver to tcp rx, e.g., tcp_v4_rcv.
# Default: 10ms
#
# - Driver2Userspace
# The latency from driver to userspace copy data, e.g., skb_copy_datagram_iovec.
# Default: 115ms
#
# - ExcludedContainerQos
# Blacklist: skip containers whose qos level matches any entry in this list.
# Values: "guaranteed", "burstable", "besteffort" (case-insensitive).
# Default: [].
#
# - ExcludedHostNetnamespace
# Don't care the skbs, packets in the host net namespace.
# Default: true
#
[EventTracing.NetRxLatency]
    # Driver2NetRx = 5
    # Driver2TCP = 10
    # Driver2Userspace = 115
    # ExcludedContainerQos = []
    ExcludedContainerQos = ["besteffort"]
    # ExcludedHostNetnamespace = true
```

| Field | Description |
| --- | --- |
| `Driver2NetRx` | Latency threshold from the driver to network-layer reception, in milliseconds. Default: `5`, for example the latency to `netif_receive_skb`. |
| `Driver2TCP` | Latency threshold from the driver to TCP stack reception, in milliseconds. Default: `10`, for example the latency to `tcp_v4_rcv`. |
| `Driver2Userspace` | Latency threshold from the driver to the userspace data copy, in milliseconds. Default: `115`, for example the latency to `skb_copy_datagram_iovec`. |
| `ExcludedContainerQos` | Container QoS classes to exclude. The built-in default is `[]`; the configuration file explicitly sets `["besteffort"]`. Supports `guaranteed`, `burstable`, and `besteffort`, case-insensitively. |
| `ExcludedHostNetnamespace` | Whether to exclude packet latency in the host network namespace. Default: `true`. |

#### 8.4 Network interface events

```toml
# netdev events
#
# monitor the net device events.
#
# - DeviceList
# Full-match regex patterns for the net devices we take care of.
# Literal names such as "eth0" keep exact-match behavior.
# Default: [] is empty, meaning no devices.
#
[EventTracing.Netdev]
    DeviceList = ["eth0", "eth1", "bond4", "lo"]
```

| Field | Description |
| --- | --- |
| `DeviceList` | Full-match regular expressions for interfaces whose events are monitored. The built-in default is `[]`, meaning no devices are monitored. The configuration file explicitly sets `["eth0", "eth1", "bond4", "lo"]`. Literal names such as `"eth0"` match exactly; patterns such as `"bond[0-9]+"` can match multiple interfaces. |

#### 8.5 Packet drops

```toml
# dropwatch
#
# monitor packets dropped events in the Linux kernel.
#

# NOT supported (go-pcap limitation):
# icmp, icmp6, igmp, vrrp, ah, esp, pim and other raw IP protocol keywords
# CIDR prefix length notation (e.g. /24) is not supported for subnet masks;
# use dotted-decimal notation instead (e.g. mask 255.255.255.0)
[EventTracing.Dropwatch]
    Filter = "tcp"
```

| Field | Description |
| --- | --- |
| `Filter` | Tcpdump-style filter expression for the standalone `dropwatch` tracer. Default: `"tcp"`. TCP retransmission correlation uses `TCPRetransmit.Filter`. |
| `MaxEventsPerSecond` | Maximum dropwatch events emitted by BPF per second. Default: `100`; `0` means unlimited. |
| `ExcludeContainers` | Reserved container exclusion list. Default: `[]`; currently has no effect. Use `EventTracing.IssuesList` to suppress drop events by call stack. |

#### 8.6 TCP retransmissions

```toml
# TCP retransmission tracing through tcpshark --mode retransmit. When local
# dropwatch correlation is enabled, the embedded collector uses the exact
# EventTracing.TCPRetransmit.Filter expression and does not emit raw drops.
# Tail Loss Probe collection is disabled by default.
# MaxEventsPerSecond limits events emitted by the BPF program; 0 disables
# rate limiting.
[EventTracing.TCPRetransmit]
    # Retransmission filter. Local correlation applies the same expression
    # to both inputs. Empty defaults to "tcp" in local mode and passes no
    # --filter flag when correlation is disabled.
    # Filter = ""
    # EnableTLP = false
    # EnableDropwatch = false
    # MaxEventsPerSecond = 100
```

| Field | Description |
| --- | --- |
| `EnableTLP` | Whether to collect `tcp_send_loss_probe` events. Default: `false`. |
| `Filter` | TCP retransmission filter expression. With local drop correlation enabled, both inputs use the same normalized expression; an empty value falls back to `tcp`. With correlation disabled, an empty value omits `--filter`. `Dropwatch.Filter` controls only the standalone dropwatch tracer. |
| `EnableDropwatch` | Whether to correlate retransmissions and drops within tcpshark. Default: `false`. Remove `tcp_retransmit` from `BlackList`; the standalone `dropwatch` tracer may remain disabled. Correlation uses the same `Filter` and does not emit raw drop events separately. |
| `MaxEventsPerSecond` | Maximum TCP retransmission events emitted by BPF per second. Default: `100`; `0` means unlimited. In correlation mode, embedded dropwatch uses the same value with an independent rate limit, so each input is limited to `100` events per second by default. Exceeding the limit produces a `rate limit hit` log. |

#### 8.7 Hardware errors (RAS)

```toml
# ras
#
# Hardware error event tracing (RAS: Reliability, Availability, Serviceability).
# Captures MCE, EDAC, ACPI/GHES, PCIe AER, and MCE threshold (THR) events via eBPF.
#
# - MceThrBackoff
# Minimum interval in seconds between consecutive MCE threshold (THR) event saves.
# THR events are fired by the local-APIC threshold interrupt and can storm at high
# frequency; this cooldown prevents flooding storage with redundant records.
# Default: 1800s (30 minutes)
#
[EventTracing.Ras]
    # MceThrBackoff = 1800
```

| Field | Description |
| --- | --- |
| `MceThrBackoff` | Minimum storage interval for MCE threshold interrupt (THR) events, in seconds. Default: `1800` (30 minutes). THR is triggered by a CPU local APIC threshold interrupt, and corrected hardware errors can produce many events. Shorter intervals increase record volume and storage load. |

#### 8.8 Moore Threads GPU XID events

```toml
# Mthreads GPU event tracing
#
# Groups all event-tracing knobs that target the Mthreads / MUSA
# GPU stack. New GPU-side tracers should be added under this
# section.
#
# - MthreadsXidLevel
# Minimum XID severity level that the mthreads_xid tracer will
# report. Anything strictly below this threshold is silently
# dropped. The tracer polls /proc/driver/musa/gpu<idx>/event_report
# every 1s. Valid values:
#   ""        — disabled; no event_report polling is processed
#   "notify"  — report all severities (most verbose)
#   "warning" — drop notify-level events
#   "fatal"   — only report fatal events (quietest)
# Default: "" (disabled)
#
[EventTracing.MthreadsGPU]
    # Example severity; leaving it commented keeps XID reporting disabled.
    # MthreadsXidLevel = "warning"
```

| Field | Description |
| --- | --- |
| `MthreadsXidLevel` | Minimum severity for reporting XID events. Options: `""` (disabled), `"notify"`, `"warning"`, and `"fatal"`. Events below the selected level are filtered out. The built-in default is `""`; the commented `"warning"` value is only an example. |

To enable XID reporting, install the MUSA driver, remove `mthreads_xid` from the global `BlackList`, and set `MthreadsXidLevel` to a valid nonempty level. The tracer polls `/proc/driver/musa/gpu*/event_report` every second. Events include UUID, XID ID, severity, scope, PCI BDF, process ID, and additional context.

#### 8.9 Known issue suppression

```toml
# Linux kernel event tracing configuration.
#
# - IssuesList
# Known issue filters for event tracing.
#
[EventTracing]
    IssuesList = []
```

| Field | Description |
| --- | --- |
| `IssuesList` | Known issue suppression rules in the form `[["name", "regex"], ...]`. Default: `[]`. For `net_rx_latency`, the expression matches the generated event title; for `dropwatch`, it matches the kernel call stack joined with newlines. Matching events are discarded. Names identify rules only and are not written to saved events. Example: `IssuesList = [["ignored_process", "comm=ignored_process"], ["neighbor_cleanup", "neigh_invalidate/"]]`. |

### 9. Metric collection

This section configures system, network, and device metrics. `Included` and `Excluded` use regular expressions with the following rules:

- No rules: Collect all items.
- `Excluded` only: Exclude matching items.
- `Included` only: Collect only matching items.
- Both: Collect items that match `Included` and do not match `Excluded`.

#### 9.1 Basic network interface statistics

```toml
# Netdev statistic
#
# - EnableNetlink
# Use netlink instead of procfs net/dev to get netdev statistic.
# Query the target host or container network namespace.
# Default is "false".
#
# - DeviceIncluded
# Accept special devices in netdev statistic.
# Default: "" (empty), meaning include all.
#
# - DeviceExcluded
# Exclude special devices in netdev statistic.
# Default: "" (empty), meaning exclude nothing.
#
# Filter logic (applies to all Included/Excluded below):
# - No rules: all items are collected
# - Excluded only: blacklist, matched items are skipped
# - Included only: whitelist, only matched items are collected
# - Both: items must match Included AND not match Excluded
#
[MetricCollector.NetdevStats]
    # EnableNetlink = false
    # DeviceIncluded = ""
    DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

| Field | Description |
| --- | --- |
| `EnableNetlink` | Whether to read network interface statistics through Netlink instead of procfs. Default: `false`. Netlink queries the target host or container network namespace and requires permission to access it. |
| `DeviceIncluded` | Regular expression for interface names to include. Default: `""` (no inclusion restriction). |
| `DeviceExcluded` | Regular expression for interfaces to exclude. The built-in default is `""` (exclude nothing). The configuration file explicitly uses the expression above to exclude matching lo, docker, and veth interfaces. |

#### 9.2 Priority flow control (PFC)

```toml
# netdev dcb, DCB (Data Center Bridging)
#
# Collecting the DCB PFC (Priority-based Flow Control).
#
# - DeviceList
# Full-match regex patterns for the net devices we take care of.
# Literal names such as "eth0" keep exact-match behavior.
# Default: [] is empty, meaning no devices.
#
[MetricCollector.NetdevDCB]
    DeviceList = ["eth0", "eth1"]
```

| Field | Description |
| --- | --- |
| `DeviceList` | Full-match regular expressions for interfaces whose DCB PFC information is collected. The built-in default is `[]` (no devices); the configuration file explicitly sets `["eth0", "eth1"]`. |

#### 9.3 Network interface hardware statistics

```toml
# netdev hardware statistic
#
# Collecting the hardware statistic of net devices, e.g, rx_dropped.
#
# - DeviceList
# Full-match regex patterns for the net devices we take care of.
# Literal names such as "eth0" keep exact-match behavior.
# Default: [] is empty, meaning no devices.
#
[MetricCollector.NetdevHW]
    DeviceList = ["eth0", "eth1"]
```

| Field | Description |
| --- | --- |
| `DeviceList` | Full-match regular expressions for interfaces whose hardware statistics, such as rx_dropped, are collected. The built-in default is `[]` (no devices); the configuration file explicitly sets `["eth0", "eth1"]`. |

#### 9.4 Network queue statistics (Qdisc)

```toml
# Qdisc
#
# - DeviceIncluded / DeviceExcluded
# Same as above.
#
[MetricCollector.Qdisc]
    # DeviceIncluded = ""
    DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

| Field | Description |
| --- | --- |
| `DeviceIncluded` / `DeviceExcluded` | Interface filtering expressions. Both default to `""`, imposing no restrictions. The configuration file explicitly sets `DeviceExcluded` to the expression above. See the filtering rules at the start of this section. |

#### 9.5 Virtual memory statistics (vmstat)

```toml
# vmstat
#
# This metric supports host vmstat and cgroup vmstat.
# - IncludedOnHost / ExcludedOnHost: same as above, for host /proc/vmstat.
# - IncludedOnContainer / ExcludedOnContainer: same, for cgroup containers memory.stat.
#
[MetricCollector.Vmstat]
    IncludedOnHost = "allocstall|nr_active_anon|nr_active_file|nr_boost_pages|nr_dirty|nr_free_pages|nr_inactive_anon|nr_inactive_file|nr_kswapd_boost|nr_mlock|nr_shmem|nr_slab_reclaimable|nr_slab_unreclaimable|nr_unevictable|nr_writeback|numa_pages_migrated|pgdeactivate|pgrefill|pgscan_direct|pgscan_kswapd|pgsteal_direct|pgsteal_kswapd"
    ExcludedOnHost = "total"
    IncludedOnContainer = "active_anon|active_file|dirty|inactive_anon|inactive_file|pgdeactivate|pgrefill|pgscan_direct|pgscan_kswapd|pgsteal_direct|pgsteal_kswapd|shmem|unevictable|writeback|pgscan_globaldirect|pgscan_globalkswapd|pgscan_cswapd|pgsteal_cswapd|pgsteal_globaldirect|pgsteal_globalkswapd"
    ExcludedOnContainer = "total"
```

| Field | Description |
| --- | --- |
| `IncludedOnHost` / `ExcludedOnHost` | Field filtering expressions for host `/proc/vmstat`. Both default to `""`. The configuration file explicitly sets the inclusion list above and excludes fields matching `total`. |
| `IncludedOnContainer` / `ExcludedOnContainer` | Field filtering expressions for container cgroup `memory.stat`. Both default to `""`. The configuration file explicitly sets the inclusion list above and excludes fields matching `total`. |

#### 9.6 Memory events

```toml
# MemoryEvents/Netstat/MountPointStat
#
# - Included / Excluded: Same as above.
# - MountPointsIncluded: whitelist only (no Excluded), same logic.
#
[MetricCollector.MemoryEvents]
    Included = "watermark_inc|watermark_dec"
    # Excluded = ""
```

| Field | Description |
| --- | --- |
| `Included` / `Excluded` | Memory event field filtering expressions. Both default to `""`. The configuration file explicitly sets `Included` to select fields matching `watermark_inc` or `watermark_dec`. |

#### 9.7 Network protocol statistics

```toml
[MetricCollector.Netstat]
    # Excluded = ""
    # Included = ""
```

| Field | Description |
| --- | --- |
| `Included` / `Excluded` | Network protocol statistics field filtering expressions. Both default to `""`, imposing no restrictions. The configuration file does not explicitly set them. |

#### 9.8 Mount point statistics

```toml
# MountPointStat
[MetricCollector.MountPointStat]
    MountPointsIncluded = "(^/home$)|(^/$)|(^/boot$)"
```

| Field | Description |
| --- | --- |
| `MountPointsIncluded` | Mount point path filtering expression. The built-in default is `""` (no restriction). The configuration file explicitly uses the expression above to collect only `/`, `/home`, and `/boot`. |

#### 9.9 Moore Threads GPU

```toml
# Moore Threads (MTT) GPU fine-grained toggles
#
# - EnableHealth
# Enable health metrics: temperature, power, voltage, utilization, memory,
# clocks, fans, pstate, VPU. Default: true
#
# - EnablePCIe
# Enable PCIe link metrics: current/max speed and width, replay counter.
# Default: false
#
# - EnableMTLink
# Enable MtLink interconnect metrics: per-link state and bandwidth.
# Default: false
#
[MetricCollector.Mthreads]
    # EnableHealth = true
    # EnablePCIe = false
    # EnableMTLink = false
```

| Field | Description |
| --- | --- |
| `EnableHealth` | Health metrics switch. Default: `true`. Includes temperature, power, voltage, utilization, memory capacity, clocks, fans, performance state, and VPU metrics. |
| `EnablePCIe` | PCIe link metrics switch. Default: `false`. Includes current and maximum link speed and width, and replay counters. |
| `EnableMTLink` | MtLink interconnect metrics switch. Default: `false`. Includes per-link bandwidth, link count, and link state. |

**Requirements**: Remove `mthreads_gpu` from the global `BlackList`, install `libmtml.so.2` or `libmtml.so` where the system dynamic linker can find it, and restart the `huatuo-bamai` service. A missing library at startup disables the collector. Restart the `huatuo-bamai` service after installing the library or changing `LD_LIBRARY_PATH`.

**Configuration updates**: All three switches read the latest configuration on each collection. Changes made through the Node API take effect on the next collection.

#### 9.10 Ascend NPU

Remove `ascend_npu` from the global `BlackList` and restart the `huatuo-bamai` service to enable the collector. The node requires an available Ascend NPU and `/usr/local/dcmi/libdcmi.so`. Device discovery still requires DCMI initialization when `EnableDCMI` is disabled.

```toml
# Ascend NPU fine-grained toggles
# Remove ascend_npu from BlackList and restart to enable the collector.
# DCMI initialization is required even when EnableDCMI is false.
#
# - EnableDCMI
# Enable DCMI metrics: health, power, temp, voltage, util, freq,
# network health, HBM, and ECC info. Default: true
#
# - EnablePCIe
# Enable PCIe link metrics: capability and status (speed/width).
# Default: false
#
# - EnableHCCN
# Enable HCCN network metrics via hccn_tool subprocesses.
# Default: false
#
[MetricCollector.AscendNPU]
    # EnableDCMI = true
    # EnablePCIe = false
    # EnableHCCN = false
```

| Field | Description |
| --- | --- |
| `EnableDCMI` | Boolean. Default: `true`. Collects health, power, temperature, voltage, utilization, frequency, network health, HBM, and ECC metrics. |
| `EnablePCIe` | Boolean. Default: `false`. Collects maximum and current PCIe link speed and width. |
| `EnableHCCN` | Boolean. Default: `false`. Collects network link, traffic, counter, and optical module metrics through `/usr/local/Ascend/driver/tools/hccn_tool` subprocesses. Ensure the tool is available before enabling it. |

These switches read the latest configuration on each collection. Changes made through the Node API take effect on the next collection.

### 10. Pod and container information

This section configures the ports and credentials used to retrieve Pod information from kubelet and associate container and Pod labels.

```toml
# Pod Configuration
#
# Configure these parameters for fetching pods from kubelet.
#
# - KubeletReadOnlyPort
# The KubeletReadOnlyPort is kubelet read-only port for the Kubelet to serve on with
# no authentication/authorization. The port number must be between 1 and 65535, inclusive.
# Setting this field to 0 disables fetching pods from kubelet read-only service.
# Default: 10255
#
# - KubeletAuthorizedPort
# The port is the HTTPs port of the kubelet. The port number must be between 1 and 65535,
# inclusive. Setting this field to 0 disables fetching pods from kubelet HTTPS port.
# Default: 10250
#
# - KubeletClientCertPath
# https://kubernetes.io/docs/setup/best-practices/certificates/
#
# Client certificate and private key file name. One file or two files:
# "/path/to/xxx-kubelet-client.crt,/path/to/xxx-kubelet-client.key",
# "/path/to/kubelet-client-current.pem"
# Default: "". The assignment below supplies explicit deployment paths.
#
# You can disable this kubelet fetching pods, for bare metal service, by
# KubeletReadOnlyPort = 0, and KubeletAuthorizedPort = 0.
#
[Pod]
    KubeletClientCertPath = "/etc/kubernetes/pki/apiserver-kubelet-client.crt,/etc/kubernetes/pki/apiserver-kubelet-client.key"
```

| Field | Description |
| --- | --- |
| `KubeletReadOnlyPort` | Kubelet read-only port for retrieving the Pod list without authentication. Default: `10255`; port range: `1–65535`. Set to `0` to disable this method. |
| `KubeletAuthorizedPort` | Kubelet HTTPS port for retrieving Pod information using certificate authentication. Default: `10250`; port range: `1–65535`. Set to `0` to disable this method. Recommended for production. |
| `KubeletClientCertPath` | Kubelet client certificate and private key paths. The built-in default is `""`. Accepts two comma-separated paths or one PEM file containing both. The configuration file explicitly uses the paths under `/etc/kubernetes/pki/` shown above; verify them for your deployment. In non-Kubernetes environments, set both kubelet ports to `0` to disable Pod discovery. |

### 11. Command-line options

`huatuo-bamai` supports these command-line options:

```bash
huatuo-bamai --region <region> [options]
```

| Option | Description | Default |
|------|------|--------|
| `--config` | Configuration file name | `huatuo-bamai.conf` |
| `--config-dir` | Configuration directory | `conf` |
| `--bpf-dir` | BPF object directory | `bpf` |
| `--tools-bin-dir` | Tracing tool binary directory | `bin` |
| `--region` | Deployment region (required) | - |
| `--disable-kubelet` | Disable Pod discovery through kubelet | `false` |
| `--disable-storage` | Disable storage backends | `false` |
| `--enable-cgroup` | Enable resource limits in Huatuo's own cgroup (disabled by default) | `false` |
| `--disable-tracing` | Disable a specified tracer; may be repeated | - |
| `--log-debug` | Force the log level to Debug | `false` |
| `--dry-run` | Test loading only; exit gracefully after startup | `false` |
| `--procfs-prefix` | Procfs mount prefix | - |

### 12. Override rules

Explicit configuration assignments override built-in defaults; comments are not loaded. Command-line options apply according to their own semantics rather than universally overriding the configuration file.

- **Log level**: `--log-debug` forces `Debug`. Otherwise, `Log.Level` is used, with `Info` as the default when omitted.
- **Tracing blacklist**: `--disable-tracing` is merged with the configuration file's `BlackList`.
- **Independent switches**: `--disable-kubelet` and `--disable-storage` disable Pod synchronization and storage backends, respectively. They have no same-named TOML fields, and enabling them does not bypass configuration validation.

### 13. Configuration recommendations

- **Resource control**: Use Pod resource limits in Kubernetes and service resource limits in systemd. For direct execution without an external manager, use `--enable-cgroup` and `[Runtime]`.
- **Storage**: Prefer LocalFile for local troubleshooting in small deployments. For large clusters, use Elasticsearch for centralized storage and queries.
- **Automatic tracing**: Tune thresholds for the workload. Low thresholds trigger frequent traces; high thresholds may miss problems. Validate changes incrementally in a test environment.
- **Security**: Use a strong password for Elasticsearch and consider enabling HTTPS. Avoid hard-coding sensitive information in configuration files.
- **Compatibility**: Check the required kernel capabilities, hardware, and runtime dependencies before enabling a feature.
