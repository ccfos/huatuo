---
title: huatuo-bamai Configuration
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 4
---

### 1. Overview

`huatuo-bamai` is the core collector of HUATUO (a BPF-based metrics and anomaly inspector). Its configuration file defines the data collection scope, probe enablement strategy, metric output format, anomaly detection rules, and logging behavior.

The configuration file uses **TOML** format and includes multiple sections such as global blacklist, logging, runtime resource limits, storage configuration, and AutoTracing. Each configuration item comes with detailed comments explaining its purpose, default value, and important notes. This document provides a clear and detailed English explanation for **every configuration item** to help users understand and safely customize the settings.

**Note**: Most parameters are provided as commented defaults (prefixed with `#`). Uncomment and adjust as needed. Changes take effect after restarting `huatuo-bamai`. In production, avoid enabling high-overhead features unnecessarily.

### 2. Global Blacklist

```bash
# Global tracing and metrics configuration.
#
# - BlackList
# Global blacklist for tracing and metrics.
#
BlackList = ["netdev_hw", "netdev_qdisc", "metax_gpu", "ascend_npu", "diskio", "tcp_retransmit", "mthreads_gpu"]
```

- **BlackList**: Global blacklist for tracing and metrics.

  Modules or hardware to exclude from tracing and metric collection. The default is `["netdev_hw", "netdev_qdisc", "metax_gpu", "ascend_npu", "diskio", "tcp_retransmit", "mthreads_gpu"]`, which disables tracing and metrics for the network device hardware layer, qdisc statistics, Metax GPU, Ascend NPU, procfs-based disk I/O statistics, TCP retransmission tracing, and Moore Threads GPU. Remove `diskio` to enable disk I/O metrics, `tcp_retransmit` to enable TCP retransmission tracing and its drop-correlation cache, or `mthreads_gpu` to enable Moore Threads GPU metric collection on hosts with MT GPUs. Supports arrays; extend as needed.

### 3. Logging

```bash
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

- **Level**: Log verbosity. Values: Debug, Info, Warn, Error, Panic. Default: Info. Use Info or Warn in production; Debug for troubleshooting.

- **File**: Log file path.

  Specifies the path to the log file. If left empty, logs are not written to any file (output goes to stdout or system logs).

  Default: empty.

  **Description**: In containerized deployments, configure a specific path and integrate with a log collection system for persistence.

### 4. Runtime Resource Limits

Huatuo does not create its own cgroup by default. This section applies only when `--enable-cgroup` is passed; Kubernetes and systemd deployments should use their native resource controls.

```bash
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

- **StartupCPULimitCores** limits CPU usage during initialization. Default:
  `0.5` cores.
- **CPULimitCores** limits CPU usage after startup. Default: `2.0` cores.
- **MemoryLimitMiB** limits process memory. Default: `2048` MiB.

The configured values remain in their documented units. Memory is converted
to bytes only when the cgroup limit is applied.

### 5. HTTP Server and Tasks

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
    # balancers and proxies that would otherwise time out idle connections. If
    # writing the ping fails three consecutive times, the server closes the
    # connection.
    # Default: 30
    #
    # ListenAddress = ":19704"
    # MaxEventStreamClients = 100
    # EventStreamKeepAliveIntervalSeconds = 30

# Locally running tracing tasks.
[Tasks]
    # - MaxConcurrent
    # Maximum number of concurrent tasks.
    # Default: 10
    #
    # MaxConcurrent = 10
