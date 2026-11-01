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
Operation 和 profiler 进程。

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
| `type` | 剖析类型：`cpu` 或 `memory` |
| `language` | 目标语言 |
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
| `type` | 是 | 剖析类型：`cpu` 或 `memory` |
| `language` | 是 | 目标进程语言，必须与剖析类型匹配 |
| `mode` | 是 | 能力表为对应类型和语言声明的模式 |
| `duration_seconds` | 是 | 采集时长，单位为秒 |
| `hostname` | 是 | 运行目标进程的节点主机名，用于任务调度 |
| `scope` | 是 | 能力表允许的 `host` 或 `container` |
| `container_id` | container 范围必需 | 目标容器 ID |
| `binary_match_path` | 否 | 能力表允许时使用的可执行文件路径匹配条件 |

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
| `type` | `cpu` 或 `memory` |
| `language` | 目标进程语言 |
| `mode` | 从能力表中选择的剖析模式 |
| `binary_match_path` | 可执行文件匹配路径；未使用时不返回该字段 |
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
已持久发布但内容为空的结果仍返回成功，`items` 为空数组。显式 `limit=0` 返回参数
错误；只有未传 limit 才使用默认值。`limit` 默认值为 20，
最大值为 100。编码后的 Profile 数据超过 64 MiB 时返回
`413 result_too_large`，调用方应减小 `limit` 后重试。

火焰图每批读取并合并 100 个窗口，每次请求总上限为 100000 个窗口。一小时每秒产生
一个窗口时，全部 3600 个窗口都会参与合并。超过 100000 个窗口时返回错误，提示缩小
时间范围或过滤条件；查询或合并失败都不返回部分火焰图。原始 Profile 列表仍按 Job
查询，其每页上限不限制完整火焰图的聚合数量。

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

Grafana 使用 `/v1/profiling/flamegraph/` 下的 Pyroscope 兼容 Protobuf 接口。
`SelectMergeStacktraces` 的标签选择器必须指定 `scope="host"` 或 `scope="container"`。
`region` 是 Agent 配置的区域，`hostname` 仅表示采集节点，不隐含宿主机范围。
容器查询使用 `container_id` 或单个任务 `id` 定位目标；宿主机查询不能携带容器条件。
例如 `{region="example",hostname="node-01",scope="container",container_id="…",id="…"}`。

统一面板 UID 为 `continuous-profiling`，变量顺序为 Region、节点、范围、容器、指标、任务。
趋势图与火焰图使用相同任务过滤。容器选择 All 时，任务仍限定到一个目标；
`physical_usage` 始终要求单个任务并只取最新快照。
任务摘要使用 `pyroscope.yaml` 的 Infinity 数据源，需与 Pyroscope 数据源配置相同的 API token。
`result_url` 包含从已存储窗口取得的 Region 和实际指标；没有窗口的结果不返回该链接。
