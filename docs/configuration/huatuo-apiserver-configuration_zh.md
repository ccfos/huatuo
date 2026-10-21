---
title: huatuo-apiserver 配置说明
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 5
---

### 1. 概述

`huatuo-apiserver` 使用 TOML 配置文件并启用严格解析。未知或废弃的配置项
会阻止服务启动。以下被注释的配置使用所示默认值。

### 2. 日志与运行时资源限制

默认不创建 huatuo-apiserver 自身 cgroup。只有显式传入 `--enable-cgroup` 时，
本节 `[Runtime]` 配置才会生效；Kubernetes 和 systemd 部署应使用各自的资源管理配置。

```toml
# Log Configuration
[Log]
    # - Level
    # The log level for huatuo-apiserver: Debug, Info, Warn, Error, Panic.
    # Default: Info
    #
    # Level = "Info"

# Runtime limits for the huatuo-apiserver process.
[Runtime]
    # - CPULimitCores
    # CPU limit in cores.
    # Default: 20
    #
    # - MemoryLimitMiB
    # Memory limit in MiB.
    # Default: 4096
    #
    # CPULimitCores = 20
    # MemoryLimitMiB = 4096
```

- `Log.Level` 支持 `Debug`、`Info`、`Warn`、`Error` 和 `Panic`。
- `CPULimitCores` 以 CPU 核数限制 API 服务进程。
- `MemoryLimitMiB` 以 MiB 限制 API 服务进程。

资源限制必须大于零。命令行参数 `--log-debug` 的优先级高于
`Log.Level`。

### 3. HTTP 服务

```toml
# HTTP server configuration.
[APIServer]
    # - ListenAddress
    # Listen address in "host:port" form.
    # Default: ":12740"
    #
    # ListenAddress = ":12740"

    # Request rate limiting.
    [APIServer.RateLimit]
        # - RequestsPerSecond
        # Maximum process-wide request rate per second.
        # Default: 200
        #
        # - Burst
        # Maximum process-wide request burst.
        # Default: 200
        #
        # RequestsPerSecond = 200
        # Burst = 200
```

`ListenAddress` 使用 `host:port` 格式，主机为空表示监听所有网络接口。
`RateLimit` 是进程级令牌桶，两个值都必须大于零。HTTP 超时和请求大小限制
属于服务固定防护参数，不对用户开放配置。

### 4. 任务与 Agent 通信

```toml
# Job persistence.
[Jobs]
    # - StoreDSN
    # SQLite DSN. Relative paths are resolved from this file's directory.
    # Default: "jobs.db"
    #
    # StoreDSN = "jobs.db"

    # Job 记录使用内部修订号进行并发更新。仅支持当前服务版本写入的记录；
    # 启动本版本前，需要停止旧服务并删除或替换不兼容的数据库。

    # Profiling and tracing retain independent quotas because their resource
    # costs and expected concurrency differ.
    [Jobs.Profiling]
        # - MaxConcurrentPerHost
        # Maximum concurrent profiling jobs on one host.
        # Default: 3
        #
        # - MaxConcurrent
        # Maximum concurrent profiling jobs across all hosts.
        # Default: 500
        #
        # MaxConcurrentPerHost = 3
        # MaxConcurrent = 500

    [Jobs.Tracing]
        # - MaxConcurrentPerHost
        # Maximum concurrent tracing jobs on one host.
        # Default: 5
        #
        # - MaxConcurrent
        # Maximum concurrent tracing jobs across all hosts.
        # Default: 1000
        #
        # MaxConcurrentPerHost = 5
        # MaxConcurrent = 1000

    # Apiserver 独立维护的 Job 生命周期策略。
    [Jobs.Controller]
        # StatusPollIntervalSeconds = 5
        # PendingTimeoutSeconds = 30
        # CompletionGracePeriodSeconds = 60
        # NodeUnavailableGracePeriodSeconds = 30
        # JobRetentionPeriodHours = 720

# huatuo-bamai Agent HTTP client configuration.
[Agent]
    # - HTTPPort
    # Agent HTTP server port.
    # Default: 19704
    #
    # HTTPPort = 19704

    [Agent.Auth]
        # 必须与各 Node 的 HTTPServer.Auth.BearerToken 一致。
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
```

`StoreDSN` 是持久化任务状态的 SQLite 数据源。相对路径基于配置文件目录
解析。

Profiling 和 tracing 复用相同的配额结构，但保留独立配置值。两类任务的
资源开销和预期并发不同，统一限额会导致一类任务挤占另一类任务。

Agent 请求传输保护使用客户端内部默认值。Job 轮询、各阶段 deadline、Node
不可用宽限期和保留期由 `Jobs.Controller` 独立维护，不与 Node 运行时协商。

服务退出时不停止 Node 上的活动 Operation。新的 API 服务实例从持久化状态
恢复活动 Job 并继续监督，不重放 Start。

### 5. Elasticsearch/OpenSearch