```

- **ListenAddress** uses `host:port` form. An empty host listens on all
  interfaces.
- **MaxConcurrent** limits locally running tracing tasks.

The event stream settings control `POST /v1/events/watch`. When
`MaxEventStreamClients` is reached, new streams receive HTTP 429.
`EventStreamKeepAliveIntervalSeconds` controls SSE heartbeat comments used to
keep proxy and load-balancer connections alive. After three consecutive write
failures, the server closes the stream. Set the interval below any upstream
idle timeout; 15–60 seconds is typical.

### 6. Storage

#### 6.1 Elasticsearch and OpenSearch Storage

```bash
# Storage configuration
[Storage]
    # Elasticsearch and OpenSearch Storage
    #
    # Disable ES/OS storage if one of Address, Username, Password is empty.
    # Store the tracing and events data of linux kernel to ES/OS.
    #
    # - Address
    # Port 9200 is commonly used for Elasticsearch/OpenSearch HTTP APIs.
    # e.g.
    # http://127.0.0.1:9200
    # https://127.0.0.1:9200
    #
    # - Index
    # Elasticsearch or OpenSearch index, a logical namespace that holds a collection of
    # documents for huatuo-bamai.
    # Default: huatuo_bamai
    #
    # - Username
    # - Password
    # Address, Username, and Password must be either all empty (disabled) or
    # all configured (enabled). Partial connection settings are invalid.
    #
    [Storage.Elasticsearch]
        # Address = "https://127.0.0.1:9200"
        # Index = "huatuo_bamai"
        # Username = "elastic"
        # Password = "REPLACE_WITH_PASSWORD"
        # InsecureSkipVerify = false
        # CABundle = "/etc/huatuo/es-ca.pem"
```

- **Address**: ElasticSearch/OpenSearch service address.

  No default value.

  **Description**: Used to store kernel tracing and event data. ES/OS storage
  is disabled when Address, Username, and Password are all empty. All three
  values are required when storage is enabled; a partial configuration
  prevents startup.

- **Index**: Index name.

  Default: huatuo_bamai.

  **Description**: Logical namespace for organizing huatuo-bamai tracing and event documents.

- **Username**: Authentication username.

  No default value.

  **Description**: Used for Basic Auth.

- **Password**: Authentication password.

  No default value.

  **Description**: Used together with the username. In production, use a strong password and enable TLS encryption.

**Overall**: ES/OS storage persists kernel tracing and event data for later search and analysis.

- **InsecureSkipVerify**: Disable TLS server certificate verification for
  HTTPS addresses.

  Default value is `false` (verification enabled).

  **Description**: Basic-auth credentials are sent on every request, so a
  man in the middle can capture them when verification is disabled. Opt out
  only for self-signed deployments, preferably combined with **CABundle**.

- **CABundle**: Path to PEM-encoded CA certificates used to verify the
  Elasticsearch/OpenSearch server.

  No default value.

  **Description**: Use this when the server certificate does not chain to
  the system roots, e.g. an internal CA. Ignored when
  InsecureSkipVerify is true.

#### 6.2 Local File Storage

```bash
# LocalFile Storage
#
# Store data to local directory for troubleshooting on the host machine.
#
# - Path
# The directory for storing data. If the Path is empty, LocalFile will be disabled.
# Default: "huatuo-local"
#
# - RotationSizeMiB
# The maximum size in Megabytes of a record file before it gets rotated
# per kernel tracer.
# Default: 100MB
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

- **Path**: Local data storage directory.

  Default: huatuo-local. If empty, local file storage is disabled.

  **Description**: Stores data locally on the host for on-site troubleshooting. Use an absolute path.

- **RotationSizeMiB**: Single file rotation size.

  Maximum size of a record file before rotation (per tracer).

  Default: 100 MB.

  **Description**: Prevents any single file from growing too large and consuming excessive disk space.

- **MaxRotatedFiles**: Maximum number of rotated files to retain.

  Default: 10.

  **Description**: Oldest files are automatically deleted once the limit is reached, controlling disk usage.

### 7. Automatic Tracing

The automatic tracing module is one of HUATUO’s intelligent features. It triggers specific performance tracing based on thresholds, reducing manual intervention.

#### 7.1 CPUIdle Automatic Tracing — Sudden High CPU Usage in Containers

