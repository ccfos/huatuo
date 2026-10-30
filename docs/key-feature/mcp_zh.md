---
title: MCP 诊断能力
type: docs
description: 面向 AI Agent 的只读事件、Profile 和指标查询
author: HUATUO Team
date: 2026-09-19
weight: 7
---

# MCP 诊断能力

`huatuo-apiserver` 通过 MCP 提供事件查询、profiling 数据分析、Prometheus 兼容指标和诊断指南。
所有能力均为只读，不触发采集、不修改配置，也不直接给出故障判定。

## 接入

- 入口：`/v1/mcp`，无状态 Streamable HTTP，与 API server 共用端口和 Bearer 认证。
- 默认关闭，通过 `[MCP] Enabled=true`、`--enable-mcp` 或 `ENABLE_MCP=true` 启用。
- 按客户端使用的方法，为非管理员授权 `POST /v1/mcp`、`GET /v1/mcp`、`DELETE /v1/mcp`。
- 事件读取 Elasticsearch/OpenSearch，指标读取 Prometheus 兼容 API。两类连接独立配置，
  不继承应用存储的地址或凭据；启动不探测后端。

连接、认证和预算配置见 [API server 配置](/docs/configuration/huatuo-apiserver-configuration_zh.md#9-model-context-protocol)。
实际可用工具及参数以 `tools/list` 返回的 `inputSchema` 为准。
下列 JSON 示例仅展示 `tools/call` 的 `arguments`，不是完整 JSON-RPC 请求；
目标、tracer 和指标名均需替换为实际值。

## 工具清单

> huatuo 采集到的上下文统称 events，时序指标统称 metrics。

| Tool | 用途 | 必填参数 |
|------|------|----------|
| `diagnostic_guide` | 获取诊断流程，始终注册 | 无；可传 `stage`、`subsystem` |
| `discover_event_types` | 发现 tracer 名称及 Profile 载荷标记 | `region`、`time_range` |
| `query_events` | 目标事件或区域聚合统计 | 见下文两种 scope |
| `query_event_profiles` | 分析 profiling 数据 | `region`、`time_range`、单个 `tracer_name`、目标条件 |
| `discover_metrics` | 发现指标候选名，或核验精确指标 | `region`、`time_range` |
| `query_metrics` | 目标 range 查询或区域 instant 统计 | 见下文两种 scope |

事件和指标工具仅在对应后端已配置时注册，不依赖启动时的连通性。
`diagnostic_guide.stage` 为 `overview`（默认）、`subsystem`、`correlation` 或 `profiler`；
`subsystem` 阶段还需指定 `cpu`、`memory`、`io`、`network` 或 `hardware`。

### 通用参数

- 数据工具的 `region` 均为非空精确名称数组，不支持 `all`、`_all` 或 `*`。
- `time_range` 支持 Go 时长（如 `1h`、`168h`，不支持 `7d`）、RFC3339 起始时间
  （结束为当前时间）或 `start/end`；`end` 也可为 `now`。区间必须为正。
- 一般查询最大跨度默认 30 天；`MCP.MaxQueryTimeRange=0` 关闭该上限，
  但不解除区域事件统计的 7 天上限和 PromQL selector 的 1 小时上限。
- 事件/Profile 的目标参数为 `host`、`container_host`、`container_hostnamespace` 数组，
  每组最多 100 个值。同字段内 OR，不同字段间 AND；独立的 host 和 container 目标应分开调用。

### 事件查询

`query_events` 默认 `scope="target"`，必须提供目标条件和 `tracer_name` 数组（最多 100 个）。
`size` 默认 200，受配置的文档预算限制，是所有 region 合计的返回预算，不是每个 tracer 的数量。
多 tracer 大结果查询按计数规划批次；返回的是有界样本，不保证每个 tracer 均被覆盖。

```json
{"region":["us"],"time_range":"1h","host":["node-a"],"tracer_name":["cpusys"],"size":20}
```

显式 `scope="region"` 用于统计和候选发现，不返回原始事件：

- 恰好一个 region，必填 `time_range` 和 `group_by`；目标和 tracer 过滤可选。
- `group_by` 从 `hostname`、`container_hostname`、`container_host_namespace`、
  `tracer_name` 中选择，不可重复。
- 时间跨度最多 7 天，同时受更小的全局上限约束。
- `size` 默认 500，上限 1000，与文档预算独立。
- 桶按分组键排序，**不是按数量取 Top-N**；返回 `count`、`first_seen`、`last_seen`。
  缺失分组值为 null，多值字段可使一条事件进入多个桶，计数不能直接当作机器数。
- `count_exact` 表示已返回桶的计数精确，不表示桶列表完整；超限标记截断，不自动翻页。
- 需要完整统一的 keyword 分组映射；不支持混合投影或 `_source` 扫描降级。
  每次搜索的后端超时最多 10 秒，且不超过 Tool 剩余预算。

```json
{"scope":"region","region":["us"],"time_range":"1h","group_by":["hostname","tracer_name"],"size":100}
```

找到候选后，新建目标请求：移除 `group_by`，将实际返回的 `hostname` 转为 `host`，
补充 `tracer_name` 数组，并用统计返回的 `start_time/end_time` 构造绝对时间范围。
`container_hostname` 对应 `container_host`，`container_host_namespace` 对应
`container_hostnamespace`；null 键不能作为目标。目标已知时无需先做区域统计。

### 事件发现与 profiling 分析

`discover_event_types` 返回 `tracers`。`has_profiler` 只表示发现了
`tracer_data.flamedata` 下的 frame/pprof 载荷标记，不保证载荷有效。
发现结果按 region 和时间范围字符串缓存 5 分钟；相对时间窗口可能复用旧结果，
不应当作实时完整清单。查询可直接指定未出现在发现结果中的 tracer。

`query_event_profiles` 接受一个字符串 `tracer_name`，分析事件中已存储的 profiling 数据
（`tracer_data.flamedata` 下的 frame 数组或有效 pprof 对象），不启动或触发 profiling 采集。
默认合并匹配记录；传 `record_id=source.record_id` 可分析单条记录，
此时只能指定一个 region，仍需目标和时间过滤；匹配多条记录会报错。

| 参数 | 默认值 | 上限 |
|------|-------:|-----:|
| `top_n` | 50 | 1000 |
| `max_call_paths` | 5 | 20 |
| `max_stack_depth` | 12 | 32 |

`analysis_type` 为 `self_time`（默认）或 `total_time`，按 exclusive/inclusive 权重排名。
调用路径从热点向调用者排列；`total_time` 包含 self 为零的路径，`self_time` 仅保留有 self 权重的路径。
检查 `summary.sample_type`、`sample_unit`，不要把 IRQ 计数等权重当作 CPU 时间；不兼容的类型/单位不能合并。
`coverage` 是返回热点的 self 权重占比，不是主机 CPU 利用率或查询完整度。
输入截断及无效载荷通过 `summary.truncated`、`unsupported_documents` 和 `query_status` 报告。

### 指标发现

`discover_metrics` 默认读取后端指标名目录，再按 `MetricPrefix` 本地过滤，返回
`availability="unknown"` 的候选名；名称存在不证明请求 region 有数据。

可传最多 5 个精确 `metric_names`，按指标名、region 和时间范围核验：

- `present`：找到时序；继续获取同范围的标签键和后端 HELP/TYPE/UNIT。
- `absent`：成功核验但未找到时序。
- `unknown`：核验失败，结果标记 partial，不等于不存在。

元数据接口不按 region/时间过滤；`metadata_status` 为 `available`、`unavailable` 或
`ambiguous_or_incomplete`。缺失或冲突时不推断单位。标签值需从查询结果读取。

### 指标查询

`query_metrics` 的两种 scope 使用不同参数：

| | `target`（默认） | `region` |
|---|---|---|
| 用途 | 已知目标的 range 证据 | 单 region 的 instant 聚合统计 |
| 时间 | 必填 `time_range`；不可传 `time` | 必填 RFC3339 `time`；不可传 `time_range` |
| 目标 | 恰好一个字符串 `host` 或 `container_host` | 不可传目标参数 |
| `step` | 默认 `1m`，最小 `15s` | 不可传 |
| PromQL | 每个 selector 包含同值精确目标 matcher | 每个 selector 使用明确指标名并位于聚合内 |
| 输出上限 | 配置的序列预算，超限截断 | 1000 与配置预算的较小值，超限报错 |

两种 scope 均要求 `query` 和 `region` 数组；调用方不可写 region matcher，由服务端注入。
非空 `MetricPrefix` 限制所有 selector 的指标名。PromQL 最长 4096 字节，matrix selector
最多回看 1 小时，不支持 subquery、`offset`、`@`；range 查询每序列最多 11000 个时间点。
区域统计额外限制 4 个 selector、100 个 AST 节点，不支持向量间二元运算，
回看还受更小的全局跨度上限约束；查询与重试共享最多 10 秒预算。

以下指标名和目标仅为示意，使用前替换为实际发现的名称和标签：

```json
{"region":["us"],"host":"node-a","time_range":"1h","query":"huatuo_cpu_usage{host=\"node-a\"}"}
```

```json
{"scope":"region","region":["us"],"time":"2026-10-27T08:00:00Z","query":"topk(20, avg by (host) (avg_over_time(huatuo_cpu_usage[5m])))"}
```

`[5m]` 是从 `time` 向前回看，不是扫描整个长时间段。跟进候选时移除 `time`，补充目标参数和
同值 matcher，用相同回看窗口的绝对 `time_range` 新建目标请求。小输出不保证低扫描成本，
后端仍需查询资源限制。区域事件/指标统计均返回 `purpose="statistics"` 和 `usage_hint`，
统计值、空结果或 NaN 不代表目标健康或不可用。

## 结果与错误

先检查 MCP `isError`，再读取 JSON `status`：

| 结果 | 含义与处理 |
|------|------------|
| `isError=true`、`status="error"` | 查询失败；检查 `error_class`、`message`、`errors`、`retryable` 和 `suggestion` |
| `status="partial"` | 仅使用已返回数据，并披露错误、warning 和截断造成的覆盖缺口 |
| `status="success"` | 调用成功；空结果仅表示本次查询未找到数据，不是健康结论 |

参数和后端错误以 JSON 文本和 `structuredContent` 返回。参数错误为 `invalid_argument`；
修正参数，不要原样重试。数据工具的 `query_status` 字段因工具而异；总数缺失表示未知。
多 region 文档/序列/发现预算均分配到各 region，预算小于 region 数时拒绝请求。

- 目标事件/Profile 查询及指标发现提供每个 region 的 `query_status`；指标查询保留后端响应和 warnings。
- 目标指标截断时，region 响应含 `mcp_truncated=true`，顶层标记 partial；区域指标统计不截断返回。
- Count 失败后可回退有界 Search，返回事件仍可用，但总数未知。
- ES 404、分片失败、无效响应均不是成功空结果。Prometheus 缺失/null 的 `result`、
  未知结果类型或无效 scalar/string 结构返回解码错误。
- `backend_diagnostics` 保留失败尝试；`recovered=true` 表示该操作最终成功，历史错误本身不代表数据缺失。
  最多保留 32 个操作、每操作 16 条失败尝试；省略数量会单独报告。
- 事件和 Profile 的 `source` 包含 `backend`、`record_id`、`region` 和可选 `locator`。
  MCP 不生成前端 URL，调用方可结合部署模板生成链接。

### 事件时间与兼容读取

时间过滤包含两端，`uploaded_timestamp`、`uploaded_time`、`time` 任一字段在范围内即可命中。
返回的 `timestamp` 按该顺序取首个非缺失、非 null 值，并据此倒序排列，因此可能在过滤区间外。
`observed_timestamp` 仅作元数据。支持日期字符串和 JSON 整数/数字字符串 `epoch_millis`，
数字始终按毫秒解释；载荷中的大整数保留精度。

无法使用索引精确过滤时，可回退有界 `_source` 扫描；候选超过 500 条即报错，
不返回伪完整结果。区域聚合不使用此降级。读取保留 alias 过滤和 routing，不创建 Scroll 游标。

## 文本指南与运维

- Resource 覆盖存储模型、子系统 tracer、故障模式和 Profile 方法；Prompt 为
  `auto-diagnosis`、`subsystem-diagnosis`。两者要求 host/container 目标、精确 region 和时间范围，
  专项 Prompt 还需 subsystem；Prompt 的 region 是逗号分隔字符串，不是数据工具的数组。
- 内嵌英文指南位于 `internal/mcp/guide/data`。`MCP.GuideOverlayDir` 按 `prompts/`、
  `resources/` 层级覆盖或扩展；现有文件下次读取生效，缺失文件回退内嵌版本。
  新增 Resource 需重启刷新目录，修改内嵌文件需重新编译；新增 Prompt 文件不会自动注册为 Prompt。
- `huatuo://data-manifest` 提供文件大小和 SHA256。
- `/metrics` 提供 MCP 请求、并发、Tool、后端尝试、重试和限流指标，不以查询、目标、region、tracer 或凭据作为标签。
