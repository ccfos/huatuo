---
title: Configuration Guide
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 4
---

### 1. Document Overview

`huatuo-bamai` is the core collector of HUATUO (a BPF-based metrics and anomaly inspector). Its configuration file defines the data collection scope, probe enablement strategy, metric output format, anomaly detection rules, and logging behavior.

The configuration file uses **TOML** format and includes multiple sections such as global blacklist, logging, runtime resource limits, storage configuration, and AutoTracing. Each configuration item comes with detailed comments explaining its purpose, default value, and important notes. This document provides a clear and detailed English explanation for **every configuration item** to help users understand and safely customize the settings.

**Note**: Most parameters in the configuration file are provided as commented defaults (starting with `#`). To enable them, remove the `#` and adjust the values according to your environment. Changes take effect only after restarting the `huatuo-bamai` process. In production environments, follow the principle of minimalism and avoid enabling high-overhead features unnecessarily.

### 2. Global Blacklist

```bash
# The global blacklist for tracing and metrics
BlackList = ["netdev_hw", "metax_gpu"]
```

- **BlackList**: Global blacklist for tracing and metrics.

  Used to exclude specific modules or hardware from tracing and metric collection, preventing irrelevant noise or high-overhead probes. The default value is ["netdev_hw", "metax_gpu"], which globally disables tracing and metrics related to network device hardware layer (netdev_hw) and Metax GPU.

  **Description**: Adding items to the blacklist can effectively reduce resource consumption, especially in specific hardware environments. It supports array format and can be extended based on actual business needs.

### 3. Logging

```bash
# Log Configuration
#
# - Level
# The log level for huatuo-bamai: Debug, Info, Warn, Error, Panic.
# Default: Info
#
# - File
# Store logs to where the logging file is. If it is empty, don't write log
# to any file.
# Default: empty
#
[Log]
    # Level = "Info"
    # File = ""
```

- **Level**: Log level.

  Default: Info. Possible values: Debug, Info, Warn, Error, Panic.

  **Description**: Controls the verbosity of huatuo-bamai logs. In production, Info or Warn is recommended to reduce log volume. Use Debug only for troubleshooting, as it generates substantial output.

- **File**: Log file path.

  Specifies the path to the log file. If left empty, logs are not written to any file (output goes to stdout or system logs).

  Default: empty.

  **Description**: In containerized deployments, configure a specific path and integrate with a log collection system for persistence.

### 4. Runtime Resource Limits

```bash
# Runtime resource limit
#
# - LimitInitCPU
# During the huatuo-bamai startup, the CPU of process are restricted from use.
# Default is 0.5 CPU.
#
# - LimitCPU
# The CPU resource restricted once the process starts.
# Default is 2.0 CPU.
#
# - LimitMem
# The memory resource limitted for huatuo-bamai process.
# Default is 2048MB.
#
[RuntimeCgroup]
    # LimitInitCPU = 0.5
    # LimitCPU = 2.0
    # LimitMem = 2048
```

- **LimitInitCPU**: CPU limit during startup phase.

  Restricts CPU cores usable by the huatuo-bamai process during initialization.

  Default: 0.5 CPU.

  **Description**: Prevents excessive CPU usage during startup from affecting host business workloads. Value is in CPU cores (supports decimals).

- **LimitCPU**: Runtime CPU limit.

  Restricts CPU resources after the process has started.

  Default: 2.0 CPU.

  **Description**: Adjust based on node scale and workload. In high-density container environments, lower this value appropriately to ensure business stability.

- **LimitMem**: Memory resource limit.

  Maximum memory allowed for the huatuo-bamai process.

  Default: 2048 MB.

  **Description**: Enforced via cgroup to prevent OOM (Out Of Memory) issues. In production, increase as needed according to collection scale.

### 5. Storage

#### 5.1 Elasticsearch Storage

```bash
# Storage configuration
[Storage]
    # Elasticsearch Storage
    #
    # Disable ES storage if one of Address, Username, Password is empty.
    # Store the tracing and events data of linux kernel to ES.
    #
    # - Address
    # Default address is :9200 of localhost. ...
    #
    # - Index
    # Elasticsearch index, a logical namespace that holds a collection of
    # documents for huatuo-bamai.
    # Default: huatuo_bamai
    #
    # - Username
    # - Password
    # There is no default username and password.
    #
    [Storage.ES]
        # Address = "http://127.0.0.1:9200"
        # Index = "huatuo_bamai"
        Username = "elastic"
        Password = "huatuo-bamai"
```