```bash
# Autotracing configuration 
[AutoTracing]
    # cpuidle
    #
    # For sudden high CPU usage in containers.
    #
    # - UserThreshold
    # User CPU usage threshold, when cpu usage reaches this threshold, cpu
    # performance tracing will be triggered.
    # Default: 75%
    #
    # - SysThreshold
    # System CPU usage threshold, when reaching this threshold, cpu performance
    # tracing will be triggered.
    # Default: 45%
    #
    # - UsageThreshold
    # The total cpu usage (system + user cpu usage) threshold, when reaching
    # this threshold, cpu performance tracing will be triggered.
    # Default: 45%
    #
    # - DeltaUserThreshold
    # The range of this user cpu changes within a short period of time.
    # Default: 45%
    #
    # - DeltaSysThreshold
    # The range of this system cpu changes within a short period of time.
    # Default: 20%
    #
    # - DeltaUsageThreshold
    # The range of this cpu usage changes within a short period of time.
    # Default: 55%
    #
    # - Interval
    # The sample interval of the cpu usage for all containers.
    # Default: 10s
    #
    # - IntervalTracing
    # Time since last run. Avoid frequently executing this tracing to prevent
    # performance impact.
    # Default: 1800s
    #
    # - RunTracingToolTimeout
    # Execution timeout of this tracing tool (seconds).
    # Default: 10s
    # 
# NOTE:
# Profiling triggers when:
# 1. UserThreshold AND DeltaUserThreshold are exceeded, or
# 2. SysThreshold AND DeltaSysThreshold are exceeded, or
# 3. UsageThreshold AND DeltaUsageThreshold are exceeded
    #
    [AutoTracing.CPUIdle]
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

- **UserThreshold**: User-mode CPU usage threshold (%).

  Default: 75%.

- **SysThreshold**: System-mode CPU usage threshold (%).

  Default: 45%.

- **UsageThreshold**: Total CPU usage threshold (%).

  Default: 90% (as shown in comments).

- **DeltaUserThreshold**: Short-term user CPU change threshold (%).

  Default: 45%.

- **DeltaSysThreshold**: Short-term system CPU change threshold (%).

  Default: 20%.

- **DeltaUsageThreshold**: Short-term total CPU change threshold (%).

  Default: 55%.

- **Interval**: CPU usage sampling interval (seconds).

  Default: 10s.

- **IntervalTracing**: Minimum interval between runs (seconds).

  Default: 1800s (30 minutes).

- **RunTracingToolTimeout**: Single tracing execution timeout (seconds).

  Default: 10s.

**Trigger Logic**: Tracing runs when any of the following is true:

1. Both UserThreshold and DeltaUserThreshold are met, or
2. Both SysThreshold and DeltaSysThreshold are met, or
3. Both UsageThreshold and DeltaUsageThreshold are met.

**Filter Container Filtering**: Use Included/Excluded rule arrays to control monitoring scope.

```bash
    # Each rule contains Field (filter field) and Pattern (regex).
    # Field: container_host_namespace | container_hostname | container_qos
    #
    # [[AutoTracing.CPUIdle.Filter.Excluded]]
    #     Field = "container_qos"
    #     Pattern = "besteffort"
    # [[AutoTracing.CPUIdle.Filter.Included]]
    #     Field = "container_host_namespace"
    #     Pattern = "^application-"
```

- **Filter**: Container filtering rules. Defined using `[[double-bracket]]` syntax with multiple rules, each containing `Field` (filter field) and `Pattern` (regex). Filtering logic:

  - No rules: monitor all containers
  - `Excluded` only: blacklist, skip matched containers
  - `Included` only: whitelist, only monitor matched containers
  - Both: must match Included AND not match Excluded

  Default: no rules, all containers monitored.

#### 7.2 CPUSys Automatic Tracing — Sudden High System CPU on Host

```bash
# cpusys
#
# For sudden high system cpu usage on the host machine.
#
# - SysThreshold
# System CPU usage threshold, when reaching this threshold, cpu performance
# tracing will be triggered.
# Default: 45%
#
# - DeltaSysThreshold
# The range of system cpu changes within a short period of time.
# Default: 20%
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
# Execution timeout of this tracing tool (seconds).
# Default: 10s
#
# NOTE:
# Profiling triggers when:
# SysThreshold AND DeltaSysThreshold are exceeded.
#
[AutoTracing.CPUSys]
	# SysThreshold = 45
	# DeltaSysThreshold = 20
	# Interval = 10
	# IntervalTracing = 1800
	# RunTracingToolTimeout = 10
