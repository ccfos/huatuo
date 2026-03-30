---
title: 配置指南
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 4
---

### 1. 文档概述

`huatuo-bamai` 作为 HUATUO 的核心采集器（bpf-based metrics and anomaly inspector），其配置文件用于定义数据采集范围、探针启用策略、指标输出格式、异常检测规则、以及日志行为等。

配置文件包含全局黑名单、日志、运行时资源限制、存储配置以及自动追踪（AutoTracing）等多个 section。每个配置项均附带详细注释，明确说明用途、默认值及注意事项。本文档针对配置文件中的每一个配置项提供中文的详细解释，帮助用户准确理解和安全定制配置。

**注意**：配置文件中多数参数以 # 注释形式提供默认值，实际启用时需移除 # 并根据环境调整。修改后需重启 huatuo-bamai 进程生效。生产环境建议遵循最小化原则，避免过度开启高开销特性。

### 2. 全局黑名单

```bash
# The global blacklist for tracing and metrics
BlackList = ["netdev_hw", "metax_gpu"]
```

- **BlackList**：全局追踪与指标黑名单。 

  用于排除特定模块或追踪和指标采集，避免无关噪声或高开销探针。例如 ["netdev_hw", "metax_gpu"]，即全局禁用网络设备硬件层（netdev_hw）和 Metax GPU 相关的追踪与指标。

  **说明**：添加黑名单项可有效降低资源消耗，尤其在特定硬件环境中；支持数组格式，可根据实际业务扩展。

### 3. 日志配置

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

- **Level**：日志级别。 

  可选值包括 Debug、Info、Warn、Error、Panic。默认值为 Info。 

  **说明**：控制 huatuo-bamai 的日志输出详细程度。生产环境推荐使用 Info 或 Warn 以减少日志量；Debug 级别仅用于故障排查，会产生大量输出。

- **File**：日志文件路径。

   指定日志写入的文件路径。若为空字符串，则不写入文件（仅输出到标准输出或系统日志）。默认值为空。

   **说明**：在容器化部署中，建议配置具体路径进行持久化。

### 4. 运行时资源限制

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

- **LimitInitCPU**：启动阶段 CPU 限制。

  huatuo-bamai 进程启动期间允许使用的 CPU 核数限制。默认值为 0.5 CPU。 

  **说明**：防止启动过程占用过多 CPU 资源影响宿主机业务，单位为 CPU 核心数（支持小数）。

- **LimitCPU**：运行时 CPU 限制。 

  进程正常运行后允许使用的 CPU 资源上限。默认值为 2.0 CPU。 

  **说明**：根据节点规模和业务负载调整，推荐在高密度容器环境中适当降低以保障业务稳定性。

- **LimitMem**：内存资源限制。 

  huatuo-bamai 进程可使用的最大内存量。默认值为 2048 MB。

  **说明**：单位为 MB，用于通过 cgroup 限制内存占用，防止 OOM（Out Of Memory）风险。生产环境可根据实际采集规模适当增加。

### 5. 存储配置

#### 5.1 Elasticsearch 存储