- **Address**: Elasticsearch service address.

  Default: http://127.0.0.1:9200.

  **Description**: Used to store kernel tracing and event data. ES storage is disabled if any of Address, Username, or Password is empty. Port 9200 is the standard HTTP API port for Elasticsearch.

- **Index**: Elasticsearch index name.

  Default: huatuo_bamai.

  **Description**: Logical namespace for organizing huatuo-bamai tracing and event documents.

- **Username**: ES authentication username.

  No default value (example uses elastic).

  **Description**: Used for Basic Auth.

- **Password**: ES authentication password.

  No default value (example uses huatuo-bamai).

  **Description**: Used together with the username. In production, use a strong password and enable TLS encryption.

**Overall**: ES storage persists kernel tracing and event data for later search and analysis.

#### 5.2 Local File Storage

```bash
# LocalFile Storage
#
# Store data to local directory for troubleshooting on the host machine.
#
# - Path
# The directory for storing data. If the Path is empty, LocalFile will be disabled.
# Default: "huatuo-local"
#
# - RotationSize
# The maximum size in Megabytes of a record file before it gets rotated
# for per linux kernel tracer.
# Default: 100MB
#
# - MaxRotation
# The maximum number of old log files to retain for per tracer.
# Default: 10
#
[Storage.LocalFile]
    # Path = "huatuo-local"
    # RotationSize = 100
    # MaxRotation = 10
```

- **Path**: Local data storage directory.

  Default: huatuo-local. If empty, local file storage is disabled.

  **Description**: Stores data locally on the host for on-site troubleshooting. Use an absolute path.

- **RotationSize**: Single file rotation size.

  Maximum size of a record file before rotation (per tracer).

  Default: 100 MB.

  **Description**: Prevents any single file from growing too large and consuming excessive disk space.

- **MaxRotation**: Maximum number of rotated files to retain.

  Default: 10.

  **Description**: Oldest files are automatically deleted once the limit is reached, controlling disk usage.

### 6. Automatic Tracing

The automatic tracing module is one of HUATUO’s intelligent features. It triggers specific performance tracing based on thresholds, reducing manual intervention.

#### 6.1 CPUIdle Automatic Tracing — Sudden High CPU Usage in Containers

```bash
# Autotracing configuration 
[AutoTracing]
    # cpuidle
    #
    # For a high cpu usage all of a sudden in containers.
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

**Filter Container Filtering**: Use Include/Exclude rule arrays to control monitoring scope.

```bash
    # Each rule contains Field (filter field) and Pattern (regex).
    # Field: container_host_namespace | container_hostname | container_qos
    #
    # [[AutoTracing.CPUIdle.Filter.Exclude]]
    #     Field = "container_qos"
    #     Pattern = "besteffort"
    # [[AutoTracing.CPUIdle.Filter.Include]]
    #     Field = "container_host_namespace"
    #     Pattern = "^application-"
```

- **Filter**: Container filtering rules. Defined using `[[double-bracket]]` syntax with multiple rules, each containing `Field` (filter field) and `Pattern` (regex). Filtering logic:

  - No rules: monitor all containers
  - `Exclude` only: blacklist, skip matched containers
  - `Include` only: whitelist, only monitor matched containers
  - Both: must match Include AND not match Exclude

  Default: no rules, all containers monitored.

#### 6.2 CPUSys Automatic Tracing — Sudden High System CPU on Host

```bash
# cpusys
#
# For a high system cpu usage all of a sudden on host machine.
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
# - RunTracingToolTimeout
# The executing time of this tracing program.
# Default: 10s
#
# NOTE:
# Running this performance tool, when:
# SysThreshold and DeltaSysThreshold are true.
#
[AutoTracing.CPUSys]
	# SysThreshold = 45
	# DeltaSysThreshold = 20
	# Interval = 10
	# RunTracingToolTimeout = 10