```

- **SysThreshold**: System CPU usage threshold (%).

  Default: 45%.

- **DeltaSysThreshold**: Short-term system CPU change threshold (%).

  Default: 20%.

- **Interval**: Host CPU usage sampling interval (seconds).

  Default: 10s.

- **IntervalTracing**: Minimum time between profiling runs. Default: 1800s.

- **RunTracingToolTimeout**: Tracing execution timeout (seconds).

  Default: 10s.

**Trigger Logic**: Tracing is triggered when both SysThreshold and DeltaSysThreshold are satisfied.

#### 7.3 Dload AutoTracing — D-State Task Profiling for Containers

```bash
# dload
#
# linux tasks D state profiling for containers.
#
# - ThresholdLoad
# Load average threshold. When exceeded, D-state profiling triggers.
# Default: 5
#
# - Interval
# The sample interval of the load for all containers.
# Default: 10s
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing to prevent
# performance impact.
# Default: 1800s
#
[AutoTracing.Dload]
	# ThresholdLoad = 5
	# Interval = 10
	# IntervalTracing = 1800
```

- **ThresholdLoad**: System load average (loadavg) threshold for containers.

  Default: 5. Triggers D-state (uninterruptible sleep) task profiling when loadavg reaches this value.

- **Interval**: Monitoring interval.

  Default: 10s.

- **IntervalTracing**: Minimum time between consecutive tracings.

  Default: 1800s (30 minutes).

#### 7.4 IOTracing AutoTracing — Container IO Performance Profiling

```bash
# iotracing
#
# io profiling for containers.
#
# - WbpsThreshold
# Max write bytes per second threshold. When exceeded, iotracing is triggered.
# For NVMe devices, UtilThreshold must also be met.
# Default: 1500 MB/s
#
# - RbpsThreshold
# Max read bytes per second threshold. When exceeded, iotracing is triggered.
# For NVMe devices, UtilThreshold must also be met.
# Default: 2000 MB/s
#
# - UtilThreshold
# Disk utilization (%). Consistently above 80-90% indicates a bottleneck.
# Default: 90%
#
# - AwaitThreshold
# Await (Average IO wait time in ms): High values indicate slow disk response times.
# Default: 100ms
#
# - RunTracingToolTimeout
# Execution timeout of this tracing tool (seconds).
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

- **WbpsThreshold**: Max write bytes per second threshold (MB/s).

  Default: 1500. (For NVMe, must also meet UtilThreshold.)

- **RbpsThreshold**: Max read bytes per second threshold (MB/s).

  Default: 2000.

- **UtilThreshold**: Disk utilization threshold (%).

  Default: 90%.

- **AwaitThreshold**: Average IO wait time threshold (ms).

  Default: 100ms.

- **RunIOTracingTimeout**: IO tracing tool timeout (seconds).

  Default: 10s.

- **MaxProcDump**: Maximum number of processes to display.

  Default: 10.

- **MaxFilesPerProcDump**: Maximum files per process to display.

  Default: 5.

**Description**: Used for diagnosing IO hotspots in containers, especially under high disk load.

#### 7.5 MemoryBurst AutoTracing

This module detects sudden memory usage spikes on the host and automatically captures kernel context to help diagnose memory pressure events.

```bash
# memory burst
#
# Capture kernel context on sudden host memory usage spikes.
#
# - Interval
# Memory usage sampling interval (seconds).
# Default: 10s
#
# - DeltaMemoryBurst
# Growth percentage threshold for memory usage. 100% means, e.g.,
# memory usage increased from 200MB to 400MB.
# Default: 100%
#
# - DeltaAnonThreshold
# Growth percentage threshold for anonymous memory. 100% means, e.g.,
# anon memory increased from 200MB to 400MB.
# Default: 70%
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing
# to prevent performance impact.
# Default: 1800s
#
# - DumpProcessMaxNum
# Number of processes to dump when triggered.
# Default: 10
#
[AutoTracing.MemoryBurst]
	# DeltaMemoryBurst = 100
	# DeltaAnonThreshold = 70
	# Interval = 10
	# IntervalTracing = 1800
	# SlidingWindowLength = 60
	# DumpProcessMaxNum = 10
```

- **DeltaMemoryBurst**: Memory usage burst growth percentage threshold.

  Default: 100%.

- **DeltaAnonThreshold**: Anonymous memory burst growth percentage threshold.

  Default: 70%.

- **Interval**: Memory usage sampling interval (seconds).

  Default: 10s.

- **IntervalTracing**: Minimum interval between runs (seconds).

  Default: 1800s.

- **SlidingWindowLength**: Sliding window length (seconds).

  Default: 60s.

- **DumpProcessMaxNum**: Maximum processes to dump on trigger.

  Default: 10.

#### 7.6 Known Issue Filtering (IssuesList)

