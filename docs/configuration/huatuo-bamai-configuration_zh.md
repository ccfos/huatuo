---
title: 节点配置
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 4
---

### 1. 概述

`huatuo-bamai.conf` 用于配置 huatuo-bamai 的采集范围、追踪规则、资源限制、存储和日志。

本文引用仓库中的 `huatuo-bamai.conf`，并用表格说明字段。值的含义区分如下：

- **内置默认值**：省略字段时由程序提供；下文的“默认”均指此值。
- **配置文件显式值**：未注释的赋值，加载文件时覆盖内置默认值。
- **注释示例值**：以 `#` 注释的赋值，不参与配置加载，也不一定等于内置默认值。

使用内置默认值无需取消注释。手动修改配置文件后需重启；通过 Node API 更新时，生效范围见 5.1 节。

### 2. 全局黑名单

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

| 字段 | 说明 |
| --- | --- |
| `BlackList` | 禁用的追踪器和指标采集器名称数组，内置默认值为 `[]`。启用模块需从黑名单移除对应名称，满足其依赖和开关条件，并重启以完成注册。 |

配置文件显式禁用以下模块：

| 黑名单项 | 功能 |
| --- | --- |
| `netdev_hw` | 网卡硬件统计 |
| `netdev_qdisc` | 队列统计 |
| `net_conntrack` | 连接跟踪统计 |
| `metax_gpu` | Metax GPU 指标 |
| `ascend_npu` | Ascend NPU 指标 |
| `diskio` | 磁盘 I/O 指标 |
| `tcp_retransmit` | TCP 重传追踪 |
| `mthreads_gpu` | 摩尔线程 GPU 指标 |
| `mthreads_xid` | 摩尔线程 GPU XID 事件 |

### 3. 日志

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

| 字段 | 说明 |
| --- | --- |
| `Level` | 日志级别，默认 `Info`。可选 `Debug`、`Info`、`Warn`、`Error`、`Panic`。生产环境建议使用 `Info` 或 `Warn`；`Debug` 日志量较大，适用于故障排查。 |
| `File` | 日志文件路径，默认 `""`，表示不写入文件。需要将日志保存到文件时设置此项。 |

### 4. 资源限制

默认不创建 Huatuo 自身 cgroup。只有显式传入 `--enable-cgroup` 时，本节配置才会生效；Kubernetes 和 systemd 部署应使用各自的资源管理配置。

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

| 字段 | 说明 |
| --- | --- |
| `StartupCPULimitCores` | 初始化阶段 CPU 上限，默认 `0.5` 核。 |
| `CPULimitCores` | 启动完成后的 CPU 上限，默认 `2.0` 核。 |
| `MemoryLimitMiB` | 进程内存上限，默认 `2048` MiB。 |

### 5. HTTP 服务

HTTP 服务提供配置更新、按需任务管理和事件订阅接口。

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

| 字段 | 说明 |
| --- | --- |
| `ListenAddress` | 监听地址，默认 `":19704"`。使用 `host:port` 格式，主机为空时监听所有接口。 |
| `MaxEventStreamClients` | `POST /v1/events/watch` 的最大并发连接数，默认 `100`，必须为正整数。达到上限后，新连接返回 HTTP 429。 |
| `EventStreamKeepAliveIntervalSeconds` | SSE 心跳注释间隔，单位秒，默认 `30`，必须为正整数。应小于上游代理的空闲超时时间。 |
| `Auth.BearerToken` | 服务鉴权凭证，内置默认值为 `""`，必须配置非空且不含空白字符的值，并与 huatuo-apiserver 配置的 Node 凭证一致。部署前必须替换配置文件中的占位值。 |

配置更新、Operations 和事件订阅接口均要求 `Authorization: Bearer <token>`。`/readyz`、指标、版本和 `GET /openapi.json` 保持公开；完整接口定义可通过 `GET /openapi.json` 查询。

#### 5.1 通过 API 更新配置

`PUT /v1/config` 接收一个非空的 `config` 对象。键名使用与 TOML 结构一致的
点分路径，值保留 JSON 类型：

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

更新成功返回 `204 No Content`。Node Agent 先校验完整配置，再原子替换配置文件，最后更新内存中的配置。校验或持久化失败时，继续使用原配置。
未知键、无效值类型和空更新对象返回 `400 Bad Request`。

动态读取配置的组件无需重启即可使用新配置。HTTP 监听与鉴权、存储初始化、cgroup 设置等仅在启动时读取的配置会写入文件，重启 `huatuo-bamai` 后生效。

#### 5.2 按需任务管理（Operations）

`Operations` 为节点上的 Profiling 和 Tracing 提供统一的任务管理，包括并发限制、启动、停止、收尾和终态保留。当前按需 Tracing 尚无执行器，通过参数校验的新启动请求返回 `501`，不创建任务。

```toml
# Shared lifecycle policy for on-demand operations.
[Operations]
    # MaxConcurrent = 10
    # LaunchTimeoutSeconds = 10
    # StopGracePeriodSeconds = 5
    # FinalizationTimeoutSeconds = 30
    # TerminalRetentionPeriodSeconds = 600
```

