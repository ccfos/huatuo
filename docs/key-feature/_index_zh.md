---
title: 核心特性
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 6
---

HUATUO 提供资源指标、异常事件、自动追踪和性能剖析能力。按排查目标选择功能：

| 排查目标 | 文档 | 主要结果 |
| --- | --- | --- |
| 查看节点和容器的资源状态 | [内核全景观测](/docs/key-feature/kernel-wide-insight_zh.md) | CPU、内存、网络、存储及加速器指标 |
| 排查已发生的系统异常 | [异常事件诊断](/docs/key-feature/instant-observability_zh.md) | 事件时间、进程和容器上下文、调用栈 |
| 在指标异常时自动采集现场 | [自动追踪](/docs/key-feature/autotracing_zh.md) | 触发指标、CPU 火焰图、I/O 和内存现场 |
| 主动分析 CPU 热点和内存分配 | [持续性能剖析](/docs/key-feature/continuous-profiling_zh.md) | CPU 时间、等待时间和内存分配的调用栈归因 |
| 排查 CPU、内存及 PCIe 硬件错误 | [硬件故障诊断](/docs/key-feature/hardware-events_zh.md) | RAS 错误计数、设备标识和错误详情 |

通常先用资源指标确定异常时间和范围，再结合事件或剖析结果分析。采集开关、阈值和存储设置见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)；任务管理与查询设置见[API 服务配置](/docs/configuration/huatuo-apiserver-configuration_zh.md)。

指标、事件字段及原始编码见[参考手册](/docs/reference/_index_zh.md)。
