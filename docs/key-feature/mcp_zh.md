---
title: MCP 诊断能力
type: docs
description: 面向各类 AI Agent 的异常上下文证据与指标查询接口
author: HUATUO Team
date: 2026-09-19
weight: 7
---

# MCP 诊断能力

`huatuo-apiserver` 通过 Model Context Protocol（MCP）提供数据与知识访问能力，
可接入支持 MCP 的任意 Agent。当前提供只读能力，用于获取类型化异常上下文证据、
已有 Profile 的热点与调用路径，以及有界指标查询结果，Agent 无需直接连接数据存储。

MCP 后端读取已有的事件、Profile 和时序指标数据，不直接调用采集器或触发新的采集。
服务通过并发配额、用户及后端限流、时间与目标过滤、结果上限、超时和有界重试控制
查询负载，并利用缓存、计数感知分批和有界 region 并行查询降低开销，减少对生产
业务和数据服务的影响。诊断流程和领域知识以文本维护，可按需更新。

## 架构

MCP 复用现有 API server，不启动第二个监听端口：

```text
MCP client -> Bearer 认证 -> /v1/mcp -> 协议服务
                                       -> escompatible -> 已有 Elasticsearch/OpenSearch 数据
                                       -> prometheus   -> 已有 Prometheus 兼容时序数据
```

传输层采用无状态模式，避免 sticky session 要求以及废弃 session 导致的
内存无界增长。服务为客户端兼容性注册 `POST`、`GET`、`DELETE`，但暴露的
操作当前均为只读。

## 能力

Resource 从内嵌知识库和配置的外部 overlay 目录自动发现，包括存储模型、排查流程、
子系统、跨领域关联和自动 profiler 指引。

Prompt：

- `auto-diagnosis`：多子系统诊断流程，要求精确 region 和时间范围。
- `subsystem-diagnosis`：CPU、内存、I/O、网络或硬件专项诊断流程，要求精确
  region 和时间范围。

Tool：

- `diagnostic_guide`：返回 `overview`、`subsystem`、`correlation` 或
  `profiler` 阶段的服务端诊断流程。
- `discover_tracers`：发现指定 region 和时间范围内的 tracer。
  `has_profiler` 表示发现了 `tracer_data.flamedata` 下的 frame 或 pprof 载荷特征，
  不保证每条载荷都有效；实际可分析性以 `query_profiles` 的结果和 warnings 为准。
- `query_events`：按 tracer、region、目标和时间范围查询事件，根据事件计数分批读取，
  结果受文档配额限制；每条事件包含原生 `source`，供调用方定位。
- `query_profiles`：将 adapter 规范化的自动 frame 聚合为有界 self/total 热点和调用路径，
  并返回每个成功解析源记录的 `source`；不读取手动 continuous profiling 文档。
  若需区分相邻异常，可传 `query_events` 返回的 `source.record_id`（仅一个 region），
  并保持目标、tracer 和时间过滤；结果只分析该记录。
  支持 frame 数组和有效 pprof 对象（含 IRQ Profile），无效载荷会报告错误或 warning。
  pprof 结果通过 `summary.sample_type`、`sample_unit` 标明权重类型和单位；
  不同采样类型或单位不混合聚合。Profile 输入截断时返回
  `summary.truncated` 和 `truncated_regions`；热点百分比只基于参与分析的输入，
  `coverage` 是 top_n 函数的 self 权重占比，不是查询完整度或主机 CPU 利用率。
- `discover_metrics`：先从 region 选定的后端按用户时间范围获取指标名，本地按
  配置前缀过滤，快速返回 `availability=unknown` 的候选名称；目录中的名称
  不代表该 region 一定有时序。可选传入最多五个精确 `metric_names`，逐项以
  指标名、region 和时间范围调用 `/series?limit=1` 核验 `present`、`absent`
  或失败时的 `unknown`。仅对 `present` 获取同范围的 label 键，并读取后端
  HELP/TYPE/UNIT；元数据接口不按 region 或时间范围过滤。缺失时标记
  `metadata_status=unavailable`，内容不完整或不一致时标记 `ambiguous`，不猜测单位。
  后端核验失败标记 partial，不把未知误判为不存在。
  `query_metrics` 仍强制精确 region 和目标条件。
- `query_metrics`：按一个或多个精确 region 校验并执行有界 range PromQL 查询。

`discover_metrics` 返回顶层 `truncated` 和每个 region 的 `query_status`，包含
`returned`、`truncated` 及可选 `warnings`。region quota 截断及后端 warning 都标记
为 partial。指标查询收到后端 warning 时，即使返回空结果或 scalar，也记录为 partial。
Profile 的 total-time 调用路径包含 self 为零的函数及其 inclusive samples；
self-time 调用路径仅包含有 self samples 的路径。

`diagnostic_guide` 始终注册；事件和指标 Tool 按有效的后端配置注册，不依赖启动时
后端是否可达。

MCP 只返回数据源定位信息，不拼接 Grafana、Prometheus 或 Elasticsearch 前端
URL。调用方 Skill 可将原生 `source`、metric 名、label 和查询范围
与部署环境的 URL 模板组合。

事件和 Profile 的 `source` 包含 `backend`、`record_id`、`region`，以及可选的
后端原生 `locator` 字段。

## 可更新的文本知识库

Resource 和 Prompt 使用 Markdown、YAML 等文本文件维护，覆盖存储模型、排查流程、
tracer 元数据、故障模式及 Profile 分析方法。可通过 `MCP.KnowledgeOverlayDir`
指定外部目录，按内嵌知识库的 `prompts/`、`resources/` 层级覆盖或扩展内容，
无需修改 Go 代码或重新编译。