```toml
# Optional Elasticsearch/OpenSearch backend for querying profiling data.
[Elasticsearch]
    # Address, Username, and Password must be configured together to enable
    # this backend.
    #
    # - Address
    # Elasticsearch or OpenSearch HTTP address.
    #
    # - Username
    # Elasticsearch or OpenSearch username.
    #
    # - Password
    # Elasticsearch or OpenSearch password.
    #
    # - Index
    # Index containing huatuo-bamai profiling data.
    # Default: "huatuo_bamai"
    #
    # Address = "https://elasticsearch.example.com:9200"
    # Username = "huatuo-apiserver"
    # Password = "REPLACE_WITH_STRONG_PASSWORD"
    # Index = "huatuo_bamai"
```

存储是可选能力。`Address`、`Username` 和 `Password` 必须同时为空或同时
配置。`Index` 默认为 `huatuo_bamai`，并且应与采集端索引一致。禁用存储
时，不注册原始 profile 和火焰图查询路由。

### 6. 认证与授权

```toml
# Authentication configuration.
[Auth]
    # - ID
    # Stable principal identifier stored with jobs.
    #
    # - BearerToken
    # Secret used only to authenticate requests. IDs and tokens must be unique.
    #
    # - Admin
    # Whether the principal has unrestricted API access.
    #
    # - Permissions
    # API method and path patterns granted to a restricted principal.
    #
    # Administrator example:
    # [[Auth.Users]]
    #     ID = "administrator"
    #     BearerToken = "REPLACE_WITH_RANDOM_HEX"
    #     Admin = true
    #
    # Restricted example:
    # [[Auth.Users]]
    #     ID = "huatuo-front"
    #     BearerToken = "REPLACE_WITH_ANOTHER_RANDOM_HEX"
    #     Permissions = [
    #         "GET /v1/tracing",
    #         "GET /v1/tracing/**",
    #         "GET /v1/profiling",
    #         "GET /v1/profiling/**",
    #     ]
```

- `ID` 是必填的稳定主体标识，会随任务持久化。
- `BearerToken` 是必填密钥，仅用于请求认证。
- `Admin` 授予全部路由权限，并忽略 `Permissions`。
- 非管理员必须配置 `Permissions`。权限可以仅包含路径，也可以包含 HTTP
  方法前缀；`*` 匹配单个路径段，`**` 匹配后续路径。

用户 ID 和 BearerToken 都必须唯一。轮换 BearerToken 不会改变任务归属，
因为 Token 不再作为用户 ID 使用或写入任务存储。

`/readyz`、`/metrics` 和 `/version` 为公开路由。
`/debug/pprof/**` 和 `/v1/profiling/flamegraph/**` 仅管理员可访问。

### 7. 性能剖析

```toml
# Profiling 结果链接配置。
[Profiling]
    # - DashboardBaseURL
    # Optional dashboard base URL. Result URLs are omitted when empty.
    # Default: empty
    #
    # DashboardBaseURL = "https://grafana.example.com/d"
```

- `DashboardBaseURL` 可选；配置时必须使用 HTTP 或 HTTPS。为空时，已完成
  或结果未知的任务不生成 Dashboard URL。

Profiler 执行和聚合参数只在 Node 本地配置中维护。

### 8. Model Context Protocol

MCP 当前提供只读访问，可接入支持 MCP 的任意 Agent，查询已有的异常
上下文、Profile 证据和时序指标。以下地址、索引及认证信息应替换为实际部署配置；
未配置的后端不注册对应数据 Tool，`diagnostic_guide` 始终可用。

```toml
[MCP]
    Enabled = false
    KnowledgeOverlayDir = ""

[MCP.Events]
    Type = "elasticsearch" # 或 "opensearch"

[MCP.Events.ESCompatible]
    Address = "http://127.0.0.1:9200"
    Index = "huatuo_events"
    AuthType = "none"

[MCP.Prometheus]
    Address = "http://127.0.0.1:9090"
    AuthType = "none"
    MetricPrefix = "huatuo_"
```

#### 接入与后端

- **入口**：`/v1/mcp` 使用无状态 Streamable HTTP，与 API server 共用端口和 Bearer
  认证。默认关闭，可通过 `Enabled=true`、`--enable-mcp` 或 `ENABLE_MCP=true` 启用。
  非管理员用户的 `Permissions` 需包含 `POST /v1/mcp`、`GET /v1/mcp`、`DELETE /v1/mcp`。
- **事件后端**：`escompatible` 适配已有 Elasticsearch/OpenSearch 数据，使用
  `[MCP.Events.ESCompatible]` 的地址和索引，独立于全局 `[Elasticsearch]`。
  `Type` 默认 `elasticsearch`；切换 OpenSearch 时填写 `opensearch`，连接节名称不变。
- **后端认证**：ES-compatible 使用 `AuthType="basic"` 配合 `Username`、`Password`；
  `AuthType="none"` 清除凭据。Prometheus 使用 `none`/空值匿名访问、`bearer` 配合
  `AuthToken`，或 `basic` 配合 `AuthUser`、`AuthPass`。后端凭据不返回给 MCP 客户端。