| 字段 | 说明 |
| --- | --- |
| `MaxConcurrent` | Profiling 与 Tracing 共用的未完成任务数上限，默认 `10`，必须为正整数。达到上限后拒绝新任务，不排队。 |
| `LaunchTimeoutSeconds` | 任务执行器的启动超时，单位秒，默认 `10`，必须为正整数。 |
| `StopGracePeriodSeconds` | 停止任务时的等待期限，单位秒，默认 `5`，必须为正整数。 |
| `FinalizationTimeoutSeconds` | 执行器收尾阶段的超时，单位秒，默认 `30`，必须为正整数。 |
| `TerminalRetentionPeriodSeconds` | 任务结束后在节点内存中保留终态记录的时长，单位秒，默认 `600`，必须为正整数。到期后无法再通过任务接口查询该记录。 |

| 接口 | 用途 |
| --- | --- |
| `POST /v1/operations` | 创建任务。新任务返回 `202`，命中已有幂等任务时返回 `200`；达到并发上限时返回 `429`。 |
| `GET /v1/operations/{request_id}` | 查询任务状态；记录不存在或已过期时返回 `404`。 |
| `POST /v1/operations/{request_id}/stop` | 请求停止任务。接受停止请求时返回 `202`；任务已在停止或已结束时返回 `200`。 |

启动请求通过 `kind` 选择 `profiling` 或 `tracing`，通过 `duration_seconds` 指定执行时长。上述启动、停止和收尾超时分别作用于各自阶段，不替代执行时长。

#### 5.3 性能剖析（Profiling）

Profiling 通过 `POST /v1/operations` 启动。请求需提供 `request_id`、`duration_seconds`、`scope`，设置 `kind = "profiling"`，并在 `spec` 中提供 `type`、`language` 和 `mode`；容器范围还需提供 `container_id`。查询与停止使用 5.2 节的统一任务接口。

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

| 字段 | 说明 |
| --- | --- |
| `AggregationIntervalSeconds` | 采样结果聚合间隔，单位秒，默认 `10`，必须为正整数。实际间隔不超过请求时长；Python 使用整个请求时长作为聚合窗口。 |
| `MaxConcurrentProcesses` | 单次 profiler 执行允许的最大并发采样子进程数，默认 `10`，必须为非负整数；`0` 表示不限。任务总并发数仍受 `Operations.MaxConcurrent` 限制。 |
| `CommandOutputLimitBytes` | Node 保留的 profiler 标准输出上限，单位字节，默认 `65536`，必须为正整数。超出时保留末尾内容，用于错误诊断；该值不限制剖析结果大小。 |
| `ToolDir` | 外部采样工具根目录，内置默认值为 `""`；`/opt/huatuo/tools` 是未生效的注释示例。该值传给 profiler 的 `--tool-path`。 |

Java 使用 `ToolDir` 下的 `bin/asprof` 和 `lib/libasyncProfiler.so`，Python 使用 `py-spy`；原生采集不需要这些外部工具。只检查请求语言所需的工具，缺少工具时拒绝请求且不创建任务。

`Operations` 和 `Profiling` 配置在服务启动时读取，通过 API 更新后需重启 `huatuo-bamai` 才能生效。

### 6. 数据存储

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

| 字段 | 说明 |
| --- | --- |
| `Address` | Elasticsearch/OpenSearch 服务地址，支持 HTTP/HTTPS，默认为空。`Address`、`Username`、`Password` 全部为空时禁用该存储后端；启用时必须同时配置，缺少任一项会导致启动失败。 |
| `Index` | 保存追踪与事件数据的索引名称，默认 `huatuo_bamai`。 |
| `Username` | Basic Auth 用户名，默认为空。 |
| `Password` | Basic Auth 密码，默认为空。生产环境建议使用强密码并启用 HTTPS。 |

#### 6.2 本地文件

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

| 字段 | 说明 |
| --- | --- |
| `Path` | 本地数据存储目录，默认 `huatuo-local`。设为空字符串时禁用本地文件存储；启用时建议使用绝对路径。 |
| `RotationSizeMiB` | 每个追踪器记录文件的轮转大小，单位 MiB，默认 `100`。 |
| `MaxRotatedFiles` | 每个追踪器最多保留的历史文件数，默认 `10`。超出数量后删除最早的文件。 |

### 7. 自动追踪

自动追踪根据资源使用情况和配置阈值触发性能采集。

`AutoTracing.CPUIdle.Tool` 和 `AutoTracing.CPUSys.Tool` 可分别设为 `perf` 或 `profiler`，缺省为 `perf`；空字符串及其他值无效。配置更新后，下一次触发使用新工具，运行中的采集不变。

- `perf`：沿用事件中的 `flamedata`，展示无需 huatuo-apiserver。
- `profiler`：使用原生 CPU on-CPU 采集，将 profile 独立存入 Elasticsearch，通过 huatuo-apiserver 的 Pyroscope 兼容接口展示。需要启用 bamai 的 Elasticsearch 存储，且不能使用 `--disable-storage`。事件保留触发指标，`flamedata` 为 `null`。

两种工具均使用 `RunTracingToolTimeout` 指定采集秒数。工具失败时不自动切换。