已注册文件在每次请求时重新读取，修改外部文件后下次读取即生效；未覆盖的文件继续使用
内嵌版本。新增 Resource 文件后需重启以刷新注册目录；修改内嵌知识库需重新编译。
`huatuo://data-manifest` 提供文件大小和 SHA256，便于核对当前知识版本。

## 安全边界

### 向客户端明确返回错误

Tool 错误（包括参数校验失败）通过 MCP `isError=true` 返回。文本内容为 JSON 错误
结构，同时提供 `structuredContent`，包含 `status=error`、`tool`、`error_class`、
`message`、`retryable` 和 `suggestion`。参数错误标记为 `invalid_argument`，保留
具体校验原因，供 LLM 修改参数。后端错误的 `errors` 数组还包含 region、backend、
operation、具体原因、是否可重试及针对性建议。
数据 Tool 使用 `status=success` 和 `status=partial` 区分成功及部分结果；部分结果
仍保留具体错误和 warning。查询失败不等于没有匹配数据，也不代表整个后端不可用。
ES search/count/聚合的 HTTP 404 保留为错误，不转为空结果。分批查询失败通过
`errors[].causes[]` 保留 tracer 与底层原因；Count 回退 Search 保留事件，同时说明
Count 错误及总数未知。Profile 分析失败也保留 region 错误和 `query_status` warnings。
Prometheus 非 JSON 错误响应的有界 body、具体解码原因均返回，截断时明确标记。

`backend_diagnostics` 按 backend/operation/region 返回失败尝试记录。`recovered=true`
表示该操作最终成功，历史错误本身不意味着数据缺失；最终失败则同时保留尝试记录和
最终错误。最多保留 32 个操作、每操作 16 条失败尝试，保留最后一条并记录省略数量；
每条尝试错误最多 512 字节加截断标记。

### 查询边界与后端行为

MCP 默认关闭，启用后复用现有 API Bearer 鉴权。后端连接独立于应用存储配置，
不会自动继承应用的存储地址或凭据；启动不探测后端、不创建写入器。

**查询范围与配额**

- 五个数据 Tool 均要求 `time_range` 和非空、精确的 `region` 数组；
  `diagnostic_guide` 不要求查询范围。时间支持 Go duration（如 `168h`）、
  RFC3339 起始时间或 `start/end`。默认最大跨度为 30 天，
  `MCP.MaxQueryTimeRange=0` 可取消跨度限制。
- 硬上限为并发请求 128、每次调用 5 个 region、事件/Profile 文档 500 条、
  指标序列 3000 条、指标发现 800 项；部署配置可以调低配额。超时、响应大小、
  请求速率和重试也受限制，部分 region 失败时保留其他 region 的成功结果。
- 事件和 Profile 查询至少指定一组 `host`、`container_host` 或
  `container_hostnamespace` 数组；每组最多 100 个值。同字段内按 OR、不同字段间
  按 AND 组合。事件查询最多指定 100 个 tracer，Profile 查询只接受一个 tracer。
- 事件/Profile 查询及指标发现通过 `query_status` 提供各 region 的完整性信息，
  具体字段随 Tool 而异。总数缺失表示未知；独立计数和查询不保证属于同一快照。
  发现缓存不是完整数据清单，查询也可以直接指定尚未发现的 tracer。

**事件与 Profile**

- 查询 Elasticsearch/OpenSearch 中已有的数据，不修改 mapping、不创建采集任务或
  Scroll 游标，并保留配置的 alias 过滤和 routing。
- 无法使用索引精确过滤时，尝试有界 `_source` 扫描。每次扫描最多检查 500 条候选
  文档，超限明确报告数据不完整；可缩小时间和目标范围后重试。
- 时间过滤包含区间两端，`uploaded_timestamp`、`uploaded_time`、`time` 任一字段
  落在范围内即可命中。返回的 `timestamp` 优先取 `uploaded_timestamp`，再取
  `uploaded_time`、`time`，仅在前者缺失或为 null 时回退；因此返回时间可能不在
  过滤区间内。结果按该时间最新优先排序，`observed_timestamp` 仅保留为元数据。
- 时间戳支持日期字符串，以及 JSON 整数或数字字符串形式的 `epoch_millis`，
  数字固定按毫秒解释。事件载荷中的大整数在解码和返回时保留精度。
- 不按 `tracer_type` 排除记录。Profile 载荷需通过解析和采样类型、单位检查，
  不能仅凭发现标记认定可分析。输入被截断或存在无效载荷时，应检查 partial、
  `summary.truncated`、`truncated_regions` 和 warnings。

**指标查询**

- `query_metrics` 必须指定一个标量 `host` 或 `container_host`，每个 PromQL
  vector selector 都须包含相同的精确目标 matcher。调用方不得指定 region matcher，
  由服务端按 region 注入。
- `MetricPrefix` 非空时，每个 selector 的指标名都必须属于该前缀；为空时不限制
  前缀。Matrix selector 最长一小时，不支持 subquery、`offset` 和 `@` 修饰。
  PromQL 长度、step 和返回点数也受限制。
- 截断或后端 warning 会标记 partial。Prometheus 响应截断时还包含
  `mcp_truncated: true`；不要将部分结果当作全量数据。

`/metrics` 提供 MCP 请求、并发、Tool 调用、错误、耗时、响应大小、后端尝试、
重试和限流指标。标签使用固定低基数值，不包含查询、目标、region、tracer 或凭据。

配置和权限说明见 API server 配置文档。
