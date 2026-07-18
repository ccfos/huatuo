---
title: 持续 Profiling
type: docs
description:
author: HUATUO Team
date: 2026-07-09
weight: 4
---

## 概述

**持续 Profiling（Continuous Profiling）** 对操作系统与应用进行长期、持续的性能采样，覆盖 **CPU、内存、锁** 三类 Profile，产出标准 pprof 格式的火焰图数据。采样数据持久化至存储后端，并支持在 Grafana 中按时间窗口聚合查看，为性能回归分析与故障复盘等场景提供数据底座。

### 架构

持续 Profiling 由三个组件协作完成：

| 组件 | 角色 | 说明 |
| --- | --- | --- |
| huatuo-apiserver | 控制面 | 接收 Profiling 任务并调度至目标节点，提供 Pyroscope 兼容的火焰图查询接口 |
| huatuo-bamai | 数据面 | 在目标节点执行采集，基于 eBPF（C/C++/Go）或第三方工具（Java/Python）采样调用栈 |
| Grafana | 可视化 | 通过 pyroscope 数据源插件直连 apiserver，渲染火焰图 |

支持的采集语言与底层实现：

| 语言 | 采集类型 | 底层实现 |
| --- | --- | --- |
| C / C++ / Go | CPU / 内存 / 锁 | eBPF（perf_event / 锁争用 + 栈映射） |
| Java | CPU / 内存 | async-profiler |
| Python | CPU | py-spy |

Profile 类型标识（Grafana 查询用）：

| 类型 | profile_type |
| --- | --- |
| CPU | `process_cpu:cpu:nanoseconds:cpu:nanoseconds` |
| 内存 | `memory:alloc_space:bytes:space:bytes` |
| 锁 | `process_lock:lock:count:lock:count`<br>`process_lock:lock:nanoseconds:lock:nanoseconds` |

## 运行

最简方式是使用 Docker Compose 一键拉起 Elasticsearch、Prometheus、Grafana、huatuo-apiserver 与 huatuo-bamai：

```bash
$ docker compose --project-directory ./build/docker up
```

启动后各组件地址：

| 组件 | 地址 |
| --- | --- |
| huatuo-apiserver | `http://127.0.0.1:12740` |
| huatuo-bamai 指标 | `http://127.0.0.1:19704/metrics` |
| Grafana | `http://localhost:3000`（admin / admin） |
| Elasticsearch | `http://127.0.0.1:9200` |

Profiling 相关配置位于 `huatuo-apiserver.conf` 的 `[Profiling]` 段：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `CPUProfilingInterval` | 10 | 单次 CPU 采样时长（秒） |
| `MemoryProfilingInterval` | 10 | 单次内存采样时长（秒） |
| `CPUSingleTraceTimeout` | 20 | 单次 CPU 采样超时（秒） |
| `MemorySingleTraceTimeout` | 20 | 单次内存采样超时（秒） |
| `MaxProfilerProcesses` | 10 | profiler 子进程最大并发数；0 表示不限制 |
| `FlameGraphBaseURL` | `http://localhost:8006/d` | 火焰图大盘基址，用于拼接任务结果链接 |

> 若希望任务返回的 `results.url` 直达 Grafana 大盘，将 `FlameGraphBaseURL` 改为实际 Grafana 地址（如 `http://localhost:3000/d`）。

调用 apiserver API 需通过 `Authorization` 请求头携带用户 ID（在 `huatuo-apiserver.conf` 的 `[[Auth.users]]` 中配置）。

> 默认 conf 未配置任何用户，此时鉴权中间件不启用，`Authorization` 可填任意非空值。生产环境请务必在 `[[Auth.users]]` 中配置真实用户，并将 `<user-id>` 替换为对应 ID。

## 采集：以宿主 CPU 为例

以对宿主机进行 CPU Profiling 为例。宿主级采集不指定 `container` 字段，`target_process_language` 设为 `go`（或 `c`/`c++`）以触发 eBPF 原生采集器：

```bash
$ curl -X POST http://127.0.0.1:12740/v1/profiles \
    -H "Content-Type: application/json" \
    -H "Authorization: <user-id>" \
    -d '{
        "type": "cpu",
        "target_process_language": "go",
        "hostname": "<target-host>",
        "duration": 30
    }'
```

请求字段说明：

| 字段 | 说明 |
| --- | --- |
| `type` | 采集类型：`cpu` / `memory` / `lock` |
| `target_process_language` | 目标语言：`go`、`c`、`c++`、`java`、`python` |
| `hostname` | **必填**。目标宿主机名，apiserver 据此将任务下发至 `http://{hostname}:19704` 上的 huatuo-bamai agent（需与 agent 上报的 hostname 一致） |
| `duration` | 采集总时长（秒），期间 agent 按 `CPUProfilingInterval` 周期采样 |
| `container` | 容器级采集时填容器 hostname，宿主级采集留空 |
| `target_exec_path` | 可选，按可执行文件路径过滤目标进程 |
| `scope` | 采集维度：`pid`、`tgid`、`cgroup`、`process-group` 或 `all` |
| `pid` | PID/TGID 目标；`process-group` 模式下也可据此解析 PGID |
| `cgroup_id` / `cgroup_path` | cgroup 目标，二选一；`container` 也会选择对应容器 cgroup |
| `process_group_id` | 进程组目标 |
| `labels` | 自定义 Prometheus 兼容标签（`[A-Za-z_][A-Za-z0-9_]*`） |