```bash
# Storage configuration
[Storage]
    # Elasticsearch Storage
    #
    # Disable ES storage if one of Address, Username, Password is empty.
    # Store the tracing and events data of linux kernel to ES.
    #
    # - Address
    # Default address is :9200 of localhost. Port 9200 is used for all API calls
    # over HTTP. This includes search and aggregations, monitoring and anything
    # else that uses a HTTP request. All client libraries will use this port to
    # talk to Elasticsearch
    # Default: :9200
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

- **Address**：Elasticsearch 服务地址。 

  默认值为 http://127.0.0.1:9200（本地 9200 端口）。 

  **说明**：用于存储内核追踪和事件数据。如果 Address、Username 或 Password 中任一项为空，则禁用 ES 存储。支持 HTTP 协议，端口 9200 为 Elasticsearch 标准 API 端口。

- **Index**：Elasticsearch 索引名称。

  默认值为 huatuo_bamai。

  **说明**：索引是 Elasticsearch 中文档的逻辑命名空间，用于组织 huatuo-bamai 产生的追踪与事件数据。

- **Username**：ES 认证用户名。

  无默认值（示例中使用 elastic）。

  **说明**：用于 Basic Auth 认证。

- **Password**：ES 认证密码。

  无默认值（示例中使用 huatuo-bamai）。

  **说明**：配合用户名进行安全认证。生产环境强烈建议使用强密码并结合 TLS 加密传输。

**整体说明**：ES 存储用于持久化内核追踪和事件数据，便于后续检索与分析。

#### 5.2 本地文件存储

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

- **Path**：本地数据存储目录。

  默认值为 huatuo-local。若路径为空，则禁用本地文件存储。

  **说明**：用于在宿主机本地保存数据，主要用于现场故障排查。推荐配置为绝对路径。

- **RotationSize**：单文件轮转大小。

  每个追踪器记录文件在达到该大小时进行轮转。默认值为 100 MB。

  **说明**：单位为 MB，防止单个文件过大导致磁盘占用失控。

- **MaxRotation**：最大保留轮转文件数。

  每个追踪器最多保留的历史文件数量。默认值为 10。

  **说明**：超过数量后自动删除最早文件，控制磁盘空间使用。

### 6. 自动追踪配置

自动追踪模块是 HUATUO 的智能特性之一，可根据阈值自动触发特定性能追踪，减少人工干预。

#### 6.1 CPUIdle 自动追踪 — 容器突发高 CPU 使用场景

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

- **UserThreshold**：用户态 CPU 使用率阈值（%）。

  默认 75%。 当容器用户态 CPU 使用率达到该值时，可能触发 CPU 性能追踪。

- **SysThreshold**：系统态 CPU 使用率阈值（%）。

  默认 45%。 当系统态 CPU 使用率达到该值时，可能触发追踪。

- **UsageThreshold**：总 CPU 使用率阈值（用户态 + 系统态，%）。

  默认 90%（注释中示例）。 总 CPU 使用率达到该阈值时触发追踪。

- **DeltaUserThreshold**：用户态 CPU 短期变化幅度阈值（%）。

  默认 45%。 短时间内用户态 CPU 使用率变化超过该值时触发。

- **DeltaSysThreshold**：系统态 CPU 短期变化幅度阈值（%）。

  默认 20%。 短时间内系统态 CPU 使用率变化超过该值时触发。

- **DeltaUsageThreshold**：总 CPU 使用率短期变化幅度阈值（%）。

  默认 55%。 短时间内总 CPU 使用率变化超过该值时触发。

- **Interval**：CPU 使用率采样间隔（秒）。

  默认 10s。 对所有容器进行 CPU 使用率采样的周期。

- **IntervalTracing**：连续运行间隔（秒）。

  默认 1800s（30 分钟）。 两次自动追踪之间的最小间隔，防止频繁执行对系统造成压力。

- **RunTracingToolTimeout**：单次性能追踪执行超时时间（秒）。默认 10s。 控制追踪程序的最长运行时间，避免长时间占用资源。

**触发逻辑说明**：当满足以下任一条件时触发追踪：

1. UserThreshold 与 DeltaUserThreshold 同时满足；或
2. SysThreshold 与 DeltaSysThreshold 同时满足；或
3. UsageThreshold 与 DeltaUsageThreshold 同时满足。

#### 6.2 CPUSys 自动追踪 — 宿主机突发高系统 CPU 使用场景

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

- **SysThreshold**：系统态 CPU 使用率阈值（%）。

  默认 45%。

- **DeltaSysThreshold**：系统态 CPU 短期变化幅度阈值（%）。

  默认 20%。

- **Interval**：宿主机 CPU 使用率采样间隔（秒）。

  默认 10s。

- **RunTracingToolTimeout**：单次追踪执行超时时间（秒）。默认 10s。

**触发逻辑**：当 SysThreshold 与 DeltaSysThreshold 同时满足时触发。

#### 6.3 Dload 自动追踪 — 容器 D 状态任务剖析

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

- **ThresholdLoad**：容器的系统负载平均值（loadavg）阈值。

  默认 5。 当 loadavg 达到该值时，触发 D 状态（不可中断睡眠）任务剖析。

  **说明**：用于诊断容器中大量进程进入 D 状态的场景。

- **Interval**：监控间隔（秒）。

  默认 10。 Dload 监控的周期。

- **IntervalTracing**：连续运行间隔（秒）。

  默认 1800s（30 分钟）。 两次自动追踪之间的最小间隔，防止频繁执行对系统造成压力。

#### 6.4 IOTracing 自动追踪 — 容器 IO 性能剖析

```bash
# iotracing
#
# io profiling for containers.
#
# - WbpsThreshold
# Max write bytes per second, when reaching this threshold, iotracing is triggered.
# Please note that if it is an NVMe device, it must also meet the UtilThreshold.
# Defalut: 1500 MB/s
#
# - RbpsThreshold
# Max read bytes per second, when reaching this threshold, iotracing is triggered.
# Please note that if it is an NVMe device, it must also meet the UtilThreshold.
# Defalut: 2000 MB/s
#
# - UtilThreshold
# Disk utilization, Percentage of time the disk is busy. If this is consistently
# above 80-90%, the disk may be a bottleneck.
# Defalut: 90%
#
# - AwaitThreshold
# Await (Average IO wait time in ms): High values indicate slow disk response times.
# Defalut: 100ms
#
# - RunTracingToolTimeout
# The executing time of this tracing tool.
# Default: 10s
#
# - MaxProcDump
# The number of processes displayed by iotracing tool.
# Defalut: 10
#
# - MaxFilesPerProcDump
# The number of files per process displayed by iotracing tool.
# Defalut: 5
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