- **注册与启动**：配置采用严格解析；无效配置阻止启动。后端按有效配置注册，不在
  启动时探测连通性；运行时查询失败会明确返回，恢复后无需重启服务。

#### 查询预算

下表为默认值；标有上限的预算只能调低。`ToolTimeout`、`MaxQueryTimeRange` 单位为秒，
速率限制单位为后端请求/分钟。

| 配置项 | 默认值 | 约束与含义 |
|--------|-------:|------------|
| `MCP.MaxConcurrentRequests` | 128 | 单进程并发请求，上限 128 |
| `MCP.MaxQueryTimeRange` | 2592000 | 30 天；`0` 关闭跨度上限 |
| `MCP.MaxRegionsPerQuery` | 5 | 单次调用 region 数，上限 5 |
| `MCP.UserRateLimit` | 120 | 认证用户跨后端共享额度；`0` 关闭 |
| `MCP.Events.BackendRateLimit` | 300 | 事件后端共享额度；`0` 关闭 |
| `MCP.Events.MaxDocumentsPerQuery` | 500 | 事件/Profile 源文档总预算，上限 500 |
| `MCP.Events.ToolTimeout` | 60 | 事件/Profile Tool 总超时，必须为正 |
| `MCP.Prometheus.BackendRateLimit` | 300 | 指标后端共享额度；`0` 关闭 |
| `MCP.Prometheus.MaxSeriesPerQuery` | 3000 | 查询返回序列总预算，上限 3000 |
| `MCP.Prometheus.MaxMetricsPerDiscovery` | 800 | 指标发现总预算，上限 800 |
| `MCP.Prometheus.ToolTimeout` | 60 | 指标 Tool 总超时，必须为正 |

每次后端尝试（含重试）均计入额度，同类后端的默认连接与 region 连接共享预算。
临时错误在 Tool 总超时内最多重试 10 次；参数、认证、日期解析等永久错误不原样重试。
MCP 内部设置普通 HTTP 响应写 deadline 为两类 Tool 超时较大值加 5 秒，默认 65 秒；
GET 通知流清除写 deadline，退出时取消活跃请求并释放连接。

#### 多 region

先配置默认后端，再添加覆盖项。匹配顺序为精确名称、声明顺序的正则表达式、默认
后端；未设置字段只继承 MCP 内同类后端的默认连接与策略。

```toml
[[MCP.Events.Regions]]
    NamePattern = "^cn-"
    Type = "opensearch"
[MCP.Events.Regions.ESCompatible]
    Address = "http://cn-events:9200"

[[MCP.Prometheus.Regions]]
    NamePattern = "^cn-"
    Address = "http://cn-prometheus:9090"
    MetricPrefix = "huatuo_cn_"
```

ES-compatible region 使用 `AuthType="none"` 清除继承凭据；Prometheus region 可通过
显式空值清除 `AuthToken`、`MetricPrefix` 等策略，匿名访问也可设置 `AuthType="none"`。

#### 查询契约与结果

- 五个数据 Tool 均要求精确 `region` 数组和 `time_range`，支持 Go 时长、RFC3339
  起始时间或 `start/end`。事件/Profile 查询还需目标条件；`query_metrics` 要求单个
  `host` 或 `container_host`，PromQL 包含同值目标 matcher、满足 `MetricPrefix`，
  region 由服务端注入。
- PromQL 最小 step 为 15 秒，每序列最多 11000 个时间点，matrix range 最长 1 小时，
  不支持 subquery、offset 或 `@`。Profile 最多返回 1000 个热点，每热点 20 条路径、
  每路径 32 层。ES 解压后响应上限 8 MiB，Tool 结果上限 4 MiB。
- `discover_metrics` 默认返回 `availability=unknown` 的候选名称；可传入最多 5 个
  精确 `metric_names` 核验 `present`/`absent`/`unknown`，并获取可用标签及 HELP/TYPE/UNIT。
- 总结果预算按 region 分配；预算小于 region 数时拒绝请求。缓存、计数感知分批和
  有界并行查询控制查询成本，不创建 Scroll 游标或 bulk writer，也不触发新的采集。
- 查询失败返回 `isError=true` 和具体原因；部分结果返回 `status=partial`，保留
  `errors`、`warnings`、`query_status` 和截断标记。计数失败后的回退会说明总数未知，
  重试记录通过 `backend_diagnostics` 返回。失败或未知结果不能解释为“无数据”。

#### 文本知识库

`KnowledgeOverlayDir` 默认使用内嵌知识库。设置外部目录后，可按 `prompts/`、
`resources/` 层级更新 Markdown/YAML 文件；现有文件内容下次读取即生效，无需重新
编译。缺失文件回退到内嵌版本，新增 Resource 后重启以刷新注册目录。

Tool 清单、时间字段兼容规则和知识库用法见 [MCP 诊断能力](/docs/key-feature/mcp_zh.md)。