### 多维度采集与标签

原生 CPU、内存、锁 Profiler 共用 `pid`、`tgid`、`cgroup`、
`process-group` 四种过滤维度。原有 CLI 的 `thread`、`thread-group`
分别保留为 `pid`、`tgid` 的兼容别名，现有命令无需修改。

```bash
# 精确线程
profiler -t cpu -l go --scope pid --pid 4242 -d 30

# 整个线程组/进程
profiler -t cpu -l go --scope tgid --pid 4242 -d 30

# cgroup v2 路径（目录 inode 即内核 cgroup ID）
profiler -t cpu -l go --scope cgroup \
  --cgroup-path /sys/fs/cgroup/system.slice/example.service -d 30

# 进程组；省略 --process-group-id 时会从 --pid 解析 PGID
profiler -t cpu -l go --scope process-group --pid 4242 -d 30
```

每份上传 Profile 都会携带 `profiling_scope`，以及当前维度对应的
`pid`、`tgid`、`cgroup_id`、`cgroup_path`、`container_id` 或
`process_group_id` 标签；
还可重复传入 `--label key=value` 添加业务标签。Huatuo 同时将这些标签
写入 Pyroscope series name 与标准 pprof sample label，因此数据兼容
Pyroscope/Parca。Pyroscope 兼容的 `LabelNames`、`LabelValues` 和火焰图
查询接口也支持按上述维度检索和聚合。

### 内核锁 Profiling

原生 C/C++/Go 目标支持 mutex、spinlock、rwlock 三类内核锁，可按争用
等待时间或争用次数生成 Profile：

```bash
profiler -t lock -l go --scope tgid --pid 4242 \
  --lock-types mutex,spinlock,rwlock \
  --lock-mode time --lock-min-wait 1us -d 30
```

使用 `--lock-mode count` 查看达到等待阈值的争用次数；`--lock-min-wait`
用于过滤较短的等待。API 对应字段为 `lock_types`、`lock_mode`、
`lock_min_wait`。

采集器采用与 `perf lock contention` 相同的“只观测真实争用”模型：Linux
5.19 及以上优先使用内核 `lock:contention_begin` / `lock:contention_end`
tracepoint；Ubuntu 20.04 的 5.4 等旧内核则按能力探测 mutex、queued
spinlock、queued rwlock 的慢路径。采集器不会退回到高频
`_raw_*_lock` 加锁入口进行全局插桩。等待时间与次数先在 BPF 内聚合，
再由用户态周期读取，因此高争用时事件量仍然有界。运行时不依赖
`perf` 命令或 `CONFIG_LOCK_STAT`；若内核既没有 contention tracepoint，
也没有安全的慢路径符号，请求会返回明确的不支持错误。

返回任务 ID：

```json
{ "id": "<task-id>" }
```

采集流程：

1. apiserver 创建任务并下发至目标宿主上的 huatuo-bamai。
2. huatuo-bamai 加载 eBPF 程序（`perf_event_sw_cpu_clock`），按默认 99Hz 采样内核栈与用户栈。
3. 采样数据经符号化后转换为 pprof 格式，写入 Elasticsearch（index 名为 `huatuo-apiserver.conf` 中 `[ElasticSearch].Index` 配置项，默认 `huatuo_bamai`）。

查询任务状态与停止任务：

```bash
# 查询任务状态
$ curl -H "Authorization: <user-id>" \
    http://127.0.0.1:12740/v1/profiles/<task-id>

# 停止任务
$ curl -X PATCH http://127.0.0.1:12740/v1/profiles/<task-id> \
    -H "Content-Type: application/json" \
    -H "Authorization: <user-id>" \
    -d '{"status":"stopped"}'
```

任务完成后，状态响应体 `results.url` 字段返回火焰图链接（基于 `FlameGraphBaseURL` 拼接）。

## 查看

火焰图通过 Grafana 大盘查看，大盘已预置并随 Docker Compose 自动加载：

| 大盘 | UID | 适用对象 |
| --- | --- | --- |
| Continuous Profiling(host) | `continuous-profiling-host` | 宿主机 |
| Continuous Profiling(container) | `continuous-profiling-container` | 容器 |

打开 `http://localhost:3000/d/continuous-profiling-host`，选择 `hostname` 与 `type`（profile_type），即可查看聚合火焰图。大盘上方时序图展示采样点分布，下方为火焰图面板，支持按时间范围聚合查看。

> **原理**：Grafana 通过 `grafana-pyroscope-datasource` 插件将火焰图请求转发至 apiserver 的 `/v1/profiles/flamegraph/` 路径；apiserver 实现 Pyroscope Querier 协议（`SelectMergeStacktraces` 等），从 Elasticsearch 检索 pprof 数据并合并返回。