- **WbpsThreshold**：每秒最大写字节数阈值（MB/s）。

  默认 1500 MB/s。 达到该值时可能触发 IO 追踪（NVMe 设备需同时满足 UtilThreshold）。

- **RbpsThreshold**：每秒最大读字节数阈值（MB/s）。

  默认 2000 MB/s。 类似写字节，达到阈值时触发。

- **UtilThreshold**：磁盘利用率阈值（%）。

  默认 90%。 磁盘忙碌时间百分比，持续高于 80-90% 可能成为瓶颈。

- **AwaitThreshold**：平均 IO 等待时间阈值（ms）。

  默认 100ms。 高值表示磁盘响应缓慢。

- **RunIOTracingTimeout**：IO 追踪工具执行超时时间（秒）。

  默认 10s。

- **MaxProcDump**：IO 追踪显示的最大进程数。

  默认 10。 控制输出中展示的进程数量。

- **MaxFilesPerProcDump**：每个进程显示的最大文件数。

  默认 5。 控制每个进程关联文件的展示数量。

**说明**：IOTracing 用于容器 IO 热点诊断，特别关注高负载磁盘场景。

#### 6.5 内存突发自动追踪

该模块用于检测宿主机内存使用量突发增长场景，并在触发时自动捕获内核上下文，便于诊断内存压力事件。

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

- **DeltaMemoryBurst**：内存使用量突发增长百分比阈值。

  默认 100%。 表示内存使用量在采样窗口内增长的比例（例如从 200MB 增长到 400MB 即 100%）。达到该阈值时可能触发内存突发追踪。 

  **说明**：用于捕获整体内存使用量的急剧上升场景。

- **DeltaAnonThreshold**：匿名页内存突发增长百分比阈值。

  默认 70%。 匿名内存（anonymous memory）增长比例阈值，匿名页是内存压力诊断的重要指标。 

  **说明**：重点监控易导致 OOM 或 swap 的匿名内存突发。

- **Interval**：内存使用量采样间隔（秒）。

  默认 10s。 对宿主机内存使用情况进行周期性采样的时间间隔。 

  **说明**：采样频率影响检测灵敏度与开销。

