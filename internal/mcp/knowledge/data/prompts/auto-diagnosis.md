# 全维度自动诊断

## 目标
用户模糊上报问题时，对目标自动进行多维度全覆盖排查。

## 前置知识
读取以下 resource 获取诊断所需的知识：
- `huatuo://storage-model` — 存储模型、核心原则、metric label 规则
- `huatuo://profiling/methodology` — 自动 profiler 热点分析方法

## 前置条件
- 目标：hostname（宿主机）或 container_hostname（容器），至少提供一个
- 精确 region 列表
- 用户指定的时间范围

## 目标类型判定
- 只有 hostname → host 类型
- 只有 container_hostname → container 类型
- 两者都有 → 同时排查

## 执行流程

### Tool 返回结果检查

每次调用后先检查 MCP `isError`、JSON `status` 和 `errors`/`warnings`，遵循
`huatuo://playbook/guide` 的“Tool 结果与错误处理”。`invalid_argument` 必须按
`message` 修改参数；`retryable=false` 不得原样重试。查询失败不是无数据，也不能
推断整个 ES/Prometheus 集群故障。`status=partial` 必须标明覆盖缺口。
报告将失败数据源标为“查询失败/未核验”，保留具体原因及结论限制。

### Phase 1: 读取子系统知识 + 发现可用数据源

依次读取各子系统的 tracer 元数据：
- `huatuo://subsystems/cpu/tracers`
- `huatuo://subsystems/memory/tracers`
- `huatuo://subsystems/io/tracers`
- `huatuo://subsystems/network/tracers`
- `huatuo://subsystems/hardware/tracers`

优先根据`问题分析行动参考原则` 确定各 subsystem 下对当前目标类型适用的 tracer及Prometheus指标。

tracer_name 和指标名可能随版本变更，需要动态发现：
- **必须先调用 discover_tracers(region=[...], time_range=用户指定范围)** 获取该范围的 tracer_name
  及其 `has_profiler` 标记
- 调用 `discover_metrics(region=[...], time_range=用户指定范围)` 获取该 region
  的前缀候选指标名（`availability=unknown`）。需要核对存在性和标签键时，
  再传入最多五个精确 `metric_names` 定向核验；`unknown` 不表示不存在。
  标签值从 `query_metrics` 结果读取

利用指标名约定和 `storage_model.md` 的 Metric Label 规则确定指标类型：
- 指标名含 `container_` 或 `hostnamespace` → 容器指标
- 指标名含 `host` 或明确为宿主机指标 → 宿主机指标
- 不确定时按 `storage_model.md` 的 label 规则构造 PromQL 试查

将知识库中的 tracer 与实际发现的对照：
- 匹配上 → 直接使用
- 未匹配 → 用描述语义匹配最接近的实际 tracer_name
- 发现知识库中不存在的 tracer → 可用于扩展排查范围

host 类型适用: target_type 含 host 的 tracer
container 类型适用: target_type 含 container 的 tracer

### Phase 2: ES 事件多维度扫描
**一次调用 query_events 传入所有适用 tracer_name**（size=3），不要逐个调用。
同时传入相同的 region 数组、目标数组和用户指定的 `time_range`。
统计各维度事件数，有事件 → 标记需深度分析。

若所有 tracer 均无事件，核对本次 `discover_tracers` 返回的 catalog，确认
tracer_name 和 region 是否正确。

### Phase 3: 有事件的维度深度分析
对每个有异常事件的维度，按 subsystem-diagnosis Prompt 执行完整诊断流程。

对 `has_profiler=true` 的 tracer：
1. 调用 `query_profiles` 获取自动抓取的热点分析；一次只传一个 tracer_name，
   region 和目标仍使用数组
   若一个时间窗口内存在多个关键异常事件，分别以各事件的 `source.record_id` 传入
   `record_id` 单独分析，然后对照合并结果，避免把先后热点归于同一次事件
2. 读取 `huatuo://profiling/methodology` 了解分析方法
3. 读取 `huatuo://profiling/kernel-signatures` 查询热点函数含义
4. 读取 `huatuo://subsystems/{subsystem}/patterns` 匹配根因模式

### Phase 4: Prometheus 辅助验证
无论 ES 是否有事件，都需要查询 Prometheus（配置的 MetricPrefix 前缀指标）。

使用 Phase 1 发现的指标名和 `storage_model.md` 的 Metric Label 规则构造 PromQL。
优先引用 `discover_metrics(metric_names=[...])` 对 `present` 指标返回的
HELP/TYPE/UNIT；若 `metadata_status` 为 `unavailable` 或 `ambiguous`，
明确说明单位/类型未知，不凭名称推断。
PromQL 必须包含一个精确目标 matcher，但不要写 region matcher；服务端会为每个
请求 region 注入精确 matcher。调用 `query_metrics` 时传入 region 数组、相同的
`time_range`，以及一个标量 `host` 或 `container_host`：

host 类型：
- PromQL 使用用户提供的精确 `host` label，并传入同值的 `host` 参数

container 类型：
- PromQL 使用用户提供的精确 `container_host` label，并传入同值的
  `container_host` 参数

读取 `huatuo://subsystems/{subsystem}/tracers` 获取各指标阈值，标记超出阈值的项。
若查询无数据，核对指标名、目标标签、region 及时间范围；目录确认该 region
有时序，但不保证用户指定的目标也有样本。

### Phase 5: 跨维度关联分析
检查多维度是否同时异常，读取 resource `huatuo://cross-domain` 获取跨子系统因果关系如：
- 内存异常 + CPU sys 异常 → 内存回收推高 sys
- IO 异常 + CPU 异常 → IO 推高 sys 或 iowait
- 网络丢包 + softirq 异常 → 软中断过载

### Phase 6: 输出统一诊断报告

记录所引用事件的 `source`、自动 profiler 的 `sources`，以及用于验证的
完整 Prometheus metric 名。若调用环境提供查看链接模板，由调用方 Skill 使用这些
定位信息生成链接；不要自行猜测 Grafana、Prometheus 或 Elasticsearch 地址。

## 自动诊断报告：{target}

**扫描概览**：

| 子系统 | ES 事件 | Prometheus | 状态 |
|--------|---------|-----------|------|
| CPU    | {有(N条)/查询成功但无数据/查询失败/部分结果} | {值或查询失败原因} | {正常/异常/未核验} |
| 内存   | ...     | ...       | ...  |
| IO     | ...     | ...       | ...  |
| 网络   | ...     | ...       | ...  |

**深度分析**：
{对异常维度按 subsystem-diagnosis 报告格式输出，有栈信息的需要包含栈信息的分析以及原始调用栈内容}

**关联发现**：
{跨维度因果链}

**数据源**：
- ES 事件：{列出引用的 tracer、事件时间及关键字段，供定位验证}
- Prometheus 查询：{列出引用的 PromQL 及对应时间点}

**建议**：
{按优先级排列的处置建议}
