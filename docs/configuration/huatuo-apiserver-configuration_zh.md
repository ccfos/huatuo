---
title: API 服务配置
type: docs
description:
author: HUATUO Team
date: 2026-07-27
weight: 5
---

### 1. 概述

`huatuo-apiserver.conf` 用于配置 huatuo-apiserver 的 HTTP 服务、任务管理、节点通信、认证和性能剖析数据查询。

本文引用仓库中的配置文件，并用表格说明字段。下文“默认值”指省略字段时程序提供的值；未注释的赋值会覆盖默认值，以 `#` 注释的赋值仅为示例，不参与加载，也不一定等于默认值。

启动前，必须替换 `Agent.Auth.BearerToken` 占位符，并在 `Auth.Users` 中配置至少一个用户。地址和凭据示例需按部署环境填写。

配置文件采用 TOML 严格解析，未知或废弃的字段会阻止启动。修改文件后需重启服务。

### 2. 日志

```toml
# Log Configuration
[Log]
    # - Level
    # The log level for huatuo-apiserver: Debug, Info, Warn, Error, Panic.
    # Default: Info
    #
    # Level = "Info"
```

| 字段 | 说明 |
| --- | --- |
| `Level` | 字符串，默认 `"Info"`。支持 `Debug`、`Info`、`Warn`、`Error` 和 `Panic`，不区分大小写；命令行参数 `--log-debug` 优先于此项。 |

### 3. 资源限制

默认不创建服务自身的 cgroup。传入 `--enable-cgroup` 后，以下限制才会生效；Kubernetes 和 systemd 部署应使用各自的资源管理配置。

```toml
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

| 字段 | 说明 |
| --- | --- |
| `CPULimitCores` | 整数，默认 `20`，单位为 CPU 核，必须大于 `0`。 |
| `MemoryLimitMiB` | 整数，默认 `4096`，单位为 MiB，必须大于 `0`。 |

### 4. HTTP 服务

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

| 字段 | 说明 |
| --- | --- |
| `ListenAddress` | 字符串，默认 `":12740"`。使用 `host:port` 格式，主机为空表示监听所有网络接口。 |
| `RateLimit.RequestsPerSecond` | 整数，默认 `200`，单位为请求/秒，必须大于 `0`。限制整个服务进程的请求速率。 |
| `RateLimit.Burst` | 整数，默认 `200`，必须大于 `0`。令牌桶容量，限制突发请求数量。 |

HTTP 超时和请求大小限制使用服务内置值，不提供配置项。

### 5. 任务管理

#### 5.1 任务存储

```toml
# Job persistence.
[Jobs]
    # - StoreDSN
    # SQLite DSN. Relative paths are resolved from this file's directory.
    # Default: "jobs.db"
    #
    # StoreDSN = "jobs.db"
```

| 字段 | 说明 |
| --- | --- |
| `StoreDSN` | 字符串，默认 `"jobs.db"`，不能为空。指定持久化任务状态的 SQLite 数据源；普通相对路径基于 `--config-dir` 指定的目录解析，`file:` URI 原样传递。 |

任务记录必须符合当前存储格式，包含有效修订号；不兼容的旧记录无法读取。升级前应确认旧数据库兼容性。

#### 5.2 并发配额

Profiling 和 Tracing 分别计数、独立限额。以下配额均为正整数，限制尚未结束的任务数量。

```toml
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
```

| 字段 | 说明 |
| --- | --- |
| `Jobs.Profiling.MaxConcurrentPerHost` | 每个节点的 Profiling 任务上限，默认 `3`。 |
| `Jobs.Profiling.MaxConcurrent` | 所有节点的 Profiling 任务总上限，默认 `500`。 |
| `Jobs.Tracing.MaxConcurrentPerHost` | 每个节点的 Tracing 任务上限，默认 `5`。 |
| `Jobs.Tracing.MaxConcurrent` | 所有节点的 Tracing 任务总上限，默认 `1000`。 |

#### 5.3 生命周期

`Jobs.Controller` 控制 API 服务对任务的状态轮询、超时处理和记录清理，独立于节点的 Operation 生命周期配置。以下值均为正整数。

```toml
    # Apiserver-owned Job supervision policy. These values are independent
    # from the Node operation lifecycle settings.
    [Jobs.Controller]
        # StatusPollIntervalSeconds = 5
        # PendingTimeoutSeconds = 30
        # CompletionGracePeriodSeconds = 60
        # NodeUnavailableGracePeriodSeconds = 30
        # JobRetentionPeriodHours = 720
