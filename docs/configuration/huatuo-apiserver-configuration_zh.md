---
title: huatuo-apiserver 配置
type: docs
description:
author: HUATUO Team
date: 2026-07-20
weight: 5
---

### 1. 文档概述

`huatuo-apiserver` 是 HUATUO 的 API 服务，负责提供任务管理、追踪与性能
剖析等接口。其配置文件用于定义日志级别、进程资源限制、服务监听地址、
任务并发限制、Elasticsearch/OpenSearch 后端、用户鉴权以及性能剖析参数。

本文档以仓库根目录的 `huatuo-apiserver.conf` 为准，逐项说明配置用途、
默认值及注意事项。配置文件采用 TOML 格式。以 `#` 开头的配置项为注释，
程序使用内置默认值；需要覆盖默认值时，应移除 `#` 并设置适合当前环境的
值。修改配置后需重启 `huatuo-apiserver` 才能生效。

**注意**：示例中的 Elasticsearch/OpenSearch 密码和用户 ID 仅用于说明，
生产环境必须替换，并避免将真实凭据提交到版本控制系统。

### 2. 日志配置

```toml
# LogLevel = "Info"
```

- **LogLevel**：日志级别。

  可选值为 `Debug`、`Info`、`Warn`、`Error` 和 `Panic`，默认值为 `Info`。

  **说明**：生产环境通常使用 `Info` 或 `Warn`。`Debug` 会输出更详细的
  调试信息，适合临时排查问题，但可能增加日志量。

### 3. 运行时资源限制

```toml
[RuntimeCgroup]
    # LimitCPU = 20
    # LimitMem = 4096
```

- **LimitCPU**：CPU 资源上限。

  默认值为 `20`，单位为 CPU 核数。

  **说明**：用于限制 `huatuo-apiserver` 进程的 CPU 使用量。应结合节点
  容量和 API 请求负载调整。

- **LimitMem**：内存资源上限。

  默认值为 `4096`，单位为 MB。

  **说明**：程序加载配置后会将该值换算为字节并应用于运行时资源限制。
  设置过小可能导致高并发查询或任务调度期间内存不足。

### 4. API 服务配置

```toml
[APIServer]
    # TCPAddr = ":12740"
```

- **TCPAddr**：API 服务监听地址。

  默认值为 `:12740`，格式为 `主机:端口`。主机部分为空表示监听所有网络
  接口。

  **说明**：如只允许本机访问，可配置为 `127.0.0.1:12740`。对外暴露
  服务时，应同时配置防火墙、反向代理和访问控制。

### 5. 任务调度配置

```toml
[TaskConfig]
    # MaxProfilingTasksPerHost = 3
    # MaxTracingTasksPerHost   = 5
    # MaxTotalProfilingTasks   = 500
    # MaxTotalTracingTasks     = 1000
```

- **MaxProfilingTasksPerHost**：单台主机允许同时执行的性能剖析任务上限。

  默认值为 `3`。

  **说明**：限制单机性能剖析并发，避免多个 CPU 或内存剖析任务同时运行
  时影响业务进程。

- **MaxTracingTasksPerHost**：单台主机允许同时执行的追踪任务上限。

  默认值为 `5`。

  **说明**：应根据主机性能和所启用追踪器的开销设置。

- **MaxTotalProfilingTasks**：集群内性能剖析任务总上限。

  默认值为 `500`。

  **说明**：用于控制整个集群中的性能剖析任务并发量，防止 API 服务和
  后端存储因突发任务过载。

- **MaxTotalTracingTasks**：集群内追踪任务总上限。

  默认值为 `1000`。

  **说明**：该限制与单主机限制共同生效。生产环境应根据集群规模和后端
  容量调整。

### 6. 存储配置

```toml
[ElasticSearch]
    Address  = "http://127.0.0.1:9200"
    Username = "elastic"
    Password = "huatuo-bamai"
    Index    = "huatuo_bamai"
    Debug    = false
```

`huatuo-apiserver` 使用 Elasticsearch/OpenSearch 查询 `huatuo-bamai` 产生
的追踪和事件数据。`Address`、`Username` 或 `Password` 中任意一项为空时，
该存储后端将被禁用。

- **Address**：Elasticsearch/OpenSearch 服务地址。

  无内置默认值，需填写包含协议和端口的完整 URL，例如
  `http://127.0.0.1:9200` 或 `https://127.0.0.1:9200`。

  **说明**：生产环境建议使用 HTTPS，并确保 API 服务能够访问该地址。

- **Username**：认证用户名。

  无默认值。

  **说明**：应使用权限最小化的服务账号，仅授予目标索引所需的访问权限。

- **Password**：认证密码。

  无默认值。

  **说明**：不得沿用示例密码。应通过部署系统安全注入配置文件，并限制
  文件读取权限。

- **Index**：数据索引名称。

  无内置默认值，示例值为 `huatuo_bamai`。

  **说明**：应与 `huatuo-bamai.conf` 中的存储索引保持一致，否则 API
  服务无法查询采集端写入的数据。

