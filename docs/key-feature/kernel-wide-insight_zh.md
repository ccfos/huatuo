---
title: 内核全景观测
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

## 概述

HUATUO 采集宿主机和容器的 CPU、内存、网络、I/O、系统事件及 GPU 指标，帮助确定异常发生的时间、资源类型和影响范围。

| 问题 | 重点指标 |
| --- | --- |
| CPU 繁忙或任务迟迟得不到调度 | 利用率、调度延迟、限流和等待占比 |
| 内存压力、回收阻塞或分配失败 | 内存状态、回收耗时、OOM 和空闲内存块 |
| 网络吞吐下降、丢包或连接异常 | 网卡、发送队列、TCP 和 Socket 统计 |
| 磁盘响应变慢 | I/O 延迟分布、吞吐量和请求耗时 |
| 系统卡顿或任务长期阻塞 | softlockup、hungtask 事件计数 |
| GPU 负载、显存或设备状态异常 | 利用率、显存、温度、功耗和互连指标 |

## 配置

实际输出取决于内核接口、设备驱动、采集器开关和过滤配置。部分采集器在仓库配置中默认禁用，启用方式见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)。

指标通过 `/metrics` 暴露。完整名称、单位、标签、示例及统计口径见[指标参考](/docs/reference/metrics_zh.md)。未输出指标不等于指标值为零。

## CPU

利用率反映 CPU 使用程度，调度延迟反映可运行任务等待 CPU 的时间。结合容器可用核数、限流和等待占比，可区分高计算负载、配额限制和资源争抢。

宿主机利用率按在线核数归一化，容器利用率按可用 CPU 容量归一化。宿主机平均负载与容器当前任务数的统计窗口不同，不应直接比较。

指标及分桶范围见[CPU 指标](/docs/reference/metrics_zh.md#cpu)。

## 内存

内存状态指标区分匿名内存、文件内存及活跃状态；回收耗时和事件计数用于分析内存压力造成的阻塞。空闲内存块按 NUMA 节点、区域和阶数统计，可辅助排查连续内存分配问题。

字段和单位依内核及 cgroup 版本而异。页数、字节数和事件次数需分别解读；部分指标依赖定制内核接口。统计条件详见[内存指标](/docs/reference/metrics_zh.md#内存)。

## 网络

网卡与发送队列指标用于观察吞吐、积压和丢包；TCP、Socket、连接跟踪和 ARP 统计用于分析连接及协议资源占用。

发送队列积压不等于排队延迟，聚合错误数可能已包含细分错误。分析时应确认统计口径，避免重复相加。指标及来源见[网络指标](/docs/reference/metrics_zh.md#网络)。

## I/O

I/O 延迟分布区分请求入队到完成（`q2c`）和驱动下发到完成（`d2c`）。磁盘统计提供请求数、吞吐量和累计耗时，XFS 指标补充文件系统日志及分配状态。

当前延迟分桶仅统计 `q2c`、`d2c` 均可计算且均不少于 20 毫秒的请求，不能代表全部 I/O。计算平均请求耗时时，应排除请求速率为零的区间。完整口径见[I/O 指标](/docs/reference/metrics_zh.md#io)。

## 系统事件

softlockup 和 hungtask 计数记录内核报告的软锁死与任务阻塞。计数变化用于确定异常时段，进程和调用栈信息见[异常事件诊断](/docs/key-feature/instant-observability_zh.md)。指标名称见[系统事件指标](/docs/reference/metrics_zh.md#系统事件)。

## GPU

### MetaX

采集 GPU 利用率、显存、温度、功耗、PCIe、MetaXLink 和 ECC 等指标。启用前需满足设备和 SDK 依赖，并检查 `metax_gpu` 黑名单配置。

指标列表见[MetaX 指标](/docs/reference/metrics_zh.md#metax)。

### MThreads

采集 GPU、显存、功耗、PCIe 和 MTLink 等指标，依赖 MTML 管理库。启用对应采集器后，使用 `MetricCollector.Mthreads` 控制指标组。

设备规格与实时吞吐需分别解读，依赖及指标列表见[MThreads 指标](/docs/reference/metrics_zh.md#mthreads)。

### Ascend

略
