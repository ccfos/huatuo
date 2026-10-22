# 存储模型

## Elasticsearch — 异常事件数据

- 异常现场上下文：每个 tracer 检测到异常时记录完整事件文档（时间、主机/容器、关键字段值、重要调用栈等）
- 自动 profiler 数据：部分 autotracing tracer 在 `tracer_data.flamedata` 中附带
  frame 数组或 `{profile_type, profile}` pprof 对象，用于热点函数分析。reader 将
  两者规范化后交给 `query_profiles`；`has_profiler` 表示有该能力，不依赖 tracer_type。
- 查询方式：`query_events`（事件列表）、`query_profiles`（自动 profiler
  热点分析）、`discover_tracers`（可用 tracer 及 `has_profiler` 标记）
- `query_events` 的每条事件返回原生 `source`；
  `query_profiles.sources` 返回参与热点分析的记录定位及 region。调用方可将
  这些定位信息交给自己的 Skill 或提示词生成查看链接，MCP 不拼接前端 URL
- `query_profiles` 可以使用 `record_id` 精确分析一条事件对应的 Profile；
  不带该参数时汇总时间范围内所有匹配记录，不能把合并热点当作单次事件热点。

## Prometheus — Metrics 时序数据

- 服务端配置非空 MetricPrefix 时（默认 `huatuo_`），MCP 仅允许查询该前缀的 metrics；
  MetricPrefix 为空时不限制指标名前缀，region 和目标过滤仍然生效。
- 通用系统指标：如 cpu、network、memory、io 等
- huatuo 特有 metrics：如 qdiscs、mem claim、hw 等指标
- 查询方式：`query_metrics`（PromQL range 查询）、`discover_metrics`（选定后端
  按前缀筛选候选名；可选 `metric_names` 定向核验 region 和标签键）
- 默认 `discover_metrics` 返回完整 metric `name`（如 `huatuo_*`），
  `availability=unknown` 表示候选名，不能认定该 region 一定有时序。
  传入最多五个精确 `metric_names` 后，分别返回 `present`、`absent` 或核验失败时的
  `unknown`；仅 `present` 有可用 label 键。label 的具体值从 `query_metrics`
  结果中获取。查看链接由调用方生成
- 定向发现仅在后端提供元数据时返回 HELP/TYPE/UNIT；
  `metadata_status=unavailable` 表示后端没有提供，不能靠指标名猜测单位。
- 注意：Prometheus 只有时序指标数据，没有 tracer 事件数据；tracer 事件和自动 profiler 数据只能通过 ES 工具查询

## 核心原则

1. **ES-first**：ES 优先查询，包含了丰富的异常上下文
2. **Prometheus 前缀边界**：MetricPrefix 非空时只能查询该前缀的指标，不能自行选择
   查询前缀外指标；此类请求会被服务端拒绝。需要扩大范围时应由部署方调整服务端配置。
3. **region 精确匹配**：region 数组必须精确使用用户提供的值，严禁自行猜测、
   转换或编造变体。如果用户未指定 region，则要求用户补充。PromQL 中不要写
   region matcher，`query_metrics` 会为数组中的每个 region 自动注入
4. **因果一致性**：同一诊断中的所有查询（ES 事件、Prometheus 指标、自动 profiler）应使用相同的时间范围，确保因果推断有效。Agent 自行维护一致性，服务端不做强制校验
5. **数据源记录**：最终报告应包含引用的 `source`、tracer、事件时间、
   关键字段、Prometheus metric 名、PromQL 查询表达式及对应时间点。有栈信息的
   必须包含栈信息的分析以及原始调用栈完整内容，供验证和回溯

## Metric Label 规则

| 指标类型 | 包含 label | 查询方式 |
|---------|-----------|---------|
| 宿主机级别 | host, region | `host='{hostname}'` |
| 容器级别 | container_host, region, container_name, container_type, container_hostnamespace | `container_host='{container_hostname}'` |

指标名通常含 `container_` 的为容器级别指标。

ES 事件和自动 profiler 查询支持 `container_hostnamespace` 目标数组。
`query_metrics` 当前要求一个标量 `host` 或 `container_host`，不接受 namespace 目标。
