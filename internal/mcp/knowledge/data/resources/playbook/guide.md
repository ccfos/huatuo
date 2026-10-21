# MCP 使用与结果处理

## Tool 使用边界

- 只调用当前 MCP 服务公布的 Tool，以 inputSchema 和参数校验结果为准。
- `diagnostic_guide` 是知识入口，只接受 `stage`、`subsystem`，不要求查询范围。
  数据 Tool 必须显式提供精确 `region` 数组和 `time_range`，按需限定目标和结果数量。
- 内置知识不是部署能力清单。通过 `discover_tracers` 确认可用事件及 profiler 能力，
  不假定私有或历史 tracer、字段、指标在当前部署中存在。
- `discover_metrics` 默认返回候选指标名，不证明指定 region 内有数据。
  可用 `metric_names` 定向核验最多 5 个名称；标签及元数据以实际返回为准，
  不猜测 HELP、TYPE、UNIT，也不把累计计数的绝对值当成速率。
- 查询应有明确目标和时间范围。遇到截断或超限时缩小范围、减少返回量或分次查询，
  不进行无界模糊搜索，不将单次有界结果描述成全量数据。
- MCP 提供只读诊断能力，不自动执行系统配置修改、重启、安装或删除操作。

## Tool 结果与错误处理（每次调用后先检查）

- 先检查 MCP `isError` 和 JSON `status`，再解读事件、指标或 profiler。
  错误文本是 JSON，同时可通过 `structuredContent` 读取。
- `status=error` / `isError=true` 表示调用失败，不是“没有事件/指标”，不能据此判断
  子系统正常、容器没有异常或 ES/Prometheus 整个集群不可用。
- `error_class=invalid_argument`：根据 `message` 的具体参数名、类型、时间范围或
  PromQL 校验原因，参照 Tool inputSchema 修改参数后再调用；不得使用同一错误参数反复请求。
- 后端失败查看 `errors[]` 的 `region`、`backend`、`operation`、`error_class`、
  `error`、`retryable`、`suggestion`。保留关键原始原因并按建议处理。
  例如 `failed to parse date field` 是日期查询格式/mapping 不兼容，不能解释为集群故障；
  HTTP 401/403 检查认证权限，429 降低频率并等待，超时缩小范围，解码失败检查响应格式。
- `retryable=false`：停止原样重试，需要修正参数、服务端查询适配、配置或权限；
  `retryable=true` 也应遵循建议等待或缩小范围，避免无限重试。
- `backend_diagnostics` 保留各 backend/operation/region 的 `attempt_errors`。
  `recovered=true` 表示重试后已成功，历史错误不代表当前数据缺失；`recovered=false`
  表示该操作最终未成功，再结合最终结果的 `status`、`errors`、`warnings` 判断影响。
  记录截断时查看 `omitted_attempts` 和 `omitted_backend_diagnostics`。
- Count 失败后回退 Search 时可以使用返回的事件，但总数未知，不能把数量当成完整计数。
  多 tracer 分批失败的 `errors[].causes[]` 保留失败批次的 `tracers` 和底层原因。
  ES HTTP 404 明确返回错误，不等于查询成功但没有事件；需要核对索引或代理路由。
- Profile 分析失败时仍检查 `query_status[].warnings` 和 `errors`，保留 payload
  解析原因与其他 region 的后端错误。Prometheus 的非 JSON 错误响应保留有界 body，
  响应解码失败保留具体解析原因；不要只根据 HTTP 状态猜测根因。
- `status=partial`：仅分析成功返回的数据，列明失败 region 和具体原因；
  `warnings`、`truncated`、`query_status` 中的缺口不能当成“无异常”。
- 只有查询成功且对应结果为空，才能写“该查询未发现匹配数据”。
  发现失败不能解释为 tracer 不存在；指标核验 `availability=unknown` 不能写成 `absent`。
- 报告中把失败数据源标记为“查询失败/未核验”，说明错误和对结论的影响；
  可以继续使用其他成功数据源，但不得把缺失证据写成已确认根因。

## 证据与引用

- 保留实际查询的 region、目标、时间范围和返回的源记录定位，确保结论可复核。
- 聚合 Profile 不能冒充单条事件现场；需逐条比较时，使用返回的源 `record_id`
  调用 `query_profiles`，并保留相同的 region、目标、tracer 和时间约束。
- 知识库模式和跨子系统关联只是待验证的线索。事件同时出现不等于因果成立，
  应区分观测事实、推断与尚缺的证据。
