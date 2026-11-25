---
title: 异常事件诊断
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

## 概述

异常事件诊断记录 Linux 内核上报的调度、内存和网络异常，并关联进程、容器及调用栈等上下文，供事后排查使用。采集主要通过 eBPF 探针完成，网卡状态变化通过 netlink 获取。

| 问题 | 事件或观测器 |
| --- | --- |
| 调度停顿、软锁死、任务长期阻塞 | `sched_tick`、`softlockup`、`hungtask` |
| OOM、直接内存回收阻塞 | `memory_oom_kill`、`memory_reclaim_events` |
| 协议栈丢包、接收延迟 | `dropwatch`、`net_rx_latency` |
| 网卡状态、LACP、发送队列超时 | `netdev_events`、`netdev_bonding_lacp`、`netdev_txqueue_timeout` |
| TCP 重传 | 见 [tcpshark](/docs/best-practice/tcpshark_zh.md) |
| CPU、内存及 PCIe 硬件错误 | 见[硬件故障诊断](/docs/key-feature/hardware-events_zh.md) |

事件提供诊断线索，不能单独证明故障根因。探针支持、过滤和限速规则会影响观测范围；缓冲区不足时也可能丢失事件。

## 配置

在 `huatuo-bamai.conf` 的 `EventTracing` 下设置阈值和过滤规则。完整字段、默认值及单位见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)的“事件追踪”章节；全局黑名单控制观测器是否启用。

## 事件记录

事件按存储配置写入 Elasticsearch 或本地文件。通过 `tracer_type=event`、事件名称、主机或容器及时间范围筛选，查看对应的进程、调用栈和设备信息。容器字段仅在关联成功时提供。

完整 JSON 示例、字段单位和兼容性说明见[异常事件字段](/docs/reference/instant-observability_zh.md)。

## CPU 调度

### 调度 tick 间隔

观测器：`sched_tick`。

相邻调度 tick 的间隔达到阈值时，记录进程和内核栈，可用于排查中断长时间关闭、CPU 停顿或虚拟化调度延迟。阈值应高于正常 tick 周期；正常 NO_HZ 停 tick 会被过滤。

记录的是延迟结束后首个 tick 的现场。进程和调用栈不一定是延迟原因，普通 CPU 高负载或 softirq 积压也不等同于 tick 延迟。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#调度-tick-间隔)。

### 软锁死

观测器：`softlockup`。

记录内核报告 softlockup 时的进程、CPU 和 NMI 回溯信息；这些信息不能单独确定致因进程。触发时间由内核 watchdog 配置决定。

重复事件的上报间隔从 10 分钟递增至最长 3 小时，期间仍累计事件计数。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#软锁死)。

### 任务阻塞

观测器：`hungtask`。

检测系统 hungtask 事件，采集 D 状态（不可中断睡眠）进程的内核栈及 CPU 回溯信息。

重复事件的上报间隔从 10 分钟递增至最长 3 小时，期间仍累计事件计数。内核未提供 `hung_task_timeout_secs` 时不注册观测器；将该值设为 `0` 会禁用内核 hungtask 检测。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#任务阻塞)。

## 内存

### OOM 终止

观测器：`memory_oom_kill`。

记录 OOM Killer 的触发进程（`trigger`）与被终止进程（`victim`），以及可获取的容器、cgroup 和宿主机内存信息。触发进程不一定是主要内存消耗者，需结合使用量和内存限制分析。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#oom-终止)。

### 直接回收

观测器：`memory_reclaim_events`。

记录容器线程单次直接内存回收的耗时。`try_to_free_pages` 返回时，若本次调用超过阈值（默认 900 毫秒），则上报进程、线程和容器信息。未关联到容器的事件会被过滤。

黑名单使用注册名 `memory_reclaim_events`，落盘记录的 `tracer_name` 为 `memory_reclaim`。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#直接回收)。

## 网络

### 协议栈丢包

观测器：`dropwatch`。

检测内核网络协议栈中的丢包行为，输出丢包时的内核调用栈、网络五元组、TCP 状态等信息。可通过可选的调用栈过滤器抑制经本机验证的噪声模式。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#协议栈丢包)。

### 接收延迟

观测器：`net_rx_latency`。

记录 TCP 报文从驱动接收点到 `__netif_receive_skb`、`tcp_v4_rcv` 和用户态拷贝点的延迟。这三个值均从驱动起算，不是相邻观测点之间的耗时；默认阈值依次为 5、10、115 毫秒。达到对应阈值时记录端点、TCP 状态、序列号及延迟值。

默认排除宿主机网络命名空间，仅观测容器网络。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#接收延迟)。

### 网卡状态

观测器：`netdev_events`。

通过订阅内核 netlink RTM_NEWLINK 消息，检测网卡链路状态标志变化（如 AdminDown、CarrierDown），输出接口名称、链路状态、MAC 地址及驱动信息。

观测器启动时扫描 `EventTracing.Netdev.DeviceList` 中配置的网卡当前状态作为基线，后续仅上报状态变化事件。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#网卡状态)。

### LACP 状态

观测器：`netdev_bonding_lacp`。

仅在系统存在 IEEE 802.3ad bonding 接口时启用。LACP 状态变化时，记录 bonding 接口的模式、链路状态和协商信息，用于分析链路聚合异常。

黑名单使用注册名 `netdev_bonding_lacp`，查询记录时使用 `tracer_name=lacp`。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#lacp-状态)。

### 发送队列超时

观测器：`netdev_txqueue_timeout`。

检测网卡发送队列超时（TX queue timeout）事件，记录发生超时的队列索引、设备名称和驱动名称，用于排查发送方向的设备、驱动或链路异常。

详细字段见[异常事件字段](/docs/reference/instant-observability_zh.md#发送队列超时)。