```bash
# Autotracing configuration.
#
# - IssuesList
# Known issue filters for autotracing.
#
[AutoTracing]
    IssuesList = []
```

- **IssuesList**: Known issue filter. Format: `[["name", "regex"], ...]`. When a collected stack trace matches the regex, it is labeled with the issue name. Default `[]`.

  Example: `IssuesList = [["known_issue1", "softlockup"], ["known_issue2", "alloc_pages.*failed"]]`

**Note**: Only supports `dload` tracing of known issues filtering, other events are not supported.

### 8. Event Tracing

This section captures key kernel events and latency, including scheduler tick intervals, memory reclaim, network receive latency, network device events, and packet drops. It is the core module for kernel-level anomaly context collection in HUATUO.

#### 8.1 Scheduler Tick Interval Tracing

```bash
# linux kernel events capturing configuration
[EventTracing]
	# scheduler tick
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

- **IntervalThreshold**: Scheduler tick interval threshold, in nanoseconds.

  Default: 10,000,000 ns (10ms).

  **Description**: The event infers CPU stalls from long scheduler tick intervals. It does not by itself prove that softirqs were disabled.

#### 8.2 Memory Reclaim Blocking Tracing

```bash
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

- **BlockedThreshold**: Memory reclaim blocking time threshold (nanoseconds).

  Default: 900,000,000 ns (900ms). When a process is blocked by memory reclaim for longer than this time, an event is reported to userspace with context.

  **Description**: Memory reclaim blocking is a common cause of process stalls, especially in memory-constrained cloud-native environments.

#### 8.3 Network Receive Latency Tracing

```bash
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
# Blacklist: skip containers whose qos level matches.
# Values: "guaranteed", "burstable", "besteffort" (case-insensitive).
# Default: [].
#
# - ExcludedHostNetnamespace
# Exclude packets in the host network namespace.
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

- **Driver2NetRx**: Latency threshold from driver to network receive layer (e.g., netif_receive_skb).

  Default: 5ms.

- **Driver2TCP**: Latency threshold from driver to TCP receive (e.g., tcp_v4_rcv).

  Default: 10ms.

- **Driver2Userspace**: Latency threshold from driver to userspace data copy (e.g., skb_copy_datagram_iovec).

  Default: 115ms.

- **ExcludedContainerQos**: Container QoS levels to exclude (blacklist).

  Default: []. Corresponds to Kubernetes Pod QoS levels (Guaranteed, Burstable, BestEffort).

- **ExcludedHostNetnamespace**: Whether to exclude packets in the host network namespace.

  Default: true.

#### 8.4 Network Device Event Monitoring

```bash
# netdev events
#
# Monitor network device events.
#
# - DeviceList
# The net devices we monitor.
# Default: [] (empty, meaning no devices).
#
[EventTracing.Netdev]
	DeviceList = ["eth0", "eth1", "bond4", "lo"]
```

- **DeviceList**: List of network device full-match regex patterns to monitor. Literal names such as `"eth0"` keep exact-match behavior; patterns such as `"bond[0-9]+"` can select multiple devices.

  Default example includes "eth0", "eth1", "bond4", "lo". An empty list means no devices are monitored.

  **Description**: Monitors physical link status events for specified network interfaces.

#### 8.5 Packet Drop Monitoring

```toml
[EventTracing.Dropwatch]
    # tcpdump-style filter expression, forwarded to dropwatch --filter.
    # Default: "tcp"
    Filter = "tcp"

    # Forwarded to dropwatch --max-events-per-second.
    # Default: 100; 0 disables rate limiting.
    MaxEventsPerSecond = 100

    # Reserved configuration field. It is not currently consumed by the
    # dropwatch event path and therefore has no filtering effect.
    # Default: []
    ExcludeContainers = []
