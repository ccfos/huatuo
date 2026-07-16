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
| C / C++ / Go | CPU / 内存 / 锁 | eBPF（perf_event + 栈映射） |
| Java | CPU / 内存 / 锁 | async-profiler |
| Python | CPU / 内存 | py-spy / memray |

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
| `type` | 采集类型：`cpu` / `memory` |
| `target_process_language` | 目标语言：`go`、`c`、`c++`、`java`、`python` |
| `hostname` | **必填**。目标宿主机名，apiserver 据此将任务下发至 `http://{hostname}:19704` 上的 huatuo-bamai agent（需与 agent 上报的 hostname 一致） |
| `duration` | 采集总时长（秒），期间 agent 按 `CPUProfilingInterval` 周期采样 |
| `container` | 容器级采集时填容器 hostname，宿主级采集留空 |
| `target_exec_path` | 可选，按可执行文件路径过滤目标进程 |

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