```

- **SysThreshold**: System CPU usage threshold (%).

  Default: 45%.

- **DeltaSysThreshold**: Short-term system CPU change threshold (%).

  Default: 20%.

- **Interval**: Host CPU usage sampling interval (seconds).

  Default: 10s.

- **RunTracingToolTimeout**: Tracing execution timeout (seconds).

  Default: 10s.

**Trigger Logic**: Tracing is triggered when both SysThreshold and DeltaSysThreshold are satisfied.

#### 6.3 Dload AutoTracing — D-State Task Profiling for Containers

```bash
# dload
#
# linux tasks D state profiling for containers.
#
# - ThresholdLoad
# The loadavg threshold value, when reaching this threshold, dload profiling
# is triggered.
# Defalut: 5
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

#### 6.4 IOTracing AutoTracing — Container IO Performance Profiling

```bash
# iotracing
#
# io profiling for containers.
#
# - WbpsThreshold
# Max write bytes per second, when reaching this threshold, iotracing is triggered.
# Please note that if it is an NVMe device, it must also meet the UtilThreshold.
# Default: 1500 MB/s
#
# - RbpsThreshold
# Max read bytes per second, when reaching this threshold, iotracing is triggered.
# Please note that if it is an NVMe device, it must also meet the UtilThreshold.
# Default: 2000 MB/s
#
# - UtilThreshold
# Disk utilization, Percentage of time the disk is busy. If this is consistently
# above 80-90%, the disk may be a bottleneck.
# Default: 90%
#
# - AwaitThreshold
# Await (Average IO wait time in ms): High values indicate slow disk response times.
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

#### 6.5 MemoryBurst AutoTracing

This module detects sudden memory usage spikes on the host and automatically captures kernel context to help diagnose memory pressure events.

```bash
# memory burst
#
# If there is a memory used burst on the host, capture this kernel context.
#
# - Interval
# The sample interval of the memory used.
# Default: 10s
#
# - DeltaMemoryBurst
# A certain percentage of memory burst used. 100% that means, e.g.,
# memory used increased from 200MB to 400MB.
# Default: 100%
#
# - DeltaAnonThreshold
# A certain percentage of anon memory burst used. 100% that means, e.g.,
# anon memory used increased from 200MB to 400MB.
# Default: 70%
#
# - IntervalTracing
# Time since last run. Avoid frequently executing this tracing
# to prevent damage to the system.
# Default: 1800s
#
# - DumpProcessMaxNum
# How many processes to dump when this event is triggered.
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

#### 6.6 Known Issue Filtering (IssuesList)

```bash
# IssuesList for known issue filtering in autotracing
IssuesList = []
```

- **IssuesList**: Known issue filter. Format: `[["name", "regex"], ...]`. When a collected stack trace matches the regex, it is labeled with the issue name. Default `[]`.

  Example: `IssuesList = [["known_issue1", "softlockup"], ["known_issue2", "alloc_pages.*failed"]]`

**Note**: Current only supports `dload` tracing of known issues filtering, other events are not supported.

### 7. Event Tracing

This section is responsible for capturing key kernel events and monitoring latency, including softirq, memory reclaim, network receive latency, network device events, and packet drop monitoring. It is the core module for kernel-level anomaly context collection in HUATUO.

#### 7.1 Softirq Disable Tracing

```bash
# linux kernel events capturing configuration
[EventTracing]
	# softirq
	#
	# tracing the softirq disabled events of linux kernel.
	#
	# - DisabledThreshold
	# When the disable duration of softirq exceeds the threshold, huatuo-bamai
	# will collect kernel context.
	# Default: 10000000 in nanoseconds, 10ms
	#
	[EventTracing.Softirq]
		# DisabledThreshold = 10000000
```

- **DisabledThreshold**: Softirq disable duration threshold (nanoseconds).

  Default: 10,000,000 ns (10ms). When softirq is disabled longer than this threshold, kernel context is collected.

  **Description**: Long softirq disable periods can cause delays in networking, timers, etc. Useful for diagnosing interrupt storms or high-load scenarios.

#### 7.2 Memory Reclaim Blocking Tracing

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

