# Kata 文档与传输约定

Host 和 Guest 复用现有两类文档。新增 `source_type` 表示采集来源：`host` 或
`kata_guest`，由 Agent 模式配置给公共文档构造器。历史文档缺少来源时保持为空，
不推测为 Host；校验会拒绝其他来源值。

| 结果 | 文档及存储 | 对外输出 |
| --- | --- | --- |
| 内核事件 | tracing 文档，`tracing_documents` | 事件订阅、CloudEvents watch |
| tracing JSON，包括 cpuidle 火焰图、内存快照事件数据 | 同一 tracing 文档和存储 | 既有存储订阅；CloudEvents 仅输出 `tracer_type=event` |
| 有明确类型的 profiling 聚合窗口 | profiling 文档，`profiling_metadata` | 既有 profiling 查询及原始 profile 响应 |

分流依据是最终数据类型，不能只看“自动触发”。cpuidle 调用 `internal/tracing.Save`
保存 JSON；profiling 的 DocumentWriter 保存有类型的聚合窗口。传输不能把后者
改造成 `tracer_data`。两类存储都增加来源索引；CloudEvents 数据及原始 profile API
增加可选来源字段，OpenAPI 和生成代码保持一致。

两个文档包提供 `DecodeDocument`，读取单个 JSON 文档，拒绝 null、格式错误和
尾随 JSON 值，允许尾随空白。tracing 的异构数据使用 `json.Number`，保留超过
2^53 的计数及地址；profiling 继续使用原有整数类型。读取异构数据的调用方不能
再假设所有数字都是 float64。

解码只负责解析。接收端必须先认证连接，核对归属、重写不可信身份和文档 ID，
再调用对应 Store 的 Save/SaveSync，沿用其校验和订阅。`source_type` 只是描述字段，
不能用于认证。本阶段没有注册 Guest 监听端点。

JSON 直接使用文档本身，不另加元数据包裹。通用连接可通过握手工具名区分两类文档；
集成测试在本机 TCP 上使用 `tracing-document` 和 `profiling-document`。
产品中的 VSOCK 路由与身份认证由后续 GuestBridge 实现。

存储写入负责设置 `uploaded_timestamp`，Host 再次保存 Guest 文档时也由 Host 设置。
采集方提供的开始时间、用户态观测时间和可选内核观测时间保持原职责，不互相填补；
缺少内核时间时仍为空。各类观测的必填时间仍由 `tracer_type` 决定。

LocalFile 只接受 `tracer_name` 作为单个本地文件名。空名称、点目录、上级目录跳转、
绝对路径、两种路径分隔符和 NUL 在创建目录、查询缓存之前被拒绝。合法名称保留原有
文件格式和轮转行为；不同采集器并发创建文件时，缓存查询和插入共用锁。

可执行以下集成测试验证 TCP 到 JSON 解码、两类存储映射、tracing 文件写入及订阅：

```sh
go test -tags integration ./pkg/profiling/store \
  -run TestDocumentTransportIntegration -count=1 -v
```

profiling 使用记录后端验证写入，不代表真实 Elasticsearch 或 Kata/VSOCK 链路验收。
通信超时和内存预算见 [Toolstream 说明](toolstream_transport.md)。
