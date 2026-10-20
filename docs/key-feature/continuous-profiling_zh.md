---
title: 持续性能剖析
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

{{% alert color="info" title="🎯 关于 HUATUO（华佗）" %}}
<div style="text-align: left;">
HUATUO（华佗）是由滴滴开源并依托 CCF（中国计算机学会）孵化的操作系统深度观测项目，广泛应用于AI 计算、AI 沙箱、云原生通用计算、云服务、基础架构服务等场景。
</div>
{{% /alert %}}

## 🚀 快速上手

示例基于仓库根目录下的 `build/docker/docker-compose.yml`，从零开始完成一次宿主机 CPU 性能剖析，并在 Grafana 中选择时间范围分析火焰图。

### 1. 启动服务

先修改配置，确保 huatuo-bamai 和 huatuo-apiserver 有写入 ES 的能力，才能正常保存 profile 数据。配置文件通过 docker compose 挂载到容器中，因此直接修改项目根目录下的文件即可。

huatuo-bamai.conf 中配置：
```toml
[HTTPServer.Auth]
    BearerToken = "REPLACE_WITH_NODE_TOKEN"

[Storage]
    [Storage.Elasticsearch]
        Address = "http://127.0.0.1:9200"
        Index = "huatuo_bamai"
        Username = "elastic"
        Password = "huatuo-bamai"
```

huatuo-apiserver.conf 中配置：
```toml
[Agent.Auth]
    BearerToken = "REPLACE_WITH_NODE_TOKEN"

[Elasticsearch]
    Address = "http://127.0.0.1:9200"
    Username = "elastic"
    Password = "huatuo-bamai"
    Index = "huatuo_bamai"

[Auth]
    [[Auth.Users]]
        ID = "administrator"
        BearerToken = "REPLACE_WITH_RANDOM_HEX"
        Admin = true
```

然后项目根目录执行：

```bash
docker compose --project-directory ./build/docker up
```

> 不加 `-d`，方便观察各组件启动日志。如需后台运行可另开终端操作，或自行加 `-d`。

启动后运行的组件及作用：

| 服务 | 作用 | 默认端口 |
| --- | --- | --- |
| `huatuo-bamai` | 采集 Agent，执行 profiler 采样 | `19704` |
| `huatuo-apiserver` | API 入口，创建任务、下发、查询火焰图 | `12740` |
| `elasticsearch` | 存储 profile 数据（index：`huatuo_bamai`） | `9200` |
| `grafana` | 火焰图面板展示 | `3000` |

### 2. 确认服务就绪

新开一个终端，执行以下命令确认服务就绪：

```bash
# Agent 健康检查
$ curl -s http://localhost:19704/version | jq .data.name

"huatuo-bamai"

# API Server 健康检查
$ curl -s http://localhost:12740/version | jq .data.name

"huatuo-apiserver"

# ES 索引状态
$ curl -s -u elastic:huatuo-bamai "http://localhost:9200/_cat/indices/huatuo_bamai?v"

health status index        uuid                   pri rep docs.count docs.deleted store.size pri.store.size dataset.size
yellow open   huatuo_bamai 147fzHJhQ820GjCKFLh5ZQ   1   1         42            0    297.8kb        297.8kb      297.8kb
```

设置环境变量便于后续调用：

```bash
API_BASE="http://127.0.0.1:12740"
API_TOKEN="REPLACE_WITH_RANDOM_HEX"
```


### 3. 创建宿主机 CPU 剖析任务

以 `c` 语言（原生，覆盖 C/C++/Go 等）为例，对宿主机整体采样 30 秒：

```bash
# hostname 需要使用节点的实际主机名
HOSTNAME=$(hostname)

JOB_ID=$(curl -s -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{
    \"type\": \"cpu\",
    \"language\": \"c\",
    \"mode\": \"oncpu\",
    \"scope\": \"host\",
    \"duration_seconds\": 30,
    \"hostname\": \"${HOSTNAME}\"
  }" \
  "${API_BASE}/v1/profiling" | jq -r .data.request_id)

echo "Job ID: $JOB_ID"
```

