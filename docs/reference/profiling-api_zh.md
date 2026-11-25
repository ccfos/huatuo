---
title: Profiling API
type: docs
author: HUATUO Team
date: 2026-10-21
weight: 5
---

本文列出性能剖析任务的请求、响应和状态约定。首次采集见[性能剖析实践](/docs/best-practice/continuous-profiling_zh.md#快速开始)。

huatuo-apiserver 通过 `/v1/profiling` 创建、监督和停止 Profiling Job。每次 HTTP
创建请求都是一次独立执行。Job 持久化在 huatuo-apiserver，Node 只维护内存中的
Operation 和采集工具进程。

Job 控制不依赖 Elasticsearch；查询原始结果和生成 Dashboard 链接时才要求配置
Elasticsearch。Node 和 Apiserver 必须使用同一索引。

## 请求约定

huatuo-apiserver 默认监听 `:12740`。以下示例使用环境变量统一设置服务地址和 Bearer token：

```bash
API_BASE="http://127.0.0.1:12740"
API_TOKEN="REPLACE_WITH_RANDOM_HEX"
```

每个请求必须在 `Authorization` 请求头中传入配置的 Bearer token：

```text
Authorization: Bearer REPLACE_WITH_RANDOM_HEX
```

非管理员用户需要配置 `/v1/profiling` 和 `/v1/profiling/**` 权限。权限可带
HTTP 方法前缀，例如 `GET /v1/profiling/**`。成功响应仅包含 `data`：

```json
{
  "data": {}
}
```

错误响应包含稳定错误码和可读消息：

```json
{
  "error": {
    "code": "result_unavailable",
    "message": "Job state does not provide a complete Profiling result"
  }
}
```

客户端应根据 `error.code` 分支处理，不应解析 `error.message`。

## 查询能力

创建任务前，建议先查询服务端支持的剖析类型、语言、CPU 模式、内存模式和运行参数：

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/capabilities"
```

`data.items` 是随版本发布的静态能力表，每项包含：

| 字段 | 说明 |
| --- | --- |
| `type` | 剖析类型：`cpu`、`memory` 或 `irq` |
| `language` | CPU、Memory 的目标语言；IRQ 不返回此字段 |
| `modes` | 此类型与语言组合允许的 `mode` |
| `supports_binary_match` | 是否允许 `binary_match_path` |
| `supported_scopes` | 允许的 `scope` |

当前 `c`、`c++` 和 `go` CPU 剖析支持 `oncpu`、`offcpu`，`java` 和
`python` 仅支持 `oncpu`。内存剖析支持以下组合：

| 语言 | `mode` | 说明 |
| --- | --- | --- |
| `c`、`c++`、`go` | `virtual_alloc` | 虚拟地址空间分配 |
| `c`、`c++`、`go` | `physical_alloc` | 物理页分配 |
| `c`、`c++`、`go` | `physical_usage` | 本次采样跟踪的物理页存活字节数 |
| `java` | `object_alloc` | JVM 对象分配 |
| `java` | `object_usage` | JVM 存活对象 |

## 创建任务

`POST /v1/profiling` 的 JSON 参数如下：

| 参数 | 是否必需 | 说明 |
| --- | --- | --- |
| `type` | 是 | 剖析类型：`cpu`、`memory` 或 `irq` |
| `language` | CPU、Memory 必需 | 目标进程语言；IRQ 不得传入 |
| `mode` | 是 | 能力表为对应类型和语言声明的模式 |
| `duration_seconds` | 是 | 采集时长，单位为秒 |
| `hostname` | 是 | 运行目标进程的节点主机名，用于任务调度 |
| `scope` | 是 | 能力表允许的 `host` 或 `container` |
| `container_id` | container 范围必需 | 目标容器 ID |
| `binary_match_path` | 否 | 能力表允许时使用的可执行文件路径匹配条件 |
| `host_cpu_list` | 否 | 目标宿主机的逻辑 CPU 列表，如 `"1,3-10"`；省略或空字符串表示全部 CPU |

`host_cpu_list` 使用目标宿主机的逻辑 CPU 编号，同时适用于 `host` 和 `container` scope。容器条件与 CPU 条件取交集，不改变容器 cpuset 或进程亲和性。支持单个编号、逗号列表及包含两端的范围；去重、排序并合并相邻编号后保存。允许首尾空白，不允许内部空白、负数、倒序范围或空片段。输入最多 65536 字节，各片段展开后的条目累计最多 65536 个（去重前），编号为 0～2147483647；目标节点启动时检查指定 CPU 是否在线。

原生 CPU 的 oncpu/offcpu 支持多个 CPU；offcpu 按线程切出 CPU 归属。IRQ 当前仅支持单 CPU，等价单元素写法 `"1,1"`、`"1-1"` 也允许。Java、Python 和 memory 暂不支持指定 CPU。合法集合不因后端限制而在创建时拒绝；底层不支持时任务进入失败终态，通过 `terminal.message` 返回具体原因。

即使请求内容相同，每次请求也会创建一个新 Job。超过每节点或进程级活动 Job
配额时返回 HTTP 429。

创建宿主机 Go CPU 剖析任务：

```bash
curl -sS -i \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "cpu",
    "language": "go",
    "mode": "oncpu",
    "scope": "host",
    "duration_seconds": 60,
    "hostname": "node-01"
  }' \
  "${API_BASE}/v1/profiling"