#### 7.3 Network Receive Latency Tracing

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
# Don't care the containers which qos level is in ExcludedContainerQos.
# This is a string slice in vendor/k8s.io/api/core/v1/types.go
# - PodQOSGuaranteed = "Guaranteed"
# - PodQOSBurstable = "Burstable"
# - PodQOSBestEffort = "BestEffort"
#
# Default: []
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
	ExcludedContainerQos = ["bestEffort"]
	# ExcludedHostNetnamespace = true
```

- **Driver2NetRx**: Latency threshold from driver to network receive layer (e.g., netif_receive_skb).

  Default: 5ms.

- **Driver2TCP**: Latency threshold from driver to TCP receive (e.g., tcp_v4_rcv).

  Default: 10ms.

- **Driver2Userspace**: Latency threshold from driver to userspace data copy (e.g., skb_copy_datagram_iovec).

  Default: 115ms.

- **ExcludedContainerQos**: List of container QoS levels to exclude from monitoring.

  Default: []. Corresponds to Kubernetes Pod QoS levels (Guaranteed, Burstable, BestEffort).

- **ExcludedHostNetnamespace**: Whether to exclude packets in the host network namespace.

  Default: true.

#### 7.4 Network Device Event Monitoring

```bash
# netdev events
#
# monitor the net device events.
#
# - DeviceList
# The net devices we take care of.
# Default: [] is empty, meaning no devices.
#
[EventTracing.Netdev]
	DeviceList = ["eth0", "eth1", "bond4", "lo"]
```

- **DeviceList**: List of network devices to monitor.

  Default example includes "eth0", "eth1", "bond4", "lo". An empty list means no devices are monitored.

  **Description**: Monitors physical link status events for specified network interfaces.

#### 7.5 Packet Drop Monitoring

```bash
# dropwatch
#
# monitor packets dropped events in the Linux kernel.
#
# - ExcludedNeighInvalidate
# Don't care of neigh_invalidate drop events.
# Default: true
#
[EventTracing.Dropwatch]
	# ExcludedNeighInvalidate = true
```

- **ExcludedNeighInvalidate**: Whether to exclude packet drops caused by neigh_invalidate.

  Default: true.

  **Description**: Neighbor table related drops are usually normal behavior; excluding them reduces false positives.

#### 7.6 Known Issue Filtering (IssuesList)

```bash
# IssuesList for known issue filtering in event tracing
IssuesList = []
```

- **IssuesList**: Known issue filter. Same format and usage as AutoTracing `IssuesList`. Matches event titles against regex patterns, labeling them with the issue name. Default `[]`.

  Example: `IssuesList = [["known_issue1", "comm=ignored_process"]]`

**Note**: Current only supports `net_rx_latency` tracing of known issues filtering, other events are not supported.

### 8. Metric Collector

This section defines collection rules for various system and network metrics, supporting fine-grained include/exclude filters for both host and container environments.

#### 8.1 Netdev Statistics

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
	# Default: [] is empty, meaning include all.
	#
	# - DeviceExcluded
	# Exclude special devices in netdev statistic. 'DeviceExcluded' has higher
	# priority than 'DeviceIncluded'.
	# Default: [] is empty, meaning ignore nothing.
	#
	[MetricCollector.NetdevStats]
		# EnableNetlink = false
		# DeviceIncluded = ""
		DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

- **EnableNetlink**: Use netlink instead of procfs to collect netdev statistics.

  Default: false. Currently only supported on the host.

- **DeviceIncluded**: Specific devices to include in statistics.

  Default: include all.

- **DeviceExcluded**: Regular expression to exclude devices.

  Default excludes loopback, docker, and veth interfaces. DeviceExcluded has higher priority.

#### 8.2 Netdev DCB Collection

```bash
# netdev dcb, DCB (Data Center Bridging)
#
# Collecting the DCB PFC (Priority-based Flow Control).
#
# - DeviceList
# The net devices we take care of.
# Default: [] is empty, meaning no devices.
#
[MetricCollector.NetdevDCB]
	DeviceList = ["eth0", "eth1"]
```

- **DeviceList**: List of network devices for which DCB (Data Center Bridging) PFC information is collected.

  Default: empty.

#### 8.3 Netdev Hardware Statistics

```bash
# netdev hardware statistic
#
# Collecting the hardware statistic of net devices, e.g, rx_dropped.
#
# - DeviceList
# The net devices we take care of.
# Default: [] is empty, meaning no devices.
#
[MetricCollector.NetdevHW]
	DeviceList = ["eth0", "eth1"]
