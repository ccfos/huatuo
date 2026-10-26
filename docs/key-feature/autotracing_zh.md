---
title: 自动追踪
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

## 概述

AutoTracing 在指标达到条件后自动启动现场采集，用于分析偶发的 CPU、I/O、负载和内存异常。CPU 突增使用配置的 `perf` 或 `profiler` 采样，其余场景按需记录进程状态、调用栈、内存排行或运行时快照。

| 问题 | 追踪器 | 采集结果 |
| --- | --- | --- |
| 宿主机系统态 CPU 突增 | `cpusys` | 宿主机 CPU 火焰图及触发指标 |
| 容器 CPU 突增 | `cpuidle` | 容器 CPU 火焰图及触发指标 |
| 容器 D 状态负载升高 | `dload` | 进程状态统计与 D 状态进程内核栈 |
| 磁盘 I/O 异常 | `iotracing` | 进程及文件 I/O 速率、I/O 调度等待栈 |
| 宿主机匿名内存突增 | `memburst` | RSS 占用最高的进程列表及运行时内存快照 |

阈值触发后才开始采集，结果不能还原触发前的完整执行过程。分析时应结合异常发生时间、资源指标和业务请求。

## 配置

在 `huatuo-bamai.conf` 中配置 `AutoTracing`。各字段的默认值、单位和限制见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)的“自动追踪”章节。

| 追踪器 | 配置节 |
| --- | --- |
| `cpusys` | `AutoTracing.CPUSys` |
| `cpuidle` | `AutoTracing.CPUIdle` |
| `dload` | `AutoTracing.Dload` |
| `iotracing` | `AutoTracing.IOTracing` |
| `memburst` | `AutoTracing.MemoryBurst` |

运行前检查全局黑名单、目标容器过滤规则及采集工具是否可用。冷却时间和适用范围取决于具体追踪器；本文的默认阈值指内置默认值，配置文件可覆盖这些值。

中断利用率异常和容器内存阈值快照的配置也见节点配置文档。本文重点说明上表五类追踪结果。

## 结果

自动追踪事件按已启用的存储配置写入 Elasticsearch 或本地文件。按主机、容器、时间范围及追踪器筛选后，结合业务请求分析。

CPU 采集使用 `perf` 时，火焰图数据内嵌在事件中；使用 `profiler` 时，事件保留触发指标，profile 独立存入 Elasticsearch，并通过同一 `tracer_id` 关联。存储要求与查看方式见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md#7-自动追踪)。

### 公共字段

| 字段 | 用途 |
| --- | --- |
| `hostname` / `container_id` | 定位节点或容器；容器字段仅在关联成功时提供 |
| `tracer_name` / `tracer_id` | 区分追踪器与本次追踪 |
| `started_timestamp` | 对齐异常时间 |
| `tracer_type` | 自动追踪记录为 `autotracing` |
| `tracer_data` | 触发指标与采集结果 |

完整记录结构见[自动追踪字段](/docs/reference/autotracing_zh.md#公共字段)。

### 宿主机系统态 CPU

追踪器：`cpusys`。通过 `AutoTracing.CPUSys.Tool` 选择采集工具，默认 `perf`。

系统态 CPU 占用率及其相邻采样增量同时超过阈值时，启动宿主机 CPU 采样。默认阈值分别为 45% 和 20 个百分点，冷却时间为 30 分钟。

结果包含触发指标和 CPU 火焰图，用于检查系统态 CPU 消耗较高的调用路径。

字段及示例见[自动追踪字段](/docs/reference/autotracing_zh.md#宿主机系统态-cpu)。

### 容器 CPU

追踪器：`cpuidle`。通过 `AutoTracing.CPUIdle.Tool` 选择采集工具，默认 `perf`。

容器用户态、系统态或总 CPU 占用率中的任一项，同时超过占用率阈值和增量阈值时，启动容器级 CPU 采样。默认冷却时间为 30 分钟，目标范围由 `AutoTracing.CPUIdle.Filter` 控制。

结果包含各项占用率、增量及容器 CPU 火焰图，用于检查容器内的 CPU 热点。

字段及示例见[自动追踪字段](/docs/reference/autotracing_zh.md#容器-cpu)。

### 容器 D 状态负载

追踪器：`dload`。

容器 D 状态负载的指数加权移动平均值（EMA）超过阈值时，采集容器及宿主机 D 状态进程的内核栈。默认阈值为 5，同一容器冷却时间为 30 分钟。

结果同时记录进程状态统计。可结合调用栈分析阻塞位置，并通过 `AutoTracing.IssuesList` 标记已知问题。

字段及示例见[自动追踪字段](/docs/reference/autotracing_zh.md#容器-d-状态负载)。

### 磁盘 I/O

追踪器：`iotracing`。

每 5 秒读取 `/proc/diskstats`。触发条件需连续两个采样周期成立：非 NVMe 设备检查利用率；NVMe 设备要求利用率和读、写吞吐率之一同时超阈值；各设备也会独立检查读、写平均等待时间。

结果包含进程及文件 I/O 速率、I/O 调度等待栈，用于检查 I/O 消耗来源和等待路径。

字段及示例见[自动追踪字段](/docs/reference/autotracing_zh.md#磁盘-io)。

### 宿主机匿名内存

追踪器：`memburst`。

默认每 10 秒采样一次，填满 60 个采样点后开始判断。当前匿名内存达到窗口最早值的 2 倍，且占宿主机总内存至少 70% 时触发；默认冷却时间为 30 分钟。

默认记录 RSS 占用最高的前 10 个进程，并尝试为排名前三的进程采集运行时内存快照，用于缩小内存消耗的排查范围。快照支持取决于目标运行时；采集失败或不可用时保留状态说明，不代表进程没有内存消耗。

字段及示例见[自动追踪字段](/docs/reference/autotracing_zh.md#宿主机匿名内存)。