```

- **Filter**: tcpdump-style packet filter passed to `dropwatch --filter` and applied by the BPF program before events are emitted.

  Default: `"tcp"`.

- **MaxEventsPerSecond**: Maximum number of dropwatch events emitted by BPF per second.

  Default: `100`. Set to `0` to disable rate limiting.

- **ExcludeContainers**: Reserved container-exclusion list.

  Default: `[]`. The field exists in the configuration schema, but the current dropwatch event path does not read or forward it, so configuring it has no effect. Use `EventTracing.IssuesList` for operator-defined dropwatch call-stack suppression.

#### 8.6 TCP Retransmission Tracing ([EventTracing.TCPRetransmit])

```bash
[EventTracing.TCPRetransmit]
    # Forwarded to tcpshark --filter.
    # Applies only to tcp_retransmit_skb events.
    # Default: ""
    Filter = ""

    # Forwarded as tcpshark --enable-tlp. Default: false.
    EnableTLP = false

    # Forwarded as tcpshark --max-events-per-second.
    # Default: 100; 0 disables rate limiting.
    MaxEventsPerSecond = 100
```

- **Filter**: tcpdump-style filter expression passed to `tcpshark --filter`.

  Default: empty string. It applies only to `tcp_retransmit_skb` events.

- **EnableTLP**: Whether to collect `tcp_send_loss_probe` events.

  Default: false.

- **MaxEventsPerSecond**: Maximum TCP retransmission events emitted by BPF per second.

  Default: 100. Set to 0 for unlimited output. When the limit is exceeded, `tcpshark` logs `rate limit hit`.

#### 8.7 Hardware Error Event Tracing (EventTracing.Ras)

```bash
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

- **MceThrBackoff**: Minimum cooldown in seconds between MCE threshold (THR) event saves.

  Default: 1800s (30 minutes).

  **Description**: THR events are generated by the CPU's local-APIC threshold interrupt when correctable hardware errors accumulate. These can fire at very high frequency during hardware degradation. The backoff suppresses redundant saves while ensuring at least one record is captured per interval. Lower values provide more granular event records at the cost of higher storage throughput; in environments with frequent correctable errors, consider raising this value to reduce noise.

#### 8.8 Known Issue Filtering (IssuesList)

```bash
# Linux kernel event tracing configuration.
#
# - IssuesList
# Known issue filters for event tracing.
#
[EventTracing]
    IssuesList = []
```

- **IssuesList**: Known-issue suppression rules in the form `[["name", "regex"], ...]`. Default `[]`.

  For `net_rx_latency`, each regex is matched against the generated event title. For `dropwatch`, it is matched against the newline-joined kernel call stack. A match causes the event to be discarded; the configured name identifies the rule but is not added to the saved event.

  Example: `IssuesList = [["ignored_process", "comm=ignored_process"], ["neighbor_cleanup", "neigh_invalidate/"]]`

### 9. Metric Collector

This section defines collection rules for various system and network metrics. All `Included`/`Excluded` fields share the same filter logic (regex):

- No rules: all items are collected
- Excluded only: blacklist, matched items are skipped
- Included only: whitelist, only matched items are collected
- Both: must match Included AND not match Excluded

#### 9.1 Netdev Statistics

```bash
# Metric Collector
[MetricCollector]
	# Netdev statistic
	#
	# - EnableNetlink
	# Use netlink instead of procfs net/dev to get netdev statistic.
	# Only support the host environment to use `netlink` now.
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
	# Filter logic see MetricCollector section header.
	#
	[MetricCollector.NetdevStats]
		# EnableNetlink = false
		# DeviceIncluded = ""
		DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

- **EnableNetlink**: Use netlink instead of procfs to collect netdev statistics.

  Default: false. Currently only supported on the host.

- **DeviceIncluded**: Regex to include specific devices. Default: include all.

- **DeviceExcluded**: Regex to exclude devices. Example: "^(lo)|(docker\\w*)|(veth\\w*)$", meaning exclude loopback, docker, and veth interfaces.

#### 9.2 Netdev DCB Collection

```bash
# netdev dcb, DCB (Data Center Bridging)
#
# Collecting the DCB PFC (Priority-based Flow Control).
#
# - DeviceList
# The net devices we monitor.
# Default: [] (empty, meaning no devices).
#
[MetricCollector.NetdevDCB]
	DeviceList = ["eth0", "eth1"]
```

- **DeviceList**: List of network device full-match regex patterns for which DCB (Data Center Bridging) PFC information is collected.

  Default: empty.

#### 9.3 Netdev Hardware Statistics

```bash
# netdev hardware statistic
#
# Collecting the hardware statistic of net devices, e.g, rx_dropped.
#
# - DeviceList
# The net devices we monitor.
# Default: [] (empty, meaning no devices).
#
[MetricCollector.NetdevHW]
	DeviceList = ["eth0", "eth1"]