```

- **DeviceList**: List of network devices for hardware-level statistics (e.g., rx_dropped).

  Default: empty.

#### 8.4 Qdisc Collection

```bash
# Qdisc
#
# - DeviceIncluded
# - DeviceExcluded same as above.
#
[MetricCollector.Qdisc]
	# DeviceIncluded = ""
	DeviceExcluded = "^(lo)|(docker\\w*)|(veth\\w*)$"
```

- **DeviceIncluded / DeviceExcluded**: Same logic as NetdevStats, used to control which network devices’ queue disciplines are monitored.

#### 8.5 vmstat Metric Collection

```bash
# vmstat
#
# This metric supports host vmstat and cgroup vmstat.
# - IncludedOnHost
# - ExcludedOnHost same as above, for the host /proc/vmstat.
#
# - IncludedOnContainer
# - ExcludedOnContainer as above, for the cgroup, containers memory.stat.
#
[MetricCollector.Vmstat]
	IncludedOnHost = "allocstall|nr_active_anon|nr_active_file|nr_boost_pages|nr_dirty|nr_free_pages|nr_inactive_anon|nr_inactive_file|nr_kswapd_boost|nr_mlock|nr_shmem|nr_slab_reclaimable|nr_slab_unreclaimable|nr_unevictable|nr_writeback|numa_pages_migrated|pgdeactivate|pgrefill|pgscan_direct|pgscan_kswapd|pgsteal_direct|pgsteal_kswapd"
	ExcludedOnHost = "total"
	IncludedOnContainer = "active_anon|active_file|dirty|inactive_anon|inactive_file|pgdeactivate|pgrefill|pgscan_direct|pgscan_kswapd|pgsteal_direct|pgsteal_kswapd|shmem|unevictable|writeback|pgscan_globaldirect|pgscan_globalkswapd|pgscan_cswapd|pgsteal_cswapd|pgsteal_globaldirect|pgsteal_globalkswapd"
	ExcludedOnContainer = "total"
```

- **IncludedOnHost / ExcludedOnHost**: Include/exclude fields for host /proc/vmstat.

- **IncludedOnContainer / ExcludedOnContainer**: Include/exclude fields for container cgroup memory.stat.

#### 8.6 Other Metric Collections

```bash
# MemoryEvents/Netstat/MountPointStat
#
# - Included
# - Excluded same as above, DeviceInclude, DeviceExclude.
#
[MetricCollector.MemoryEvents]
	Included = "watermark_inc|watermark_dec"
	# Excluded = ""
[MetricCollector.Netstat]
	# Excluded = ""
	# Included = ""

# MountPointStat
[MetricCollector.MountPointStat]
	MountPointsIncluded = "(^/home$$   )|(^/   $$)|(^/boot$)"
```

- **Included / Excluded**: Control which fields are collected for MemoryEvents and Netstat.

- **MountPointsIncluded**: Regular expression for mount points to collect statistics. Default example includes root, /home, and /boot.

### 9. Pod

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

### 10. Best Practices and Important Notes

- **Resource Control**: In production, prioritize adjusting CPU and memory limits in [RuntimeCgroup] to avoid impacting business containers.
- **Storage Choice**: For small-scale deployments, prefer [Storage.LocalFile] for local troubleshooting. For large clusters, configure Elasticsearch for centralized storage and querying.
- **AutoTracing Tuning**: Adjust thresholds based on workload characteristics. Thresholds that are too low cause frequent triggering; thresholds that are too high may miss issues. Validate gradually in a test environment.
- **Security**: Use strong passwords for ES configuration and consider enabling HTTPS. Avoid hard-coding sensitive information in the configuration file.
- **Compatibility**: Configuration parameters may be affected by kernel version and hardware environment. Always verify with the official HUATUO documentation for your specific setup.

By properly configuring huatuo-bamai.conf, you can fully leverage HUATUO’s capabilities in kernel-level anomaly detection and intelligent tracing, significantly improving observability and troubleshooting efficiency in cloud-native systems.

If you need deeper customization for a specific scenario, feel free to provide more details about your environment.