查看 profiler 结果时，打开目标对应的 Continuous Profiling 宿主机或容器面板，将 Task ID 设为事件的 `tracer_id`，`type` 设为 `process_cpu:cpu:nanoseconds:cpu:nanoseconds`，时间范围覆盖采样时段。面板入口和交互操作见[查看火焰图](/docs/best-practice/continuous-profiling_zh.md#5-查看火焰图)。

#### 7.1 容器 CPU 突增

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

| 字段 | 说明 |
| --- | --- |
| `UserThreshold` | 用户态 CPU 使用率阈值，默认 `75%`。 |
| `SysThreshold` | 系统态 CPU 使用率阈值，默认 `45%`。 |
| `UsageThreshold` | 总 CPU 使用率阈值，默认 `90%`。 |
| `DeltaUserThreshold` | 用户态 CPU 使用率相对上次采样的增量阈值，默认 `45` 个百分点。 |
| `DeltaSysThreshold` | 系统态 CPU 使用率相对上次采样的增量阈值，默认 `20` 个百分点。 |
| `DeltaUsageThreshold` | 总 CPU 使用率相对上次采样的增量阈值，默认 `55` 个百分点。 |
| `Interval` | 容器 CPU 使用率采样间隔，单位秒，默认 `10`。 |
| `IntervalTracing` | 两次自动追踪之间的最小间隔，单位秒，默认 `1800`（30 分钟）。 |
| `RunTracingToolTimeout` | 采集时长，单位秒，默认 `10`。 |

**触发条件**：满足追踪间隔后，以下任一组的使用率及其增量均严格大于对应阈值时，容器成为追踪候选：

- `UserThreshold` 与 `DeltaUserThreshold`；
- `SysThreshold` 与 `DeltaSysThreshold`；
- `UsageThreshold` 与 `DeltaUsageThreshold`。

**容器过滤**：通过 `Included` 和 `Excluded` 规则数组控制监控范围。

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

| 字段 | 说明 |
| --- | --- |
| `Filter` | 容器过滤规则，默认无规则，监控所有容器。使用 TOML 数组表 `[[...]]` 定义规则，每条包含过滤字段 `Field` 和正则表达式 `Pattern`。 |

- 仅配置 `Excluded`：排除匹配的容器。
- 仅配置 `Included`：仅监控匹配的容器。
- 同时配置两者：监控匹配 `Included` 且不匹配 `Excluded` 的容器。

#### 7.2 宿主机系统态 CPU 突增

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

| 字段 | 说明 |
| --- | --- |
| `SysThreshold` | 宿主机系统态 CPU 使用率阈值，默认 `45%`。 |
| `DeltaSysThreshold` | 系统态 CPU 使用率相对上次采样的增量阈值，默认 `20` 个百分点。 |
| `Interval` | 宿主机 CPU 使用率采样间隔，单位秒，默认 `10`。 |
| `IntervalTracing` | 两次追踪之间的最小间隔，单位秒，默认 `1800`。 |
| `RunTracingToolTimeout` | 采集时长，单位秒，默认 `10`。 |

**触发条件**：满足追踪间隔后，系统态 CPU 使用率严格大于 `SysThreshold`，且其增量严格大于 `DeltaSysThreshold` 时触发。

#### 7.3 容器 D 状态负载

依赖 cgroup v1 的负载统计接口；cgroup v2 下跳过该追踪器。

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

| 字段 | 说明 |
| --- | --- |
| `ThresholdLoad` | 容器 D 状态（不可中断睡眠）任务的负载平均值阈值，默认 `5`。D 状态负载严格大于该值且满足 `IntervalTracing` 时触发追踪；不使用包含运行中任务的总负载判断。 |
| `Interval` | 容器负载采样间隔，单位秒，默认 `10`。 |
| `IntervalTracing` | 两次自动追踪之间的最小间隔，单位秒，默认 `1800`（30 分钟）。 |
| `EnableDebug` | 调试开关，默认 `false`。启用后跳过负载阈值和最小追踪间隔检查，尝试采集容器及宿主机各状态任务的栈，可能增加开销，仅用于调试。 |

#### 7.4 容器 I/O 性能

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

| 字段 | 说明 |
| --- | --- |
| `WbpsThreshold` | NVMe 写吞吐量阈值，单位 MiB/s，默认 `1500`。写吞吐量和磁盘利用率都需连续两次采样超过各自阈值。 |
| `RbpsThreshold` | NVMe 读吞吐量阈值，单位 MiB/s，默认 `2000`。读吞吐量和磁盘利用率都需连续两次采样超过各自阈值。 |
| `UtilThreshold` | 磁盘忙碌时间百分比阈值，默认 `90`。非 NVMe 设备连续两次采样超过该值时触发；NVMe 设备还需满足读或写吞吐量条件。 |
| `AwaitThreshold` | 平均读或写 IO 等待时间阈值，单位 ms，默认 `100`。读等待或写等待连续两次采样超过该值即可触发，不要求同时满足利用率和吞吐量条件。 |
| `RunTracingToolTimeout` | IO 追踪工具执行超时，单位秒，默认 `10`。 |
| `MaxProcDump` | IO 追踪结果中最多显示的进程数，默认 `10`。 |
| `MaxFilesPerProcDump` | 每个进程最多显示的文件数，默认 `5`。 |

#### 7.5 宿主机匿名内存突增

该模块检测宿主机匿名内存突增，触发后记录按 RSS 排序的进程信息，并尝试采集运行时内存快照。匿名内存取 `/proc/meminfo` 中 `Active(anon)` 与 `Inactive(anon)` 之和。历史采样窗口填满后，增长比例和占总内存比例必须同时达到阈值，并满足追踪间隔才会上报。

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

| 字段 | 说明 |
| --- | --- |
| `DeltaMemoryBurst` | 匿名内存相对历史窗口最早采样值的增长比例阈值，默认 `100%`，即至少翻倍，例如从 200 MiB 增至 400 MiB。 |
| `DeltaAnonThreshold` | 匿名内存占宿主机 `MemTotal` 的比例阈值，默认 `70%`，范围 `0–100`；表示占比，不是增长比例。 |
| `Interval` | 宿主机匿名内存采样间隔，单位秒，默认 `10`。 |
| `IntervalTracing` | 两次内存突发追踪之间的最小间隔，单位秒，默认 `1800`（30 分钟）。 |
| `DumpProcessMaxNum` | 按 RSS 降序保留的进程数上限，默认 `10`。尝试为排名前 `min(DumpProcessMaxNum, 3)` 个进程采集运行时快照；大于 `3` 时只增加排名数量。 |

快照支持范围、采集预算和输出裁剪规则见[自动追踪字段](/docs/reference/autotracing_zh.md#宿主机匿名内存)。

#### 7.6 容器内存阈值快照

容器内存达到条件时，尝试采集 Go、HotSpot 或 CPython 运行时快照。默认开启；将 `memory_threshold_snapshot` 加入全局 `BlackList` 并重启 `huatuo-bamai` 服务可关闭。运行时支持仍为实验性，需在目标版本验证，不保证在 OOM 前完成采集。

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

| 字段 | 说明 |
| --- | --- |
| `ThresholdPercent` | 容器内存使用量占限额的百分比阈值，默认 `90`，范围 `1–100`。触发方式受 cgroup 版本影响，见下方说明。 |
| `IntervalTracing` | 成功或失败采集尝试完成后的节点级最小间隔，单位秒，默认 `300`，必须为正数。 |
| `RunTracingToolTimeout` | 运行时采集超时，单位秒，默认 `2`，必须为正数。超时采用协作式取消，不含运行时识别和保存时间，不能中断同步系统调用，因此不是整个操作的耗时上限。 |
| `MaxMemoryObjectEntries` | 单次快照最多保留的内存对象排序条目数，默认 `10`，范围 `1–100`。最终 JSON 上限为 512 KiB，超限时裁剪。 |

**触发条件**：cgroup v1 注册配置比例对应的内存阈值；cgroup v2 在 `high` 或 `max` 事件后检查当前使用量与限额的比例，不设置 `memory.high`，不能保证在恰好达到配置比例时收到通知。两种版本均在注册和硬限制变化时检查水位。

**启用条件**：需要 Linux、memory cgroup、已启用的 Pod 管理器、kubelet 元数据、宿主机 PID/procfs/cgroup 视图、内核 BTF，以及 BPF 和目标进程内存访问权限。`--procfs-prefix` 不会重定向快照的进程内存读取。

监控运行中的普通容器和可重启 init sidecar，每个容器需有独立的 memory cgroup。最多监听 4096 个容器；注册失败或监听失效后不自动重试，需检查日志并在修复后重启 `huatuo-bamai` 服务。

#### 7.7 中断利用率异常

该模块检测单个 CPU 的 irq+softirq 利用率异常，并调用 `irqtracing` 采集
softirq source 和 victim 调用栈。IRQ 结果统一写入 profiling 存储，需要
配置 Elasticsearch。窗口可查询且发布标记保存成功后，任务才成功；仅配置
LocalFile 不足以保存该结果。

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

| 字段 | 说明 |
| --- | --- |
| `Interval` | 各 CPU 的 irq+softirq 利用率采样间隔，数据来自 `/proc/stat`，单位秒，默认 `2`。 |
| `RunTracingToolTimeout` | 单次 `irqtracing` 采集时长，单位秒，默认 `3`。 |
| `IntervalTracing` | 两次触发之间的最小间隔，单位秒，默认 `300`。 |
| `MaxEventsPerSecond` | 被跟踪 CPU 上每秒采集的 source 与 victim 栈样本总上限，默认 `1000`，范围 `2–8589934590`。额度尽量均分给 `softirq_raise` 和 `softirq_entry`，默认各 `500` 条/秒。 |
| `MinCPUs`、`DeltaUsageThreshold` 和 `RelativeIncreaseThreshold` | 控制多 CPU irq+softirq 利用率突增规则；两个阈值分别表示利用率增加的百分点和相对上一采样值的增长百分比。 |
| `SustainedIntervals` 和 `UsageThreshold` | 控制单 CPU irq+softirq 持续高利用率规则的连续采样次数和利用率阈值。 |

#### 7.8 已知问题标记

```toml
# Autotracing configuration.
#
# - IssuesList
# Known issue filters for autotracing.
#
[AutoTracing]
    IssuesList = []
```

| 字段 | 说明 |
| --- | --- |
| `IssuesList` | 已知问题过滤器。格式 `[["问题名称", "正则"], ...]`。采集到的堆栈匹配正则时标记为对应问题名称，默认 `[]`。当前用于 dload 追踪。示例：`IssuesList = [["known_issue1", "softlockup"], ["known_issue2", "alloc_pages.*failed"]]` |

### 8. 事件追踪

本节配置调度、内存回收、网络和硬件事件的采集条件。

#### 8.1 调度 tick 间隔

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

| 字段 | 说明 |
| --- | --- |
| `IntervalThreshold` | 调度 tick 间隔阈值，单位纳秒，默认 `10000000`（10 ms）。达到阈值时采集事件。过长的 tick 间隔可用于推断 CPU 异常停顿，但不能单独证明软中断被禁用。 |

#### 8.2 内存回收阻塞

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

| 字段 | 说明 |
| --- | --- |
| `BlockedThreshold` | 内存回收阻塞时间阈值，单位纳秒，默认 `900000000`（900 ms）。进程因内存回收阻塞超过该时间时，上报事件并采集上下文。 |

#### 8.3 网络接收延迟

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

| 字段 | 说明 |
| --- | --- |
| `Driver2NetRx` | 从驱动到网络层接收的延迟阈值，单位毫秒，默认 `5`，例如到达 `netif_receive_skb` 的延迟。 |
| `Driver2TCP` | 从驱动到 TCP 协议栈接收的延迟阈值，单位毫秒，默认 `10`，例如到达 `tcp_v4_rcv` 的延迟。 |
| `Driver2Userspace` | 从驱动到用户态数据拷贝的延迟阈值，单位毫秒，默认 `115`，例如到达 `skb_copy_datagram_iovec` 的延迟。 |
| `ExcludedContainerQos` | 排除的容器 QoS 列表，内置默认值为 `[]`；配置文件显式设置为 `["besteffort"]`。支持 `guaranteed`、`burstable`、`besteffort`，大小写不敏感。 |
| `ExcludedHostNetnamespace` | 是否排除宿主机网络命名空间中的数据包延迟，默认 `true`。 |

#### 8.4 网卡事件

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

| 字段 | 说明 |
| --- | --- |
| `DeviceList` | 监控网卡事件的完整匹配正则列表，内置默认值为 `[]`，即不监控设备。配置文件显式设置为 `["eth0", "eth1", "bond4", "lo"]`。`"eth0"` 等字面量精确匹配，`"bond[0-9]+"` 等模式可匹配多块网卡。 |

#### 8.5 丢包

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

| 字段 | 说明 |
| --- | --- |
| `Filter` | 独立 `dropwatch` 追踪器使用的 tcpdump 风格过滤表达式，默认 `"tcp"`。TCP 重传关联使用 `TCPRetransmit.Filter`。 |
| `MaxEventsPerSecond` | BPF 每秒最多输出的 dropwatch 事件数，默认 `100`，设为 `0` 表示不限速。 |
| `ExcludeContainers` | 预留的容器排除列表，默认 `[]`，当前配置后不生效。按调用栈抑制丢包事件应使用 `EventTracing.IssuesList`。 |

#### 8.6 TCP 重传

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

| 字段 | 说明 |
| --- | --- |
| `EnableTLP` | 是否采集 `tcp_send_loss_probe` 事件，默认 `false`。 |
| `Filter` | TCP 重传过滤表达式。开启本地丢包关联时，两个输入使用相同的规范化表达式，空值回退为 `tcp`；关闭关联时，空值不传入 `--filter`。`Dropwatch.Filter` 仅控制独立的 dropwatch 追踪器。 |
| `EnableDropwatch` | 是否在 tcpshark 内关联重传与丢包，默认 `false`。需从 `BlackList` 移除 `tcp_retransmit`；独立的 `dropwatch` 追踪器可保持禁用。关联使用同一个 `Filter`，不单独输出原始丢包事件。 |
| `MaxEventsPerSecond` | BPF 每秒最多输出的 TCP 重传事件数，默认 `100`，设为 `0` 表示不限速。关联模式下，内嵌 dropwatch 使用同值但独立限速，默认两条输入各自最多 `100` 条/秒。超限时输出 `rate limit hit` 日志。 |

#### 8.7 硬件错误（RAS）

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

| 字段 | 说明 |
| --- | --- |
| `MceThrBackoff` | MCE 阈值中断（THR）事件的最小存储间隔，单位秒，默认 `1800`（30 分钟）。THR 由 CPU 本地 APIC 阈值中断触发，纠正性硬件错误可能产生大量事件；缩短间隔会增加记录量和存储压力。 |

#### 8.8 摩尔线程 GPU XID 事件

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

| 字段 | 说明 |
| --- | --- |
| `MthreadsXidLevel` | XID 事件报告的最低严重级别，可选 `""`（禁用）、`"notify"`、`"warning"`、`"fatal"`，低于所选级别的事件会被过滤。内置默认值为 `""`；注释中的 `"warning"` 仅为示例。 |

启用时需安装 MUSA 驱动，从全局 `BlackList` 移除 `mthreads_xid`，并将 `MthreadsXidLevel` 设为非空的有效级别。追踪器每秒轮询 `/proc/driver/musa/gpu*/event_report`，事件包含 UUID、XID ID、严重级别、作用域、PCI BDF、进程 ID 和附加上下文。

#### 8.9 已知问题抑制

```toml
# Linux kernel event tracing configuration.
#
# - IssuesList
# Known issue filters for event tracing.
#
[EventTracing]
    IssuesList = []
```

| 字段 | 说明 |
| --- | --- |
| `IssuesList` | 已知问题抑制规则，格式为 `[["名称", "正则"], ...]`，默认值为 `[]`。对 `net_rx_latency`，正则匹配生成的事件标题；对 `dropwatch`，正则匹配以换行符连接的内核调用栈。匹配后事件会被丢弃；配置的名称只用于标识规则，不会写入已保存事件。示例：`IssuesList = [["ignored_process", "comm=ignored_process"], ["neighbor_cleanup", "neigh_invalidate/"]]` |

### 9. 指标采集

本节配置系统、网络和设备指标的采集。`Included` 和 `Excluded` 使用正则表达式，过滤规则如下：

- 未配置规则：全部采集。
- 仅配置 `Excluded`：排除匹配项。
- 仅配置 `Included`：仅采集匹配项。
- 同时配置两者：采集匹配 `Included` 且不匹配 `Excluded` 的项。

#### 9.1 网卡基础统计

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

| 字段 | 说明 |
| --- | --- |
| `EnableNetlink` | 是否使用 Netlink 替代 procfs 读取网卡统计，默认 `false`。Netlink 查询目标宿主机或容器的网络命名空间，需有权限访问该命名空间。 |
| `DeviceIncluded` | 纳入统计的网卡名称正则，默认 `""`（不限制纳入范围）。 |
| `DeviceExcluded` | 排除网卡的正则，内置默认值为 `""`（不排除）。配置文件显式设置上方正则，排除匹配的 lo、docker 和 veth 接口。 |

#### 9.2 网卡优先级流控（PFC）

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

| 字段 | 说明 |
| --- | --- |
| `DeviceList` | 采集 DCB PFC 信息的网卡完整匹配正则列表。内置默认值为 `[]`（不采集设备）；配置文件显式设置为 `["eth0", "eth1"]`。 |

#### 9.3 网卡硬件统计

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

| 字段 | 说明 |
| --- | --- |
| `DeviceList` | 采集硬件层统计（如 rx_dropped）的网卡完整匹配正则列表。内置默认值为 `[]`（不采集设备）；配置文件显式设置为 `["eth0", "eth1"]`。 |

#### 9.4 网卡队列统计（Qdisc）

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

| 字段 | 说明 |
| --- | --- |
| `DeviceIncluded` / `DeviceExcluded` | 网卡过滤正则，内置默认值均为 `""`，即不限制范围。配置文件显式设置 `DeviceExcluded` 为上方正则；过滤逻辑见本节开头。 |

#### 9.5 虚拟内存统计（vmstat）

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

| 字段 | 说明 |
| --- | --- |
| `IncludedOnHost` / `ExcludedOnHost` | 宿主机 `/proc/vmstat` 的字段过滤正则，内置默认值均为 `""`。配置文件显式指定上方纳入列表，并排除匹配 `total` 的字段。 |
| `IncludedOnContainer` / `ExcludedOnContainer` | 容器 cgroup `memory.stat` 的字段过滤正则，内置默认值均为 `""`。配置文件显式指定上方纳入列表，并排除匹配 `total` 的字段。 |

#### 9.6 内存事件

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

| 字段 | 说明 |
| --- | --- |
| `Included` / `Excluded` | 内存事件字段过滤正则，内置默认值均为 `""`。配置文件显式设置 `Included`，纳入匹配 `watermark_inc` 或 `watermark_dec` 的字段。 |

#### 9.7 网络协议统计

```toml
[MetricCollector.Netstat]
    # Excluded = ""
    # Included = ""
```

| 字段 | 说明 |
| --- | --- |
| `Included` / `Excluded` | 网络协议统计字段的过滤正则，内置默认值均为 `""`，即不限制范围；配置文件未显式设置。 |

#### 9.8 挂载点统计

```toml
# MountPointStat
[MetricCollector.MountPointStat]
    MountPointsIncluded = "(^/home$)|(^/$)|(^/boot$)"
```

| 字段 | 说明 |
| --- | --- |
| `MountPointsIncluded` | 挂载点路径过滤正则，内置默认值为 `""`（不限制范围）。配置文件显式设置为上方正则，仅采集 `/`、`/home`、`/boot`。 |

#### 9.9 摩尔线程 GPU

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

| 字段 | 说明 |
| --- | --- |
| `EnableHealth` | 健康指标采集开关，默认 `true`。包括温度、功耗、电压、利用率、内存容量、时钟、风扇、性能状态及 VPU 指标。 |
| `EnablePCIe` | PCIe 链路指标采集开关，默认 `false`。包括当前与最大链路速率、宽度及重放计数器。 |
| `EnableMTLink` | MtLink 互连指标采集开关，默认 `false`。包括每链路带宽、链路数及链路状态。 |

**启用条件**：从全局 `BlackList` 移除 `mthreads_gpu`，安装可由系统动态链接器找到的 `libmtml.so.2` 或 `libmtml.so`，然后重启 `huatuo-bamai` 服务。启动时缺少库会禁用采集器；补装库或修改 `LD_LIBRARY_PATH` 后需重启 `huatuo-bamai` 服务。

**配置更新**：三个指标开关在每次采集时读取最新配置，使用 Node API 更新后，下次采集生效。

#### 9.10 Ascend NPU

从全局 `BlackList` 移除 `ascend_npu` 并重启 `huatuo-bamai` 服务后启用采集器。节点需有可用的 Ascend NPU 和 `/usr/local/dcmi/libdcmi.so`；即使关闭 `EnableDCMI`，设备发现仍依赖 DCMI 初始化。

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

| 字段 | 说明 |
| --- | --- |
| `EnableDCMI` | 布尔值，默认 `true`。采集健康状态、功耗、温度、电压、利用率、频率、网络健康、HBM 和 ECC 指标。 |
| `EnablePCIe` | 布尔值，默认 `false`。采集 PCIe 最大及当前链路速率、宽度。 |
| `EnableHCCN` | 布尔值，默认 `false`。通过 `/usr/local/Ascend/driver/tools/hccn_tool` 子进程采集网络链路、流量、计数器和光模块指标，启用前需确保工具可用。 |

以上开关在每次指标采集时读取最新配置；使用 Node API 更新后，下次采集生效。

### 10. Pod 容器信息

本节配置从 kubelet 获取 Pod 信息的端口和凭证，用于关联容器与 Pod 标签。

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

| 字段 | 说明 |
| --- | --- |
| `KubeletReadOnlyPort` | kubelet 只读端口，默认 `10255`，通过无认证方式获取 Pod 列表。端口范围为 `1–65535`，设为 `0` 时禁用该方式。 |
| `KubeletAuthorizedPort` | kubelet HTTPS 端口，默认 `10250`，使用证书认证获取 Pod 信息。端口范围为 `1–65535`，设为 `0` 时禁用该方式。生产环境建议使用此方式。 |
| `KubeletClientCertPath` | kubelet 客户端证书及私钥路径，内置默认值为 `""`。支持逗号分隔的证书、私钥两条路径，或包含两者的单个 PEM 文件。配置文件显式使用上方 `/etc/kubernetes/pki/` 下的路径，部署时需核对。非 Kubernetes 环境可将两个 kubelet 端口都设为 `0`，禁用 Pod 获取。 |

### 11. 命令行参数

`huatuo-bamai` 支持以下命令行参数：

```bash
huatuo-bamai --region <region> [选项]
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--config` | 配置文件名 | `huatuo-bamai.conf` |
| `--config-dir` | 配置文件目录 | `conf` |
| `--bpf-dir` | BPF 对象文件目录 | `bpf` |
| `--tools-bin-dir` | 追踪工具二进制目录 | `bin` |
| `--region` | 部署区域（必填） | - |
| `--disable-kubelet` | 禁用 kubelet Pod 获取 | `false` |
| `--disable-storage` | 禁用存储后端 | `false` |
| `--enable-cgroup` | 启用自身 cgroup 资源限制（默认关闭） | `false` |
| `--disable-tracing` | 禁用指定追踪模块（可多次指定） | - |
| `--log-debug` | 强制设置日志级别为 Debug | `false` |
| `--dry-run` | 仅加载测试，启动后优雅退出 | `false` |
| `--procfs-prefix` | procfs 挂载点前缀 | - |

### 12. 覆盖规则

配置文件中的显式赋值覆盖内置默认值，注释不参与加载。命令行参数按各自语义生效，不统一覆盖配置文件。

- **日志级别**：`--log-debug` 强制使用 `Debug`；否则读取 `Log.Level`，未配置时使用 `Info`。
- **追踪黑名单**：`--disable-tracing` 与配置文件 `BlackList` 合并。
- **独立开关**：`--disable-kubelet` 和 `--disable-storage` 分别关闭 Pod 同步和存储后端，没有同名 TOML 字段；启用这些开关仍需通过配置文件校验。

### 13. 配置建议

- **资源控制**：Kubernetes 使用 Pod 资源限制，systemd 使用服务资源限制。直接运行且没有外部管理器时，可使用 `--enable-cgroup` 和 `[Runtime]`。
- **存储选择**：小规模部署可优先使用 LocalFile 进行本地排查；大规模集群推荐配置 Elasticsearch 实现集中存储与查询。
- **自动追踪调优**：根据业务负载特征调整阈值，过低阈值会导致频繁触发，过高则可能遗漏问题。建议在测试环境逐步验证。
- **安全性**：ES 配置中请使用强密码，并考虑启用 HTTPS；避免在配置文件中硬编码敏感信息。
- **兼容性**：启用功能前，核对所需的内核能力、硬件和运行时依赖。

### retain unresolved reclaim events in the host stream

- **主机输出**：将未解析出容器归属的慢速直接回收事件保留在主机事件流。
  要求启用 `memory_reclaim_events` 采集器，且现有 `try_to_free_pages` 入口/返回探针
  可用并能挂载；不新增探针，也没有额外的 cgroup 版本开关。
  沿用 `memory_reclaim` 事件名和 `BlockedThreshold`。
  空容器 ID 标记为 `container_attribution="unresolved"`，可能是主机任务，
  也可能是归属未解析的容器任务，不能认为已确认属于物理机服务。
  已知容器仍只输出一条容器事件，不重复输出主机事件。此路径不采集 kswapd，
  也不增加聚合指标；新增开销是原先丢弃事件的输出与存储。

### collect cgroup v2 task states with a BPF iterator

# 容器 D 状态任务数量的一分钟 EMA 阈值，超过该值时触发 dload 剖析。
- **ThresholdLoad**：容器不可中断睡眠（D 状态）任务数量的一分钟 EMA 阈值。
  默认 5。当 D 状态任务 EMA 超过该值时触发剖析。

### enable v2 load stats for metrics and dload

# cgroup v2 dload 使用 BPF task iterator，每次采样遍历宿主机全部任务。
# Kubernetes 部署必须设置 hostPID: true。
#
- **Cgroup v2**：随 `dload` 启用，cgroup v1 保持 netlink 路径。v2 实现要求内核 BTF 可读并支持 BPF `task` iterator；统计仅包含
  直接挂在目标 cgroup 下的任务，不递归包含子 cgroup。

	# loadavg 启用后自动后台采样，Prometheus 抓取读取缓存的
	# 容器负载及主机 D 状态计数。BPF iterator 要求 Kubernetes 设置 hostPID: true。
	[MetricCollector.Loadavg]

### estimate container load averages on cgroup v1 and v2

# cgroup v2 容器负载与主机 D 状态指标共用 BPF task iterator，默认每 15 秒
	# 遍历宿主机全部任务。不支持时仍保留宿主机 loadavg 和 cgroup v1 容器指标。
		# 后台采样间隔，单位秒；0 使用默认值 15。
		# 控制容器负载及主机 D 状态采样，不影响主机 /proc/loadavg 抓取，
		# 也不影响 AutoTracing.Dload.Interval（默认 10 秒）。
		# Interval = 15

### add host D-state counts using the shared task iterator

# 开启主机参与负载计算的 D 状态任务计数，每 Interval 秒采样。
		# 适用于 cgroup v1/v2，需要主机 PID 可见性、内核 BTF 和 BPF
		# task iterator，与 v2 容器统计共用遍历；不支持时省略指标。

### trigger host dload independently

#### 7.3 Dload 自动追踪 — 容器与主机 D 状态任务剖析
# cgroup v2 与主机 dload 共用 BPF task iterator，每次采样遍历宿主机全部任务。
	# HostThresholdLoad = 5
- **主机触发**：随 `dload` 启用，独立检测整机 D 状态负载。包含容器线程，
  不代表仅统计物理机服务。要求启用 `dload`、内核 BTF 可读、支持 BPF `task` iterator、
  具备 BPF 权限及主机 PID 可见性（Kubernetes 设置 `hostPID: true`）。支持 cgroup v1/v2，
  内核不支持 iterator 时无法提供主机触发，
  原有 v1 容器 netlink 路径仍可使用。

- **HostThresholdLoad**：整机 D 状态任务数量的一分钟 EMA 阈值，默认 `5`，
  超过阈值触发主机堆栈采集，主机独立维护冷却状态，
  冷却时长使用 `IntervalTracing`。复用 `Interval`（默认 10 秒）采样，
  不是 `/proc/loadavg` 的 R+D 负载，也不受 `MetricCollector.Loadavg.Interval` 控制。

### add host user and total CPU burst triggers

| 可选配置项 | 默认值 | 范围与行为 |
| --- | --- | --- |
| `UserThreshold` | `0`（%） | 0 关闭 user 触发，正值启用；用户态使用率须超过此值。 |
| `DeltaUserThreshold` | `0`（百分点） | 用户态使用率相较上一采样区间的增幅也须超过此值。 |
| `UsageThreshold` | `0`（%） | 0 关闭 total 触发，正值启用；实际执行 CPU 使用率须超过此值。 |
| `DeltaUsageThreshold` | `0`（百分点） | 实际执行 CPU 使用率相较上一采样区间的增幅也须超过此值。 |

#### 7.2 CPUSys 自动追踪 — 宿主机 CPU 突增
# Both thresholds of any trigger are exceeded (system, user or total).
	# UserThreshold = 0
	# DeltaUserThreshold = 0
	# UsageThreshold = 0
	# DeltaUsageThreshold = 0

**触发逻辑与前提**：须启用 `cpusys`、可读取主机 `/proc/stat`，且现有整机 perf
采集工具及其所需权限可用。每种触发条件都要求使用率与正向增幅同时超过对应阈值，
任意一种命中即可触发。三种条件共用采集流程与 `IntervalTracing` 冷却时间，
同时命中不会重复采集。使用率按整机 CPU 时间计算，包含容器工作量，不按容器配额归一化。
未配置 user/total 时，保持原有仅 system 触发的行为。四个新增阈值均须在 [0, 100] 范围内。
显式启用示例（使用率/增幅）：user 75/45，total 90/55。