```

- **DeviceList**: List of network device full-match regex patterns for hardware-level statistics (e.g., rx_dropped).

  Default: empty.

#### 9.4 Qdisc Collection

```bash
# Qdisc
#
# - DeviceIncluded / DeviceExcluded
# Same as above.
#
[MetricCollector.Qdisc]
	# DeviceIncluded = ""
	DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

- **DeviceIncluded / DeviceExcluded**: Same as above.

#### 9.5 vmstat Metric Collection

```bash
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

- **IncludedOnHost / ExcludedOnHost**: Filter fields for host /proc/vmstat.

- **IncludedOnContainer / ExcludedOnContainer**: Filter fields for container cgroup memory.stat.

#### 9.6 Other Metric Collections

```bash
# MemoryEvents/Netstat/MountPointStat
#
# - Included / Excluded: same as above.
# - MountPointsIncluded: whitelist only (no Excluded), same logic.
#
[MetricCollector.MemoryEvents]
	Included = "watermark_inc|watermark_dec"
	# Excluded = ""
[MetricCollector.Netstat]
	# Excluded = ""
	# Included = ""

# MountPointStat
[MetricCollector.MountPointStat]
	MountPointsIncluded = "(^/home$)|(^/$)|(^/boot$)"
```

- **Included / Excluded**: Same as above.

- **MountPointsIncluded**: Regex for mount points to collect. Default includes /, /home, /boot.

#### 9.7 Moore Threads GPU Metrics

```bash
# MetricCollector.Mthreads
#
# Moore Threads GPU metric collection via the MTML (Moore Threads Management
# Library) shared library. The library is discovered automatically at startup
# using SONAME search (libmtml.so.2, then libmtml.so) through the system
# dynamic linker; no hardcoded path is required.
#
# Remove "mthreads_gpu" from BlackList to enable this collector.
#
# - EnableHealth
# Enable health metrics: temperature, power, utilization, clocks, fans, pstate, VPU.
# Default: true
#
# - EnablePCIe
# Enable PCIe link metrics: current speed/width and replay counter.
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

- **EnableHealth**: Controls collection of health-related metrics.

  Default: true. When enabled, collects: GPU/memory temperature (`gpu_temperature_celsius`, `memory_temperature_celsius`), power usage and limits (`device_power_watts`, `gpu_power_limit_watts`, `gpu_power_default_limit_watts`), GPU/memory utilization (`gpu_utilization_percent`, `memory_utilization_percent`), clock frequencies (`gpu_clock_mhz`, `gpu_max_clock_mhz`, `memory_clock_mhz`, `memory_max_clock_mhz`), voltage (`gpu_voltage_volts`), memory capacity (`memory_total_bytes`, `memory_used_bytes`), fan speed (`fan_rpm`, `fan_speed_percent`), performance state (`gpu_pstate`), and VPU metrics (`vpu_utilization_percent`, `vpu_encoder_utilization_percent`, `vpu_decoder_utilization_percent`, `vpu_clock_mhz`).

- **EnablePCIe**: Controls collection of PCIe link metrics.

  Default: false. When enabled, collects: current PCIe link speed and width (`pcie_link_speed_gt_per_sec`, `pcie_link_width_lanes`), max capability (`pcie_link_max_speed_gt_per_sec`, `pcie_link_max_width_lanes`), and replay counter (`pcie_replay_total`).

- **EnableMTLink**: Controls collection of MtLink interconnect metrics.

  Default: false. When enabled, collects: device-level static specs (per-link bandwidth `mtlink_link_bandwidth_gb_s` and link count `mtlink_link_count`) and per-link state (`mtlink_state`).

**Library discovery**: At startup, the collector searches for `libmtml.so.2` then `libmtml.so` via the system dynamic linker (respecting `LD_LIBRARY_PATH` and `/etc/ld.so.cache`). If no library is found, the collector logs a warning and is disabled for the lifetime of the process. Library discovery is performed only at startup: changing `LD_LIBRARY_PATH` or installing a new MTML version also requires a restart.

