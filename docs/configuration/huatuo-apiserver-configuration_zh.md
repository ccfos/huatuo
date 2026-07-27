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

# huatuo-bamai Agent HTTP client configuration.
[Agent]
    # - HTTPPort
    # Agent HTTP server port.
    # Default: 19704
    #
    # - RequestTimeoutSeconds
    # Timeout in seconds for one Agent HTTP request.
    # Default: 10
    #
    # - StatusPollingIntervalSeconds
    # Interval in seconds between job status requests.
    # Default: 5
    #
    # - MaxConsecutiveStatusPollingErrors
    # Maximum consecutive status request errors before a job fails.
    # Default: 3
    #
    # HTTPPort = 19704
    # RequestTimeoutSeconds = 10
    # StatusPollingIntervalSeconds = 5
    # MaxConsecutiveStatusPollingErrors = 3
```

`StoreDSN` 是持久化任务状态的 SQLite 数据源。相对路径基于配置文件目录
解析。

Profiling 和 tracing 复用相同的配额结构，但保留独立配置值。两类任务的
资源开销和预期并发不同，统一限额会导致一类任务挤占另一类任务。

Agent 请求重试使用客户端内部默认值。公共配置仅保留 Agent 端口、单次请求
超时、状态轮询周期和连续失败阈值。

服务退出时不会停止 Agent 上的活动任务，而是打印任务标识和目标信息。新的
API 服务实例会恢复持久化的 `pending` 或 `running` 状态并继续监控。

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
    #         "GET /v1/traces",
    #         "GET /v1/traces/**",
    #         "GET /v1/profiles",
    #         "GET /v1/profiles/**",
    #     ]
```

- `ID` 是必填的稳定主体标识，会随任务持久化。
- `BearerToken` 是必填密钥，仅用于请求认证。
- `Admin` 授予全部路由权限，并忽略 `Permissions`。
- 非管理员必须配置 `Permissions`。权限可以仅包含路径，也可以包含 HTTP
  方法前缀；`*` 匹配单个路径段，`**` 匹配后续路径。

用户 ID 和 BearerToken 都必须唯一。轮换 BearerToken 不会改变任务归属，
因为 Token 不再作为用户 ID 使用或写入任务存储。

`/healthz`、`/readyz`、`/metrics` 和 `/version` 为公开路由。
`/debug/pprof/**` 和 `/v1/profiles/flamegraph/**` 仅管理员可访问。

### 7. 性能剖析

```toml
# Profiling subprocess configuration.
[Profiling]
    # - AggregationIntervalSeconds
    # Aggregation interval in seconds. Must be greater than 0 and less than
    # 1200.
    # Default: 10
    #
    # - MaxConcurrentProfilerProcesses
    # Maximum concurrent third-party profiler processes. A value of 0 disables
    # this process limit.
    # Default: 10
    #
    # - DashboardBaseURL
    # Optional dashboard base URL. Result URLs are omitted when empty.
    # Default: empty
    #
    # AggregationIntervalSeconds = 10
    # MaxConcurrentProfilerProcesses = 10
    # DashboardBaseURL = "https://grafana.example.com/d"
```

- `AggregationIntervalSeconds` 必须大于零且小于 1200。
- `MaxConcurrentProfilerProcesses` 限制第三方 profiler 子进程并发数。
  配置为零表示禁用该进程数限制，负数无效。
- `DashboardBaseURL` 可选；配置时必须使用 HTTP 或 HTTPS。为空时，已完成
  任务不生成结果 URL。

Agent 任务超时由请求的剖析持续时间加一个聚合周期自动计算，不再单独配置。
