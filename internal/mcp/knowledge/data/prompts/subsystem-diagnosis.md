# 单维度深度诊断

## 目标
对指定内核子系统的异常进行深度诊断，输出根因分析报告。

## 前置知识
读取以下 resource 获取诊断所需的知识：
- `huatuo://storage-model` — 存储模型、核心原则、metric label 规则
- `huatuo://subsystems/{subsystem}/tracers` — 该子系统的 tracer 元数据（key_fields、阈值、match_field、metric_keywords、related_tracers）
- `huatuo://profiling/methodology` — 自动 profiler 分析方法（`has_profiler=true` 的 tracer 需要）

## 前置条件
- 子系统：cpu | memory | io | network | hardware
- 目标：hostname（宿主机名）或 container_hostname（容器主机名）
- 精确 region 列表
- 用户指定的时间范围

## 执行流程

每次调用后先检查 MCP `isError`、JSON `status`、`errors` 和 `warnings`，遵循
`huatuo://playbook/guide` 的“Tool 结果与错误处理”。参数错误 `invalid_argument`
须按 `message` 修改后再查；`retryable=false` 不得原样重试。查询失败不等于无数据
或后端集群故障；部分结果须标注覆盖缺口，报告应说明未核验证据和具体失败原因。

### Step 1: 读取子系统知识
读取 resource `huatuo://subsystems/{subsystem}/tracers`，获取：
- 适用 tracer 及其 target_type 列表、match_field、has_profiler
- key_fields 及异常阈值
- related_tracers

### Step 2: 发现可用 tracer 和指标
tracer_name 和 Prometheus 指标名可能随版本变更。在查询前先确认当前实际可用的：
- **必须先调用 discover_tracers(region=[...], time_range=用户指定范围)** 获取该范围的 tracer_name
  及其 `has_profiler` 标记
- 调用 `discover_metrics(region=[...], time_range=用户指定范围)` 获取该 region
  的前缀候选指标名（`availability=unknown`）；需要核对存在性和标签键时，
  再传入最多五个精确 `metric_names` 定向核验，失败的 `unknown` 不等于不存在。
  具体标签值从 `query_metrics` 结果读取

将 Step 1 知识库中的 tracer 与实际发现的 tracer_name 对照：
- 匹配上 → 直接使用
- 未匹配 → 用描述语义匹配最接近的实际 tracer_name
- 发现知识库中不存在的 tracer → 可用于扩展排查范围

对于 Prometheus 指标，利用指标名约定和 `storage_model.md` 的 Metric Label 规则确定指标类型和 PromQL 构造方式：
- 指标名含 `container_` → 容器指标，使用用户提供的精确 `container_host`
- 指标名含 `host` 或明确为宿主机指标 → 宿主机指标，使用用户提供的精确 `host`
- 不确定时按 `storage_model.md` 的 label 规则试查

### Step 3: 获取 ES 异常事件
**一次调用 query_events 传入所有适用 tracer_name**，不要逐个调用：
- tracer_name: [{从 Step 2 确认的所有实际 tracer_name，必须是 discover_tracers 返回的}]
- region: [{用户提供的精确 region}]
- host: [{用户提供的目标主机名}]
- container_host: [{用户提供的目标容器名}]
- time_range: {用户指定}
- size: 5

无事件时核对本次 `discover_tracers` 返回的 catalog，确认 tracer_name 和
region 是否正确。

### Step 4: 获取自动 profiler 热点
对 `has_profiler=true` 的 tracer，调用 `query_profiles`：
- tracer_name: {同 Step 3 的 tracer，一次只查询一个}
- region: {同 Step 3 的 region 数组}
- host: {宿主机目标数组}
- container_host: {容器目标数组}
- time_range: {同 Step 3}
- top_n: 10
- analysis_type: "self_time"

获取自动 profiler 热点分析后，先读取 `huatuo://profiling/methodology` 了解分析方法，再读取 `huatuo://profiling/kernel-signatures` 查询热点内核函数的含义。

### Step 5: Prometheus 指标
无论 ES 是否有事件，都需要查询 Prometheus（部分异常未到阈值但指标可看到趋势）。

根据 Step 2 发现的指标和 `storage_model.md` 的 Metric Label 规则构造 PromQL：
- 用 tracer 的 metric_keywords 从 discover_metrics 结果中筛选相关指标
- PromQL 包含一个与请求参数相同的精确 `host` 或 `container_host` matcher
- PromQL 不写 region matcher，服务端为每个请求 region 自动注入
- 逐个调用 `query_metrics`，传入 region 数组、相同的 `time_range` 和一个标量目标

对比 abnormal_threshold 判断当前是否异常。

### Step 6: 关联检查
将 Step 1 中 related_tracers 按 Step 2 的动态发现结果映射为实际 tracer_name，只有当前 catalog 中存在的名称才调用 query_events；无法对应的名称标记为不可用，不猜测名称。

### Step 7: 根因匹配
读取 resource `huatuo://subsystems/{subsystem}/patterns`，将 profiler 热点函数与模式表匹配。

如有跨子系统关联，读取 resource `huatuo://cross-domain` 获取因果关系。

### Step 8: 输出诊断报告

记录所引用事件的 `source`、自动 profiler 的 `sources`，以及用于验证的
完整 Prometheus metric 名。查看链接由调用方 Skill 按部署环境生成，不要猜测
Grafana、Prometheus 或 Elasticsearch 地址。

## 诊断报告：{target} {subsystem} 异常

**当前状态**：{各关键指标值及是否异常}
**根因判定**：{匹配的模式名，未匹配则标注"未识别模式"}
**证据**：
- ES 事件：{各 tracer 事件数、事件时间及关键字段值}
  - **有栈信息的事件，必须将原始调用栈原文复制到报告中**（包括每个栈帧的函数名、偏移、文件名），不得自行概括或截断
- 自动 profiler：{top 热点函数及占比，含内核函数含义解释}
- Prometheus：{各指标值及异常判定}（PromQL：{查询表达式}，时间点：{timestamp}）
- 关联：{related_tracers 是否有事件}
**建议**：{匹配模式对应的典型原因及处置建议}