- **IntervalTracing**：连续运行最小间隔（秒）。

  默认 1800s（30 分钟）。 两次内存突发追踪之间的冷却时间，避免频繁执行对系统造成额外压力。

  **说明**：防止追踪工具被过度触发。

- **DumpProcessMaxNum**：触发事件时转储的最大进程数。

  默认 10。 当内存突发事件触发时，最多转储多少个相关进程的详细信息（包括内存占用、调用栈等）。

  **说明**：控制输出数据量，避免单次事件产生过多诊断信息。

### 7. 事件追踪配置

该 section 负责内核关键事件的捕获与延迟监控，包括软中断、内存回收、网络接收延迟、网卡事件及丢包监控等，是 HUATUO 内核级异常上下文采集的核心模块。

#### 7.1 软中断禁用追踪

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
	# Defalut: 10000000 in nanoseconds, 10ms
	#
	[EventTracing.Softirq]
		# DisabledThreshold = 10000000
```

- **DisabledThreshold**：软中断禁用持续时间阈值（纳秒）。默认 10000000 ns（10ms）。 当内核软中断被禁用时间超过该阈值时，huatuo-bamai 将自动采集内核上下文。 说明：软中断长时间禁用可能导致网络、定时器等延迟，适合诊断中断风暴或高负载场景。

#### 7.2 内存回收阻塞追踪

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

- **BlockedThreshold**：内存回收阻塞时间阈值（纳秒）。默认 900000000 ns（900ms）。 当单个进程因内存回收（reclaim）被阻塞超过该时间时，向用户态上报事件并捕获上下文。 说明：内存回收阻塞是导致进程卡顿的常见原因，尤其在内存紧张的云原生环境中。

#### 7.3 网络接收延迟追踪

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

- **Driver2NetRx**：从驱动到网络层接收的延迟阈值（毫秒）。

  默认 5ms。 例如 netif_receive_skb 等函数的延迟监控阈值。

- **Driver2TCP**：从驱动到 TCP 协议栈接收的延迟阈值（毫秒）。

  默认 10ms。 例如 tcp_v4_rcv 等函数的延迟监控。

- **Driver2Userspace**：从驱动到用户态数据拷贝的延迟阈值（毫秒）。

  默认 115ms。 例如 skb_copy_datagram_iovec 等函数的延迟监控。

- **ExcludedContainerQos**：排除的容器 QoS 级别列表。

  默认 [""]。 不监控指定 QoS 级别的容器网络接收延迟（对应 Kubernetes Pod QoS：Guaranteed、Burstable、BestEffort）。 

  **说明**：通常排除 BestEffort 容器以减少噪声。

- **ExcludedHostNetnamespace**：是否排除宿主机网络命名空间。

  默认 true。 不监控宿主机 net namespace 中的 skb 数据包延迟。 

  **说明**：聚焦容器网络流量，减少无关宿主机数据干扰。

#### 7.4 网卡事件监控

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

- **DeviceList**：需要监控的网卡设备列表。

  默认示例包含 "eth0", "eth1", "bond4", "lo"。 为空列表时表示不监控任何设备。 监控网络设备的物理链路状态事件等。

  **说明**：精确指定感兴趣的网络接口，支持 bond、lo 等。

#### 7.5 丢包监控（[EventTracing.Dropwatch]）

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

- **ExcludedNeighInvalidate**：是否排除邻居表无效化（neigh_invalidate）导致的丢包事件。

  默认 true。 

  **说明**：邻居表相关丢包通常为正常行为，排除可减少误报。

### 8. 指标采集器配置

该 section 定义各类系统与网络指标的采集规则，支持精细的包含/排除过滤，适用于宿主机与容器环境。

#### 8.1 网卡统计

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

- **EnableNetlink**：是否使用 netlink 而非 procfs 获取网卡统计。

  默认 false。 仅宿主机环境支持 netlink。 

  **说明**：netlink 方式通常更高效，但需内核支持。

- **DeviceIncluded**：需要纳入统计的特定网卡设备（正则或列表）。

  默认空（包含所有）。

- **DeviceExcluded**：需要排除的网卡设备正则。

  默认排除 lo、docker、veth 等虚拟接口。 

  **说明**：DeviceExcluded 优先级高于 DeviceIncluded，常用于过滤噪声接口。

#### 8.2 网卡 DCB（Data Center Bridging）采集

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

- **DeviceList**：需要采集 DCB（优先流控 PFC）信息的网卡列表。

  默认空。 

  **说明**：主要用于数据中心网络环境下的优先级流控监控。

#### 8.3 网卡硬件统计

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

- **DeviceList**：需要采集硬件层统计（如 rx_dropped）的网卡列表。

  默认空。 

  **说明**：聚焦硬件丢包、错误等底层指标。

#### 8.4 Qdisc（队列规则）采集

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

- **DeviceIncluded / DeviceExcluded**：同 NetdevStats，控制需要监控队列规则的网卡范围。 

  **说明**：用于诊断流量整形、调度延迟等问题。

#### 8.5 vmstat 指标采集

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

- **IncludedOnHost / ExcludedOnHost**：宿主机 /proc/vmstat 的包含/排除字段列表（正则支持）。

- **IncludedOnContainer / ExcludedOnContainer**：容器 cgroup memory.stat 的包含/排除字段列表。 

  **说明**：精细控制 vmstat 指标采集，支持主机与容器差异化配置，避免采集无关字段。

#### 8.6 其他指标采集

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
	MountPointsIncluded = "(^/home$)|(^/$)|(^/boot$)"
```