```

| 字段 | 说明 |
| --- | --- |
| `StatusPollIntervalSeconds` | 状态轮询间隔，默认 `5` 秒。 |
| `PendingTimeoutSeconds` | 发起节点 Operation 前开始计时的启动等待期限，默认 `30` 秒。到期仍处于 Pending 时，进入停止流程。 |
| `CompletionGracePeriodSeconds` | 完成宽限期，默认 `60` 秒。确认任务运行后，执行期限为任务时长加此值；进入停止流程后，也使用此值等待节点结束。 |
| `NodeUnavailableGracePeriodSeconds` | 节点不可用的恢复宽限期，默认 `30` 秒。从首次发现不可用时计时，持续不可用并超期后任务失败。 |
| `JobRetentionPeriodHours` | 已结束任务记录的保留期，默认 `720` 小时（30 天）。从任务结束时计时，过期后定期清理。 |

服务退出不会停止节点上的活动 Operation。重启后从数据库恢复未结束任务并继续管理；已发起的 Operation 不会重复启动。

### 6. 节点通信

以下配置用于 API 服务访问 huatuo-bamai 节点，与客户端访问 API 服务所需的 `Auth.Users` 配置不同。

```toml
# huatuo-bamai Agent HTTP client configuration.
[Agent]
    # - HTTPPort
    # Agent HTTP server port.
    # Default: 19704
    #
    # HTTPPort = 19704

    [Agent.Auth]
        # Required service credential. It must match HTTPServer.Auth on every
        # huatuo-bamai Node.
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
```

| 字段 | 说明 |
| --- | --- |
| `HTTPPort` | 整数，默认 `19704`，范围为 `1`–`65535`。必须与节点 HTTP 服务端口一致。 |
| `Auth.BearerToken` | 字符串，无内置凭据，必填且不能包含空白字符。必须与各节点的 `HTTPServer.Auth.BearerToken` 一致；配置文件中的占位符需替换。 |

节点请求的传输超时等保护参数使用客户端内置值。

### 7. 认证与授权

至少配置一个 `[[Auth.Users]]`。以下用户配置均为注释示例，使用时需取消注释并替换凭据；配置多个用户时，每个用户必须使用不同的 ID 和 BearerToken。

```toml
# Authentication configuration.
[Auth]
    # /readyz, /metrics, and /version are public. Flame graph query
    # endpoints and /debug/pprof/** are restricted to administrators.
    #
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
    #     BearerToken = "REPLACE_WITH_RANDOM_HEX"
    #     Permissions = [
    #         "GET /v1/tracing",
    #         "GET /v1/tracing/**",
    #         "GET /v1/profiling",
    #         "GET /v1/profiling/**",
    #     ]
```

| 字段 | 说明 |
| --- | --- |
| `ID` | 字符串，必填且唯一。稳定的用户标识，随任务持久化。 |
| `BearerToken` | 字符串，必填且唯一。仅用于请求认证；轮换密钥不会改变任务归属。 |
| `Admin` | 布尔值，默认 `false`。为 `true` 时授予全部 API 权限，授权时不受 `Permissions` 限制。 |
| `Permissions` | 字符串数组，默认空，非管理员必填。每项为路径或 `HTTP方法 路径`；仅写路径时不限制方法。 |

路径中的 `*` 匹配一个路径段，`**` 匹配一个或多个后续路径段。例如，`GET /v1/profiling/**` 不包含集合路径 `GET /v1/profiling`，两者需分别授权。

`/readyz`、`/metrics`、`/version` 和 `/openapi.json` 无需认证。`/debug/pprof/**` 和 `/v1/profiling/flamegraph/**` 仅管理员可访问；pprof 接口还需通过 `--enable-pprof` 启用。

### 8. 性能剖析

#### 8.1 数据查询

Elasticsearch/OpenSearch 用于查询节点写入的剖析数据，与 `Jobs.StoreDSN` 中的任务记录分开存储。`Address`、`Username` 和 `Password` 必须同时为空或同时配置。

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
    # Address = "http://127.0.0.1:9200"
    # Username = "elastic"
    # Password = "REPLACE_WITH_PASSWORD"
    # Index = "huatuo_bamai"
```

| 字段 | 说明 |
| --- | --- |
| `Address` | 字符串，默认空。HTTP/HTTPS 地址，多个地址用逗号分隔。 |
| `Username` | 字符串，默认空。启用查询后端时必填。 |
| `Password` | 字符串，默认空。启用查询后端时必填，需替换示例占位符。 |
| `Index` | 字符串，默认 `"huatuo_bamai"`。启用查询后端时不能为空，需与节点写入剖析数据的索引一致。 |

未启用后端时，相关接口仍注册，但无法查询原始 Profile 和火焰图数据。

#### 8.2 结果链接

```toml
# Profiling result-link configuration.
[Profiling]
    # - DashboardBaseURL
    # Optional dashboard base URL. Result URLs are omitted when empty.
    # Default: empty
    #
    # DashboardBaseURL = "http://localhost:8006/d"
```

| 字段 | 说明 |
| --- | --- |
| `DashboardBaseURL` | 字符串，默认空，不生成 Dashboard URL。配置时必须为包含主机的 HTTP/HTTPS 地址；配置文件中的地址仅为示例。 |

结果链接仅在任务已结束、结果为已完成或未知，且查询后端确认数据已发布时生成。Profiler 的执行和聚合参数在节点配置中维护。