### 4. 验证数据已写入 ES

每 10 秒生成一个聚合窗口，30 秒任务约产生 3 条采样数据，等待 30 秒后确认数据写入：

```bash
$ curl -s -u elastic:huatuo-bamai "http://localhost:9200/huatuo_bamai/_count" \
  -H "Content-Type: application/json" \
  -d '{"query":{"exists":{"field":"profile_data.profile"}}}' | jq .count

3
```

### 5. Grafana 面板查看火焰图

示例是对本机进行剖析，打开 **Continuous Profiling (host)** 面板（容器是独立的面板）：

- 地址：[http://localhost:3000/d/continuous-profiling-host](http://localhost:3000/d/continuous-profiling-host) （根据实际环境地址替换 `localhost:3000`）
- 账号/密码：`admin / admin` （首次登陆提示修改默认账号密码，暂时跳过即可）

操作步骤：

1. 右上角选择时间范围，确保覆盖采样时间段
2. 变量栏选择输入你的 `hostname` 和 选择 `type` 为 `process_cpu:cpu:nanoseconds:cpu:nanoseconds`
3. 火焰图面板自动加载该时间窗口的聚合调用栈，随选择时间范围变化而自动聚合更新，focus block 可选定关心的调用栈
4. symbol 排序、统计、筛选等操作在 top table 中进行，选择 Both 可展示

![continuous-profiling-grafana-host.png](/docs/img/continuous-profiling-grafana-host.png)

其他更多丰富维度的剖析任务参考 Profiling API。


## 🌐 Profiling API

huatuo-apiserver 通过 `/v1/profiling` 创建、监督和停止 Profiling Job。每次 HTTP
创建请求都是一次独立执行。Job 持久化在 huatuo-apiserver，Node 只维护内存中的
Operation 和 profiler 进程。

Job 控制不依赖 Elasticsearch；查询原始结果和生成 Dashboard 链接时才要求配置
Elasticsearch。Node 和 Apiserver 必须使用同一索引。

### 1. 请求约定

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

### 2. 查询剖析能力

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
| `c`、`c++`、`go` | `physical_usage` | 采集期间物理内存有符号净变化 |
| `java` | `object_alloc` | JVM 对象分配 |
| `java` | `object_usage` | JVM 存活对象 |

### 3. 创建剖析任务

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

### 4. 查询任务列表

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

### 5. 查询单个任务

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

任务状态流转如下：

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

### 6. 获取原始剖析数据

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

### 7. 停止任务

停止是异步意图，`pending`、`running` 或已经处于 `stopping` 的 Job 均可接收：

```bash
curl -sS \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/${JOB_ID}/stop"
```

成功返回 `200 OK` 和当前 Job 快照。终态 Job 返回 `409 Conflict`。由于结果可能
不完整，`stopped` Job 不提供结果查询。

## 📖 profiler 命令行功能概述

`profiler` 是 HUATUO 提供的独立性能剖析命令行工具。它可以直接对宿主机进程或容器内进程采样，不依赖 huatuo-apiserver、Elasticsearch 或 Grafana。工具支持 C、C++、Go、Java 和 Python 进程，并将调用栈输出为折叠栈或 SVG 火焰图。

C、C++ 和 Go 使用基于 eBPF 的原生采集器，可观测 on-CPU、off-CPU 阻塞与调度延迟、虚拟内存分配、物理内存分配和物理内存驻留。Java 通过 async-profiler 观测 CPU、对象分配和存活对象；Python 通过 py-spy 观测 CPU。采集结果适合用于热点函数定位、内存增长归因、容器内进程分析和性能问题现场留存。

本节以下介绍 `_output/bin/profiler` 的独立使用方式。服务化的持续 Profiling 使用方式见上方 Profiling API。

## 🎯 应用场景

### 1. CPU 热点与调用路径定位

对 C、C++、Go、Java 或 Python 进程按固定频率采样调用栈，通过栈宽度判断 CPU 时间的主要消耗路径。原生采集器还可以使用 `--cpuid` 将采样限定到指定 CPU，以分析绑核任务或局部 CPU 热点。

### 2. 原生进程内存归因

对 C、C++ 和 Go 进程分别观测虚拟地址空间分配、物理页分配和采集期间物理内存有符号净变化。三种模式区分“申请了多少地址空间”“实际分配了多少物理页”和“当前仍驻留多少物理页”，用于定位 `mmap`、缺页分配和常驻内存增长的调用路径。

### 3. JVM 对象分配与存活对象分析

通过 async-profiler 采集 Java 对象分配或存活对象调用栈。对象分配适合定位高分配速率和 GC 压力来源；存活对象适合分析采集窗口内仍被引用的对象及其分配路径。

### 4. 容器与多进程任务分析

通过容器 ID 自动解析容器内目标进程，适合 Docker 和 containerd 工作负载。Java 和 Python 还支持逗号分隔的多个 PID，并可限制同时运行的采集子进程数量，适用于同一服务的多实例或父子进程分析。

## 🛠️ 功能使用

### 1. 构建与运行条件

在仓库根目录构建完整产物：

```bash
make build
```

生成的命令位于 `_output/bin/profiler`。原生采集依赖 Linux eBPF、perf event 和仓库构建出的 BPF 对象，通常需要 root 权限，并要求 `kernel.perf_event_paranoid` 允许采样。Java 需要 async-profiler，`--tool-path` 指向统一工具根目录，其中包含 `bin/asprof` 和 `lib/libasyncProfiler.so`。Python 使用同一根目录下的 `py-spy`。

查看当前版本的完整帮助：

```bash
_output/bin/profiler --help
```

命令的基本结构如下：

```bash
sudo _output/bin/profiler \
  --type <cpu|memory> \
  --language <c|c++|go|java|python> \
  --pid <pid> \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles/
```

`--type` 和 `--language` 为必填参数。Java、Python 和原生内存采集必须在 `--pid` 与 `--container-id` 中指定且仅指定一个目标；原生 CPU 采集未指定目标时可进行宿主机级采样。

### 2. 通用命令参数

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--type`, `-t` | 无 | 全部 | 观测类型：`cpu` 或 `memory`，必填 |
| `--language`, `-l` | 无 | 全部 | 目标语言：`c`、`c++`、`go`、`java` 或 `python`，必填 |
| `--pid`, `-p` | 无 | 全部 | 目标 PID；Java、Python 可使用逗号分隔多个 PID，原生采集最多一个 PID |
| `--container-id` | 无 | 全部 | 目标容器 ID；不能与 `--pid` 同时使用 |
| `--duration`, `-d` | `10` | 全部 | 总采集时长，单位为秒，最小为 1 |
| `--aggr-interval` | `10` | 全部 | 聚合周期，单位为秒，不得大于采集时长 |
| `--freq`, `-F` | `99` | CPU | 每秒采样次数；Java 最大为 1000 |
| `--output-path` | 必填 | 全部 | 本地文件、目录或 `stdout`，`remote-store` 时为 Unix socket；新目录路径以 `/` 结尾。危险使用提醒：stdout 可能暴露采集数据 |
| `--output-format` | `collapsed`（必须显式指定） | 全部 | `collapsed`、`flamegraph`、`svg`、`json` 或 `remote-store`；必须与 `--output-path` 同时使用 |
| `--max-concurrent-procs` | `0` | Java、Python | 并发采集子进程上限；`0` 表示不限制 |
| `--tool-path` | 无 | Java、Python | 外部采集工具统一根目录，必填 |
| `--binary-match-path` | 无 | Java、Python | 按可执行文件路径匹配容器内目标进程 |
| `--huatuo-api-address` | `127.0.0.1:19704` | 容器目标 | 用于解析容器元数据的 HUATUO API 地址 |
| `--task-id` | 空；本地输出时内部生成 | 全部；`remote-store` 必填 | toolstream 和远端存储共用的稳定采集任务 ID |
| `--enable-pprof` | `false` | 工具自身 | 在 `:6000` 暴露 profiler 进程自身的 Go pprof 接口 |
| `--version-format` | `text` | 版本查询 | `--version` 的输出格式：`text`、`json` 或 `short` |
| `--help`, `-h` | - | 全部 | 显示命令帮助 |
| `--version`, `-v` | - | 全部 | 显示版本与构建信息 |

原生采集专用参数：

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--memory-mode` | 无 | 原生内存、Java 内存 | 内存观测维度；使用 `--type memory` 时必填 |
| `--cpuid` | 全部 CPU | 原生 CPU | CPU 列表或范围；off-CPU 样本按任务切出时所在 CPU 过滤 |
| `--cpu-mode` | `oncpu` | 原生 CPU | `oncpu` 按频率采样，`offcpu` 归因阻塞与可运行调度延迟 |
| `--require-hardware-pmu` | `false` | 原生 on-CPU | 强制使用硬件 PMU 采样；不可用时失败，不回退软件 CPU clock |
| `--offcpu-phase` | `all` | 原生 off-CPU | 累计 `all`、`blocked` 或 `runqueue` 时间 |
| `--offcpu-min-duration-us` | `1000` | 原生 off-CPU | 丢弃持续时间小于该微秒数的阶段 |
| `--offcpu-stats` | `false` | 原生 off-CPU | 收集 BPF 诊断统计；错误和清理路径会产生额外开销 |
| `--thread-group` | `false` | 原生 | 同时采集目标 PID 所在线程组中的其他线程 |
| `--physical-memory-probability` | `100` | 原生物理内存 | 物理内存事件采样概率，范围为 1～100 |
| `--log-bpf-debug` | `false` | 原生 | 输出 BPF 调试事件，常规采集不建议启用 |

日志参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--log-level` | `error` | `trace`、`debug`、`info`、`warn` 或 `error` |
| `--log-file` | `stdout` | 日志文件路径，或 `stdout` |
| `--log-size` | `100` | 日志轮转大小，单位 MB；`0` 表示不轮转，仅用于文件输出 |
| `--verbose` | `false` | 等价于 `--log-level debug --log-file stdout`，并覆盖显式日志设置 |

### 3. C、C++ 和 Go 观测

C、C++ 和 Go 均使用原生 eBPF 采集器，命令只需替换 `--language`。CPU 模式按采样次数统计调用栈宽度，同时包含可解析的用户态栈和内核态栈。

```bash
sudo _output/bin/profiler \
  --type cpu \
  --language go \
  --pid 12345 \
  --duration 30 \
  --aggr-interval 10 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/go-cpu/
```

如需包含同一进程的工作线程，增加 `--thread-group`。如需限定 CPU，增加 `--cpuid 2,4-7`。原生 CPU 也支持容器和宿主机级采样：

原生 on-CPU 采集优先使用硬件 CPU cycle event；硬件 PMU 不可用时回退软件
CPU clock。两种采样源下 `--freq` 均表示每秒采样次数。若软件时钟回退会掩盖
IRQ 关闭期间的 CPU 时间，可指定 `--require-hardware-pmu`。

```bash
# 采集指定容器
sudo _output/bin/profiler \
  --type cpu --language c --container-id <container-id> \
  --duration 30 --aggr-interval 10 \
  --output-format collapsed --output-path ./profiles/container/

# 不指定 PID 或容器，采集宿主机
sudo _output/bin/profiler \
  --type cpu --language c \
  --duration 30 --aggr-interval 10 \
  --output-format flamegraph --output-path ./profiles/host/
```

如需把线程离开 CPU 的时间归因到触发切出的调用路径，使用 off-CPU 模式：

```bash
sudo _output/bin/profiler \
  --type cpu --language go --pid 12345 --thread-group \
  --cpu-mode offcpu --offcpu-phase all \
  --cpuid 2,4-7 \
  --offcpu-min-duration-us 1000 \
  --duration 30 --aggr-interval 10 \
  --output-format flamegraph --output-path ./profiles/go-offcpu/
```

off-CPU 是事件驱动采集，因此不使用 `--freq`。指定 `--cpuid` 时，仅记录任务从目标 CPU 切出后开始的区间；后续在其他 CPU 唤醒或切入不会改变该归属。火焰图直接以纳秒为数值，并增加 `off-CPU blocked`、`scheduling delay (preempted)`、`scheduling delay (yielded)` 等根节点。`all` 阶段同时累计阻塞与 runqueue 等待时间，但仍按这些根节点分开显示。采集端使用单一稳定的 BPF stack map，避免长时间睡眠跨越多轮读取后被错误解析到另一代栈。

原生内存支持以下维度：

| `--memory-mode` | 统计内容 | 适用问题 |
| --- | --- | --- |
| `virtual_alloc` | 虚拟地址空间分配量及其调用栈 | `mmap` 等虚拟内存申请过多、地址空间增长 |
| `physical_alloc` | 采集窗口内新分配的物理内存量 | 缺页触发的物理页分配热点、分配速率分析 |
| `physical_usage` | 采集期间物理内存有符号净变化 | 分配与释放差额，不能推断当前占用 |

```bash
sudo _output/bin/profiler \
  --type memory \
  --language c++ \
  --memory-mode physical_usage \
  --pid 12345 \
  --thread-group \
  --physical-memory-probability 100 \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles/native-memory/
```

`--physical-memory-probability` 仅适用于 `physical_alloc` 和 `physical_usage`。降低该值可减少高频内存事件的处理量，但火焰图中的值由采样事件估算，不再是逐事件统计。

### 4. Java 观测

Java CPU 采集依赖 async-profiler。单 PID、容器和多 PID 均可使用：

```bash
_output/bin/profiler \
  --type cpu \
  --language java \
  --pid 12345,12346 \
  --tool-path /opt/huatuo/tools \
  --max-concurrent-procs 2 \
  --duration 30 \
  --aggr-interval 10 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/java-cpu/
```

Java 栈中的类名使用点分隔（例如 `java.lang.Thread.run`）。分配对象类型不保留 TLAB 内外的颜色标记；同一调用栈中的同类型对象按字节数合并。真正的内核帧仍使用 `_[k]` 后缀。

Java 内存支持两个维度：

| `--memory-mode` | 统计内容 | 适用问题 |
| --- | --- | --- |
| `object_alloc` | 采集窗口内的对象分配及分配调用栈 | 高分配速率、短命对象和 GC 压力来源 |
| `object_usage` | 存活对象及其分配调用栈 | 长生命周期对象、堆占用来源和疑似内存泄漏 |

```bash
_output/bin/profiler \
  --type memory \
  --language java \
  --memory-mode object_usage \
  --pid 12345 \
  --tool-path /opt/huatuo/tools \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles/java-memory/
```

使用容器 ID 时，将 `--pid` 替换为 `--container-id <container-id>`。如果容器内存在多个候选进程，可通过 `--binary-match-path` 指定目标可执行文件路径。

### 5. Python 观测

Python 当前仅支持 CPU 观测。`--aggr-interval` 必须与 `--duration` 相等，即一次采集只生成一个聚合窗口。`--tool-path` 指向包含 `py-spy` 的统一工具根目录。

```bash
_output/bin/profiler \
  --type cpu \
  --language python \
  --pid 12345,12346 \
  --tool-path /opt/huatuo/tools \
  --max-concurrent-procs 2 \
  --duration 30 \
  --aggr-interval 30 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/python-cpu/
```

Python 不支持 `--type memory`。若需要 Python 内存分析，应使用独立的内存分析工具；当前 `profiler` 命令不会调用 memray 生成 Python 内存结果。

### 6. 火焰图与输出格式选择

| 格式 | 生成内容 | 选择建议 |
| --- | --- | --- |
| `collapsed` | `perf_<Unix 时间戳>.folded`；每行是以分号分隔的调用栈及末尾计数 | 用于脚本检索、结果比较，或交给其他火焰图工具二次渲染 |
| `flamegraph` | `flamegraph_<Unix 时间戳>.svg`；内嵌交互脚本的 SVG | 默认的人工分析格式，可在浏览器中搜索、缩放和查看栈帧数值 |
| `svg` | 与 `flamegraph` 相同的交互式 SVG | 兼容显式要求 SVG 的调用方；当前实现与 `flamegraph` 等价 |
| `remote-store` | 不生成本地火焰图，通过 Unix socket 上传 pprof 兼容数据 | 接入 HUATUO 存储链路时使用，不适合离线查看 |

火焰图从下到上表示调用方向，矩形宽度表示该调用栈在当前观测维度中的累计值。不同类型的宽度含义不同：CPU 表示采样次数折算的 CPU 时间占比；内存模式表示相应的虚拟分配、物理分配、物理驻留、Java 对象分配或存活对象量。横向位置不表示时间先后。

折叠栈示例：

```text
main;handleRequest;parsePayload 428
main;handleRequest;writeResponse 172
```

需要保留原始数据并支持后续使用不同配色或过滤规则重新渲染时，选择 `collapsed`。只需直接定位热点时，选择 `flamegraph`。`remote-store` 依赖 HUATUO toolstream Unix socket，要求提供非空的 `--task-id`，独立离线使用时不应选择该格式。

### 7. 根据集成测试复现

仓库集成测试提供了可执行的端到端示例。测试会创建目标进程、运行 profiler，并校验输出中的预期调用栈：

```bash
# 原生 CPU
sudo ./integration/run.sh test_profiler_native_cpu.sh

# 原生 off-CPU 阻塞与调度延迟
sudo ./integration/run.sh test_profiler_native_cpu_offcpu.sh

# 原生虚拟内存与物理内存
sudo ./integration/run.sh test_profiler_native_memory_virtual_alloc.sh
sudo ./integration/run.sh test_profiler_native_memory_physical_usage.sh

# Java CPU 与内存
sudo ./integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo ./integration/run.sh test_profiler_java_memory_usage_alloc.sh

# Python 多进程 CPU
sudo ./integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

容器、线程组和指定 CPU 的示例分别位于 `test_profiler_native_cpu_container.sh`、`test_profiler_native_cpu_thread_group.sh` 和 `test_profiler_native_cpu_cpuid.sh`。运行前需完成 `make build`，并根据 `integration/env.sh` 配置 Java 或 Python 采集工具路径。

## ⚙️ 功能原理介绍

`profiler` 先根据语言和观测类型选择采集器。原生 on-CPU 采集器将 eBPF 程序挂载到 perf event；off-CPU 模式挂载调度切换、唤醒、退出和任务释放 tracepoint；原生内存采集器通过内核事件记录分配与释放路径；Java 和 Python 采集器分别启动 async-profiler 和 py-spy 子进程。采集记录进入统一聚合流水线，按调用栈合并计数，最后写入本地文件或上传远端存储。

```mermaid
flowchart LR
    CLI[profiler 命令参数] --> Select{语言与观测类型}
    Select -->|C/C++/Go| Native[eBPF 原生采集器]
    Select -->|Java| Java[async-profiler]
    Select -->|Python| Python[py-spy]
    Native --> Queue[采样记录队列]
    Java --> Queue
    Python --> Queue
    Queue --> Aggregate[按调用栈聚合]
    Aggregate --> Folded[collapsed 折叠栈]
    Aggregate --> SVG[交互式 SVG 火焰图]
    Aggregate --> Remote[Unix socket 远端上传]
```

`--duration` 控制采集生命周期，`--aggr-interval` 控制快照周期。`json` 将每个公共采集窗口写成一行 JSON，因此流式采集可能输出多行。本地 `collapsed`、`flamegraph` 和 `svg` 在采集结束时写出最终聚合结果；`remote-store` 在流式模式下周期上传快照，在单次模式下于采集结束后上传。provider 在入队前完成符号化；所有聚合模式在输入队列满时均返回错误。

## 公共采集窗口

profiler 与 irqtracing 共用 `internal/profiling/profile.Run`、Pipeline 和 `types.AggregationWindow`。provider 负责采集、解码、符号化、单位换算及标签规范化，提交 `SampleBatch`；Pipeline 按用户栈、内核栈和完整标签组累计。Java agent 生命周期位于 `cmd/profiler/runtime/java`，工具执行位于 `cmd/profiler/exec`。

窗口包含结构化帧（Name/File/Line）、root → leaf 栈表、标签组、带符号 int64 权重及时间。帧 ID 从 0 开始，栈和标签组 ID 从 1 开始，0 表示空。provider 提交最终单位，CPU/锁等待使用纳秒，内存使用字节，IRQ source/victim 使用事件次数；Pipeline 保留负数及相消后的零值。新路径没有 Period，不在输出时再次缩放。physical_usage 表示采集期间有符号净变化，不代表当前占用。

`pid`、`cpu`、`comm`、`lock_address`、`irq`、`softirq_vector` 和 `offcpu_category` 是单值标签；PID/CPU 使用规范十进制字符串，锁地址使用规范十六进制。comm 仅表示进程命令名，Java/Python 入口分别保存为 `java_entry`、`python_entry`。offcpu 类别为 unknown、blocked、runqueue、runqueue_preempted、runqueue_yielded、runqueue_missed_wakeup。标签不固定进入公共栈；缺失、空字符串和数字零互不等价。

本地 formatter 直接读取统一窗口；JSON 和服务端 handler 使用公共 builder 构建完整 pprof，保留源位置、带符号值及 sample labels。JSON 外层和 ES 文档结构不变。旧完整 pprof 通过独立兼容入口保留多指标、mapping、inline 和原采样元数据。

### 配置、队列和时间

runner 在启动采集前校验固定配置并创建 Formatter。`provider.New` 为每次任务复制配置并创建独立采集器。任务 ID、工具名分别来自 `RunConfig.ID`、`ProfilerMeta.ToolName`；仅缺失 ID 时生成。`formatter.Config` 包含 Format、Destination、TaskID、ToolName，文件创建与关闭由 formatter 管理。

Native CPU、memory alloc 和 Java（CPU、object_alloc、object_usage）使用 Streaming，周期输出并在停止时排空；native physical_usage、Python 和 IRQ 使用 OneShot，采集结束后输出。Run 统一管理所有 provider 的采集时长。输入队列容量为 16 个批次，聚合与输出由同一个 worker 顺序执行；每批最多 256 样本、1 MiB 帧和标签文本，每个样本每侧最多 4096 帧、64 标签。超限返回明确错误。16 批最多包含 4096 样本、16 MiB 计量文本，帧结构及底层分配另计；这不是总内存上限。单窗口身份数量尚未设限，高基数采集仍可能增长内存。

`Enqueue(ctx, batch)` 返回 nil 表示成功入队并转移底层切片/map 所有权，不保证已经聚合或输出。所有模式均非阻塞入队；队列满返回 `ErrQueueFull`，context 取消时拒绝交付，错误时批次仍归调用者。provider 不复用已交付批次、不自动重试；runner 收到入队错误后取消处理，失败任务不 Complete。同步输出期间暂停消费输入，较大的队列只能吸收短暂突发，持续过载仍会失败。

首个有效批次在 Enqueue 尝试交付前取得的时间成为窗口起点，snapshot 在同步调用 Add 之前确定终点。封存窗口后、调用 Add 前替换所有窗口数据和索引，下一有效批次重新建立起点；相邻窗口允许空闲或重叠。纯 collection 丢样批次同样建立窗口起点。这些时间描述接收和聚合区间，不是精确采样区间。

### 输出和错误

collapsed 将标签投影为虚拟根帧并累计展示栈；SVG 拒绝负权重。JSON 保留有符号值并逐窗口写出；remote-store 发送统一窗口。Speedscope 按完整标签组分组，保留源码位置并检查非负权重和浏览器整数范围。Chrome Trace 仅接受时间单位，换算微秒形成估算时间线，不恢复真实事件顺序。后两者支持工厂构造，暂无 CLI 选项。

Pipeline.Close 仅关闭输入，不等待或释放 Formatter。runner 在生产者退出后调用一次，因此关闭输入不会与入队并发。Run 同样仅调用一次，不支持重复调用或关闭后入队。Add/Complete 各有 30 秒超时，取消不撤销已经写出的内容。Formatter 纯取消及超时错误保持原值，独立 I/O 与清理错误仍保留。

Pipeline.Run 同步处理批次，聚合或 Add 失败直接返回，不重试。runner 同步执行采集循环，仅显式启动 pipeline 处理 goroutine；Start 成功后由定时器取消原有采集 context，采集返回即停止计时器。采集循环退出、collector 清理后，仅关闭一次 pipeline 输入并等待处理结果。Stop() 无需 context；collector 自行管理外部清理超时，确保任务取消后仍执行资源释放。采集、清理和处理全部成功才调用 Complete，工作循环退出后调用 Formatter.Close。Pipeline 不维护生命周期状态机、错误回调或 Formatter 完成逻辑。Run 统一管理采集时长。时长到期和 Ctrl+C（SIGINT）正常结束采集，完成尾部数据交付后允许提交；普通取消、作为首次取消原因的调用方 deadline、SIGTERM/SIGHUP/SIGQUIT 或真实错误禁止提交。当前 CLI 调用链不设置 deadline；Run 不恢复已分离的 deadline，正常停止先发生时不会再由原 deadline 中止后续处理。ReadDataLoop 保留单个 context，通过取消原因区分停止语义；正常收尾在内部使用有超时的独立 context。Python 用 SIGINT 停止 py-spy 并等待完整文件，IRQ 先 detach 再读快照，Java 停止采样后读最后文件。后续处理失败会拒绝入队并禁止提交，但不会立即打断已开始的独立收尾 I/O。provider 必须在采集循环返回前结束所有入队调用。nodeagent 原有 Stop 中止并丢弃任务的行为不变。

### 远端协议、存储与可见性

remote-store 复用 Toolstream 服务 `profiling_store_service`，使用业务版本 `3`。接收端仅在版本 3 接收统一窗口，版本 1/2 仅接收旧完整 pprof；旧版本的轻量聚合表示及未知字段明确拒绝。先升级接收端，再升级发送端。业务版本与 Toolstream 传输版本独立。

handler 完整校验外部窗口，通过公共 builder 构建 pprof 并 `SaveSync`。sample labels 与样本对应落盘；工具名写入 tracer_name。Complete 发送 `End + Commit` 并等待 ACK；普通 End、断连或失败不写完成标记，已写窗口不回滚。普通数据帧无 ACK，接收或保存错误由提交 ACK 返回。

JSON 在首个窗口转换成功后才打开目标文件。转换期间取消或超过 Pipeline 的 30 秒输出超时，不会截断已有结果，也不会创建空结果文件；后续写入失败仍不提供事务回滚。 JSON 可在 Add 写文件，其他本地输出也不提供事务回滚。现有火焰图查询不以完成标记过滤全部窗口；标签持久化不表示已支持标签索引或样本过滤。ES 标签查询、输出发布及上传时间切换采集时间的规则仍需独立落实。

### 丢样统计

新 Pipeline 的 `DroppedSamples` 仅产生 `collection`，存入 `profile_data.metrics.dropped_samples`。provider 提交采集丢样增量；按累计不会超过 uint64 范围的业务前提直接相加，不做溢出判断或提前输出。队列满返回错误，不静默丢弃或产生 `aggregation_queue` 计数；该分类仍支持历史窗口读取和存储兼容。仅有丢样的窗口仍输出，取消不保证导出最终统计。

## 🌟 结尾

{{% alert color="info" %}}
<div style="text-align: left;">
🌟 欢迎 Star: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
<br><br>
👀 欢迎订阅官方微信公众号<br>
<img src="/img/contact-weixin.png" alt="微信公众号二维码" style="max-width: 200px; margin-top: 10px;">
</div>
{{% /alert %}}