- **Debug**：Elasticsearch/OpenSearch 客户端调试日志开关。

  默认值为 `false`。

  **说明**：启用后会输出更详细的请求和响应信息，仅建议在排查后端连接
  或查询问题时临时使用。

### 7. 认证与授权配置

每个用户使用一个 `[[Auth.users]]` 数组表声明。可配置多个用户，例如：

```toml
# 管理员：拥有全部接口权限，Permissions 将被忽略。
[[Auth.users]]
    ID      = "REPLACE_WITH_RANDOM_HEX"
    Name    = "Administrator"
    IsAdmin = true

# 普通用户：仅允许访问追踪和性能剖析接口。
[[Auth.users]]
    ID          = "REPLACE_WITH_RANDOM_HEX"
    Name        = "huatuo-front"
    IsAdmin     = false
    Permissions = [
        "/v1/traces",
        "/v1/traces/**",
        "/v1/profiles",
        "/v1/profiles/**",
    ]
```

- **ID**：用户唯一标识和请求认证凭据。

  无默认值。

  **说明**：服务根据客户端请求携带的 ID 查找用户。该值应视为敏感凭据，
  使用足够随机的字符串，例如通过 `openssl rand -hex 16` 生成。不同用户
  不得共用 ID。

- **Name**：用户显示名称。

  无默认值，仅用于标识用户，不参与权限判断。

- **IsAdmin**：管理员标志。

  默认值为 `false`。设置为 `true` 时，该用户可以访问全部接口，
  `Permissions` 配置将被忽略。

  **说明**：管理员账号应严格控制数量，普通调用方应使用明确的权限列表。

- **Permissions**：允许访问的 URL 路径模式列表。

  无默认值，仅在 `IsAdmin = false` 时生效。支持完整路径和通配路径，
  `**` 匹配其所在位置之后的任意内容，例如 `/v1/traces/**`。

  **说明**：访问集合路径和其子路径通常需要分别声明，例如同时配置
  `/v1/traces` 和 `/v1/traces/**`。应遵循最小权限原则，只开放调用方
  必需的接口。

### 8. 性能剖析配置

```toml
[Profiling]
    # AggregationInterval  = 10
    # ExecutionTimeout     = 20
    # MaxProfilerProcs     = 10
    # FlameGraphBaseURL     = "http://localhost:8006/d"
```

- **AggregationInterval**：性能剖析数据的聚合与上报周期。

  默认值为 `10`，单位为秒。有效范围为 `1` 到 `1199`。

  **说明**：该值对应 profiler 的 `--aggr-interval` 参数，同时用于调度
  连续性能剖析任务。间隔越短，结果更新越及时，但会增加聚合、上报和
  存储开销。

- **ExecutionTimeout**：单个 profiler 子进程的执行超时时间。

  默认值为 `20`，单位为秒，且不得小于 `AggregationInterval` 的两倍。

  **说明**：该值限制 profiler 子进程的最长运行时间，不代表整个性能
  剖析任务的持续时间。配置不满足约束时，API 服务将拒绝启动。

- **MaxProfilerProcs**：第三方性能剖析工具的最大并发进程数。

  默认值为 `10`。

  **说明**：该值用于限制外部性能剖析进程并发，避免耗尽 CPU、内存或
  进程资源。该值不能为负数，设为 `0` 表示不限制。

- **FlameGraphBaseURL**：火焰图仪表盘基础 URL。

  默认值为 `http://localhost:8006/d`。

  **说明**：API 服务会在该地址后追加仪表盘标识，生成单次追踪对应的
  火焰图链接。部署独立可视化服务时，应修改为客户端可访问的地址。
  地址必须使用 HTTP 或 HTTPS，并包含主机名。

### 9. 配置示例

以下示例展示一个启用后端存储、管理员账号和普通只读账号的基础配置：

```toml
LogLevel = "Info"

[RuntimeCgroup]
    LimitCPU = 20
    LimitMem = 4096

[APIServer]
    TCPAddr = ":12740"

[TaskConfig]
    MaxProfilingTasksPerHost = 3
    MaxTracingTasksPerHost = 5
    MaxTotalProfilingTasks = 500
    MaxTotalTracingTasks = 1000

[ElasticSearch]
    Address = "https://elasticsearch.example.com:9200"
    Username = "huatuo-apiserver"
    Password = "REPLACE_WITH_STRONG_PASSWORD"
    Index = "huatuo_bamai"
    Debug = false

[[Auth.users]]
    ID = "REPLACE_WITH_RANDOM_HEX"
    Name = "Administrator"
    IsAdmin = true

[[Auth.users]]
    ID = "REPLACE_WITH_ANOTHER_RANDOM_HEX"
    Name = "huatuo-front"
    IsAdmin = false
    Permissions = [
        "/v1/traces",
        "/v1/traces/**",
        "/v1/profiles",
        "/v1/profiles/**",
    ]

[Profiling]
    AggregationInterval = 10
    ExecutionTimeout = 20
    MaxProfilerProcs = 10
    FlameGraphBaseURL = "https://grafana.example.com/d"
```

部署前应替换示例中的密码、用户 ID、后端地址和火焰图地址，并根据主机与
集群容量调整资源及任务并发限制。