- **Included / Excluded**（MemoryEvents、Netstat）：同上，控制内存事件、水印变化、Netstat 等指标的过滤。

- **MountPointsIncluded**：需要采集挂载点统计的路径正则。默认示例包含根目录、/home、/boot。

  **说明**：用于监控关键文件系统使用情况。

### 9. Pod 配置

该 section 用于从 kubelet 获取 Pod 信息，实现容器与 Pod 级别的标签关联和指标隔离。

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

- **KubeletReadOnlyPort**：kubelet 只读端口。

  默认 10255。 用于无认证方式从 kubelet 获取 Pod 列表。设置为 0 时禁用该方式。

  **说明**：端口范围 1-65535，适合测试或非安全环境。

- **KubeletAuthorizedPort**：kubelet HTTPS 授权端口。

  默认 10250。 用于安全方式（证书认证）从 kubelet 获取 Pod 信息。设置为 0 时禁用。 

  **说明**：生产环境推荐使用该端口结合证书认证。

- **KubeletClientCertPath**：kubelet 客户端证书及私钥路径。 

  支持格式："/path/to/xxx-kubelet-client.crt,/path/to/xxx-kubelet-client.key" 或单文件 PEM 格式。 

  **说明**：参考 Kubernetes 证书最佳实践，用于 HTTPS 端口的 mTLS 认证。在裸金属或非 Kubernetes 环境中可通过将两个端口设为 0 来禁用 Pod 获取功能。

### 10. 配置最佳实践与注意事项

- **资源控制**：生产环境优先调整 RuntimeCgroup 中的 CPU 和内存限制，避免影响业务容器。
- **存储选择**：小规模部署可优先使用 LocalFile 进行本地排查；大规模集群推荐配置 Elasticsearch 实现集中存储与查询。
- **自动追踪调优**：根据业务负载特征调整阈值，过低阈值会导致频繁触发，过高则可能遗漏问题。建议在测试环境逐步验证。
- **安全性**：ES 配置中请使用强密码，并考虑启用 HTTPS；避免在配置文件中硬编码敏感信息。
- **兼容性**：配置参数受内核版本、硬件环境影响，建议结合 HUATUO 官方文档验证。

通过合理配置 huatuo-bamai.conf，可充分发挥 HUATUO 在内核级异常检测与智能追踪方面的优势，有效提升云原生系统的可观测性和故障诊断效率。如需针对特定场景的深度定制，欢迎提供更多环境细节进一步讨论。