**Hot-reload semantics**: `EnableHealth`, `EnablePCIe`, and `EnableMTLink` are read from the latest config snapshot on every scrape, so toggling them takes effect on the next Prometheus scrape without restarting `huatuo-bamai`. A `false → true → false` transition emits and suppresses the corresponding metric groups on the next scrape after each change.

Note: enabling the collector itself (i.e. removing `mthreads_gpu` from `BlackList` after the process has already started with the collector disabled because `libmtml.so` was missing at startup) requires a restart. The collector factory runs only during initialization, so a successful late library load will not register a new collector.

### 10. Pod

This section configures how to fetch Pod information from kubelet to enable container/Pod-level labeling and metric isolation.

```bash
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
#
# You can disable this kubelet fetching pods, for bare metal service, by
# KubeletReadOnlyPort = 0, and KubeletAuthorizedPort = 0.
#
[Pod]
	KubeletClientCertPath = "/etc/kubernetes/pki/apiserver-kubelet-client.crt,/etc/kubernetes/pki/apiserver-kubelet-client.key"
```

- **KubeletReadOnlyPort**: Kubelet read-only port.

  Default: 10255. Set to 0 to disable this method.

- **KubeletAuthorizedPort**: Kubelet HTTPS authorized port.

  Default: 10250. Set to 0 to disable.

- **KubeletClientCertPath**: Path to kubelet client certificate and private key. Supports comma-separated files or single PEM file.

  **Description**: Used for mTLS authentication on the HTTPS port. In non-Kubernetes (bare-metal) environments, set both ports to 0 to disable Pod fetching.

### 11. CLI Flags

`huatuo-bamai` supports the following command-line flags:

```bash
huatuo-bamai --region <region> [options]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Configuration file name | `huatuo-bamai.conf` |
| `--config-dir` | Configuration file directory | `conf` |
| `--bpf-dir` | BPF object file directory | `bpf` |
| `--tools-bin-dir` | Tracing tool binary directory | `bin` |
| `--region` | Deployment region (required) | - |
| `--disable-kubelet` | Disable kubelet Pod fetching | `false` |
| `--disable-storage` | Disable storage backends | `false` |
| `--enable-cgroup` | Enable self cgroup resource limits (disabled by default) | `false` |
| `--disable-tracing` | Disable specified tracing modules (may be repeated) | - |
| `--log-debug` | Force log level to Debug | `false` |
| `--dry-run` | Load-only test; exit gracefully after startup | `false` |
| `--procfs-prefix` | procfs mount point prefix | - |

### 12. Configuration Override Precedence

When the same configuration item is set in both command-line flags and the configuration file, the following precedence applies:

**CLI flag > Configuration file > Built-in default**

Specific rules:

1. **Log level**: `--log-debug` > config file `[Log] Level` > built-in default `Info`
   - `--log-debug` has the highest priority and forces the log level to `Debug` regardless of the `Level` value in the configuration file.
   - An explicit `Level` in the configuration file overrides the built-in default.
   - If neither is set, the default `Info` is used.

2. **Tracing blacklist**: `--disable-tracing` is merged with the configuration file `BlackList` (they complement each other rather than override).

3. **Other boolean switches** (`--disable-kubelet`, `--disable-storage`): When explicitly set on the command line, they override the configuration file.

### 13. Best Practices and Important Notes

- **Resource Control**: Kubernetes uses Pod resources and systemd uses service limits.
  Use `--enable-cgroup` and `[Runtime]` only for direct execution without an
  external manager.
- **Storage Choice**: For small-scale deployments, prefer [Storage.LocalFile] for local troubleshooting. For large clusters, configure Elasticsearch for centralized storage and querying.
- **AutoTracing Tuning**: Adjust thresholds based on workload characteristics. Thresholds that are too low cause frequent triggering; thresholds that are too high may miss issues. Validate gradually in a test environment.
- **Security**: Use strong passwords for ES configuration and consider enabling HTTPS. Avoid hard-coding sensitive information in the configuration file.
- **Compatibility**: Configuration parameters may be affected by kernel version and hardware environment. Always verify with the official HUATUO documentation for your specific setup.

By properly configuring huatuo-bamai.conf, you can fully leverage HUATUO’s capabilities in kernel-level anomaly detection and intelligent tracing, significantly improving observability and troubleshooting efficiency in cloud-native systems.

If you need deeper customization for a specific scenario, feel free to provide more details about your environment.