```

创建容器内 Java 存活对象剖析任务：

```bash
curl -sS -i \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "memory",
    "language": "java",
    "mode": "object_usage",
    "scope": "container",
    "duration_seconds": 60,
    "container_id": "9f4c2f1a8b7d",
    "hostname": "node-01"
  }' \
  "${API_BASE}/v1/profiling"
```

创建宿主机 softirq 剖析任务：

```bash
curl -sS -i \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"hostname":"node-01","duration_seconds":10,"scope":"host","type":"irq","mode":"softirq"}' \
  "${API_BASE}/v1/profiling"
```

IRQ 仅支持 `scope=host`、`mode=softirq`，不传 `language`、`container_id`
或 `binary_match_path`。默认采集全部 CPU，可用 `host_cpu_list` 指定单个 CPU；source、victim 各限 500 样本/CPU/秒，
不提供限速参数。结果在结束后一次发布，profile type 为
`irqtracing:irq:count:irq:count`，单位为次数；不采集硬中断调用栈。

创建成功返回 `201 Created`，`Location` 响应头指向新任务，响应体包含后续查询所需的任务 ID：

```json
{
  "data": {
    "request_id": "<profile-job-id>",
    "hostname": "node-01",
    "duration_seconds": 60,
    "scope": "host",
    "type": "cpu",
    "language": "go",
    "mode": "oncpu",
    "status": "pending",
    "created_at": "2026-08-24T10:00:00Z",
    "updated_at": "2026-08-24T10:00:00Z"
  }
}
```

```bash
JOB_ID="<profile-job-id>"
```

## 查询任务

**任务列表**

`GET /v1/profiling` 支持以下查询参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `limit` | `100` | 每页数量，取值范围为 1 到 1000 |
| `offset` | `0` | 起始偏移量，必须大于或等于 0 |

查询最新的 20 个 Profiling Job：

```bash
curl -sS -G \
  -H "Authorization: Bearer ${API_TOKEN}" \
  --data-urlencode "limit=20" \
  --data-urlencode "offset=0" \
  "${API_BASE}/v1/profiling"
```

服务端固定只返回 Profiling Job，并按 `created_at` 倒序排列。`data.items`
是任务数组，`data.limit` 和 `data.offset` 是实际使用的分页参数；
`data.has_more` 表示是否还有下一页。非管理员只能查看自己创建的任务。

**任务详情**

`GET /v1/profiling/:request_id` 返回单个任务：

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/${JOB_ID}"
```

任务信息位于 `data` 字段：

| 字段 | 说明 |
| --- | --- |
| `request_id` | 后续所有请求使用的 Profiling Job ID |
| `container_id` | 目标容器 ID；宿主机任务不返回该字段 |
| `hostname` | 目标节点主机名 |
| `duration_seconds` | 请求的剖析时长，单位为秒 |
| `scope` | `host` 或 `container` |
| `type` | `cpu`、`memory` 或 `irq` |
| `language` | 目标进程语言；IRQ 不返回此字段 |
| `mode` | 从能力表中选择的剖析模式 |
| `binary_match_path` | 可执行文件匹配路径；未使用时不返回该字段 |
| `host_cpu_list` | 规范化后的 CPU 选择条件；未限制时不返回该字段 |
| `status` | 当前任务状态 |
| `terminal` | 终态详情 `{outcome, reason, message}`；非终态不返回 |
| `created_at` | 任务创建时间 |
| `updated_at` | Job 状态最后更新时间 |
| `started_at` | 实际开始执行时间；尚未观察到运行时不返回 |
| `ended_at` | 进入终态的时间；运行期间不返回 |
| `result_url` | 完整结果可用时，由单任务详情接口返回的 Dashboard 链接 |

