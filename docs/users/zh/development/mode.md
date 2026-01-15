---
title: 采集模式
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

为帮助用户全面深入洞察系统的运行状态，HUATUO 提供三种数据采集: `metrics`, `event`, `autotracing`. 用户可以根据具体场景和需求实现自己的观测数据采集。

### 模式
| 模式             | 类型           | 触发条件       | 数据存储          | 适用场景         |
|-------------    |----------------|--------------|------------------|-----------------|
| **Metrics**     | 指标数据     | Pull 采集    | Prometheus  | 系统性能指标   |
| **Event**       | 异常事件     | 内核事件触发 | ES + 本地存储，Prometheus（可选）| 常态运行，事件触发，获取内核运行上下文 |
| **Autotracing** | 系统异常     | 系统异常触发 | ES + 本地存储，Prometheus（可选）| 系统异常触发，获取例如火焰图数据 |

### 指标
- **类型**：指标采集。
- **功能**：采集内核各子系统指标数据。
- **特点**：
     - 通过 Procfs 或 eBPF 方式采集。
     - Prometheus 格式输出，最终集成到 Prometheus/Grafana。
     - 主要采集系统的基础指标，如 CPU 使用率、内存使用率、网络等。
     - 适合用于监控系统运行状态，支持实时分析和长期趋势观察。
- **已集成**：
    - CPU sys, usr, util, load, nr_running ...
    - Memory vmstat, memory_stat, directreclaim, asyncreclaim ...
    - IO d2c, q2c, freeze, flush ...
    - Networking arp, socket mem, qdisc, netstat, netdev, socketstat ...

### 事件
- **类型**：Linux 内核事件采集。
- **功能**：常态运行，事件触发并在达到预设阈值时，获取内核运行上下文。
- **特点**：
     - 常态运行，异常事件触发，支持阈值设定。
     - 数据实时存储 ElasticSearch、物理机本地文件。
     - 适合用于常态监控和实时分析，捕获系统更多异常行为观测数据。
- **已集成**：
    - 软中断异常 softirq
    - 内存异常分配 oom
    - 软锁定 softlockup
    - D 状态进程 hungtask
    - 内存回收 memreclaim
    - 异常丢包 dropwatch
    - 网络入向延迟 net_rx_latency

### 自动追踪
- **类型**：系统异常追踪
- **功能**：自动跟踪系统异常状态，并在异常发生时触发工具抓取现场信息。
- **特点**：
     - 系统出现异常时自动触发，捕获。
     - 数据实时存储 ElasticSearch、物理机本地文件。
     - 适用于获取现场时性能开销较大、指标突发的场景。
- **已集成**：
    - CPU 异常追踪
    - 进程 D 状态追踪
    - 容器内外争抢
    - 内存突发分配
    - 磁盘异常追踪