任务状态如下：

| 状态 | 说明 |
| --- | --- |
| `pending` | 任务已创建，正在等待 Agent 执行 |
| `running` | Agent 正在采集剖析数据 |
| `stopping` | 停止意图已持久化，正在异步执行停止操作 |
| `terminal` | 任务已结束，具体结果见 `terminal.outcome` |

`terminal.outcome` 可取 `completed`、`failed`、`stopped` 或
`unknown`。`failed` 时通过 `terminal.reason` 和 `terminal.message` 定位原因；
`stopped` 表示主动停止，结果可能不完整；`unknown` 表示 Apiserver 无法确认
Operation 最终状态，但持久发布的结果仍可能可用。

`operation_lost` 和 `execution_timed_out` 是 `terminal.reason` 的原因码，不是 Job
状态。其他失败原因包括等待启动或停止超时、Node 不可用、执行容量超过上限、
启动或执行失败、表示 Apiserver 构造的 Node 请求无效的
`invalid_node_request`，以及协议错误。

## 获取结果

`GET /v1/profiling/:request_id/raw` 仅在持久发布标记仍存在时返回
`terminal.outcome` 为 `completed` 或 `unknown` 的 Job 原始剖析窗口。发布标记
不存在或已经过期时返回 `result_not_found`。活动状态返回 `result_not_ready`；
`failed` 和 `stopped` 返回
`result_unavailable`。

数据量可能较大，可以直接保存到文件：

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -o profile-raw.json \
  "${API_BASE}/v1/profiling/${JOB_ID}/raw?limit=100&offset=0"
```

剖析窗口位于响应体的 `data.items` 字段；`data.limit`、`data.offset`
和 `data.has_more` 描述分页。每条记录包含 `uploaded_timestamp`、`started_timestamp`、
`profile_type` 和兼容 pprof 的 `profile` 数据。
可选 `dropped_samples` 按原因记录丢样数，零值可省略。`collection` 非零表示
限速或采集容量不足导致结果不完整。Grafana 时间线右轴显示该计数，标签筛选只作用于火焰图。
已持久发布但内容为空的结果仍返回成功，`items` 为空数组。显式 `limit=0` 返回参数
错误；只有未传 limit 才使用默认值。`limit` 默认值为 20，
最大值为 100。编码后的 Profile 数据超过 64 MiB 时返回
`413 result_too_large`，调用方应减小 `limit` 后重试。

火焰图每次请求最多查询 100000 个窗口、1000000 个样本和
10000000 个标签，按样本过滤前计数。超限需缩小时间范围或增加文档过滤条件；查询或
合并失败不返回部分结果。原始 Profile 的分页上限不限制火焰图聚合数量。

## 停止任务

停止是异步意图，`pending`、`running` 或已经处于 `stopping` 的 Job 均可接收：

```bash
curl -sS \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/${JOB_ID}/stop"
```

成功返回 `200 OK` 和当前 Job 快照。终态 Job 返回 `409 Conflict`。由于结果可能
不完整，`stopped` Job 不提供结果查询。

## 统一火焰图查询

Grafana 使用 `/v1/profiling/flamegraph/querier.v1.QuerierService/` 下的 Pyroscope 兼容接口。
请求与成功响应使用二进制 `application/proto`，需管理员权限；`id` 仅作过滤，不执行任务所有者授权。
`SelectMergeStacktraces` 的标签选择器必须指定 `scope="host"` 或 `scope="container"`。
`region` 是 Agent 配置的区域，`hostname` 仅表示采集节点，不隐含宿主机范围。
容器查询使用 `container_id` 或单个任务 `id` 定位目标；宿主机查询不能携带容器条件。
例如 `{region="example",hostname="node-01",scope="container",container_id="…",id="…"}`。

文档标签（节点、容器、任务等）只接受一个等值条件。在本接口上，通配值 `*`、`all` 和空字符串只表示取消该标签
的限制，因此 `{scope="host",hostname="*",id="job-1"}` 仍只选中一个任务，`scope` 依旧必填。
容器筛选还支持 `container_name`、`container_hostname`、`container_namespace`、`container_type` 和 `container_qos`，均匹配完整字段值；名称和命名空间不能代替容器 ID。
重复同一个等值条件会被接受，而 `{hostname="host-a",hostname="host-b"}` 这类互相矛盾的
同名条件会返回 `400`，错误信息同时给出两个值，因为一次查询无法同时选中两台主机。
文档标签使用 `=` 以外运算符的条件（包括 `hostname!="*"`）同样会被拒绝。

样本标签 `pid`、`comm`、`cpu` 及扩展标签支持 `=`、`=~`，多个条件取 AND。
例如 `{scope="host",id="job-1",pid="1234",cpu="0"}` 按宿主机 PID 和 CPU 0 过滤。
native on-CPU 的 `cpu` 表示采样时所在 CPU；native off-CPU 的 `cpu` 表示最初切出时的 CPU，
blocked/runqueue 各阶段均保留该值，即使唤醒或恢复运行时已迁移 CPU。`pid` 为宿主机 TGID，
`comm` 为采集线程名称；off-CPU 使用切出时的名称。历史样本缺失 `cpu` 时不会补为 CPU 0。
不限制时省略条件或用 `=~".*"`，后者包含缺失标签；
`all`、`*`、空字符串均为真实值。多值标签按值集合匹配，数值标签格式如 `4096 bytes`。
`physical_usage` 先选最新快照再过滤，不回退到旧快照。

原生 off-CPU 样本使用 `offcpu_reason` 表示离开 CPU 后的时间归因，取值为：

| 值 | 含义 |
| --- | --- |
| `blocked` | 切出至唤醒的阻塞等待 |
| `runqueue` | 唤醒后等待 CPU |
| `runqueue_preempted` | 被抢占后等待 CPU |
| `runqueue_yielded` | 可运行任务非抢占切出后等待 CPU |
| `runqueue_missed_wakeup` | 未观察到唤醒，无法区分阻塞与运行队列等待的区间 |
| `unknown` | 未识别的事件种类 |

例如 `{scope="host",id="job-1",offcpu_reason="blocked"}` 仅选择阻塞等待；
`offcpu_reason=~"runqueue.*"` 选择所有 `runqueue` 前缀的归因，其中
`runqueue_missed_wakeup` 不能视为已确认的运行队列等待。样本权重单位为纳秒。

标签查询方法为 `LabelNames`、`LabelValues`：

| 字段 | 约束 |
| --- | --- |
| `matchers` | 一个选择器字符串。`LabelValues` 必须包含 `__profile_type__`；查询 `physical_usage` 样本标签时，两者均须指定类型和单个任务 `id`。 |
| `name` | `LabelValues` 必填的标签名称，如 `pid`。 |
| `start`、`end` | Unix 毫秒时间戳，`end >= start`；省略为 `0`，不是无限范围。 |

请求字段示例（须编码为 Protobuf 后发送）：

```json
{
  "name": "pid",
  "matchers": ["{__profile_type__=\"process_cpu:cpu:nanoseconds:cpu:nanoseconds\",id=\"job-1\"}"],
  "start": "1787565600000",
  "end": "1787565660000"
}
```

响应为 `names` 列表，如 `{"names":["1234","5678"]}`。样本标签结果去重排序，未知样本标签为空。
`LabelNames` 省略 `matchers` 时返回文档标签清单；带条件时返回该清单与实际样本标签的并集。
样本标签名称或单标签取值超过 1000 个时报错；文档标签值最多返回 100 个，
不接受样本过滤条件。`scope` 固定返回 `host`、`container`；未知的 `__` 前缀标签会被拒绝。

`result_url` 包含从已存储窗口取得的 Region 和实际指标；没有窗口的结果不返回该链接。

Grafana 标签过滤的操作说明见[查看火焰图](/docs/best-practice/continuous-profiling_zh.md#5-查看火焰图)。
