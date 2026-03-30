---
title: 异常事件
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

HUATUO 华佗平台通过 eBPF 技术实时检测 Linux 内核中的多种异常事件，帮助用户快速定位系统、应用及硬件相关问题。

## 事件列表

| 事件名称               | 核心功能                                           | 典型场景                               |
| ---------------------- | -------------------------------------------------- | -------------------------------------- |
| softirq                | 检测内核关闭软中断时间过长，输出调用栈、进程信息等 | 解决系统卡顿、网络延迟、调度延迟等问题 |
| softlockup             | 检测系统 softlockup 事件，提供目标进程及内核栈信息 | 定位和解决系统 softlockup 问题         |
| hungtask               | 检测 hungtask 事件，输出所有 D 状态进程及栈信息    | 定位瞬时批量 D 进程场景，保留故障现场  |
| oom                    | 检测宿主机或容器内的 OOM 事件                      | 聚焦内存耗尽问题，提供详细故障快照     |
| memory_reclaim_events  | 检测内存直接回收事件，记录回收耗时、进程及容器信息 | 解决内存压力导致的业务卡顿问题         |
| ras                    | 检测 CPU、Memory、PCIe 等硬件故障事件              | 及时感知硬件故障，降低业务影响         |
| dropwatch              | 检测内核网络协议栈丢包，输出调用栈及网络上下文     | 解决协议栈丢包导致的业务毛刺和延迟     |
| net_rx_latency         | 检测协议栈收包路径（驱动、协议、用户态）的延迟事件 | 解决接收延迟引起的业务超时和毛刺       |
| netdev_events          | 检测网卡链路状态变化                               | 感知网卡物理链路故障                   |
| netdev_bonding_lacp    | 检测 bonding LACP 协议状态变化                     | 界定物理机与交换机故障边界             |
| netdev_txqueue_timeout | 检测网卡发送队列超时事件                           | 定位网卡发送队列硬件故障               |


## 详细说明

### 通用字段说明

- **hostname**: 物理机 hostname
- **region**：物理机所在可用区
- **uploaded_time**：数据上传时间
- **container_id**：如果事件关联容器，则记录的容器 id
- **container_hostname**：如果事件关联容器，则记录的容器 hostname
- **container_host_namespace**：如果事件关联容器，则记录容器的 K8s 命名空间
- **container_type**：记录容器类型，例如 normal 普通容器，sidecar 边车容器等
- **container_qos**：记录容器级别
- **tracer_name**: 事件名称
- **tracer_id**：此次的 tracing id
- **tracer_time**：触发 tracing 时间
- **tracer_type**：类型，手动触发还是自动触发
- **tracer_data**：特定 tracer 私有数据

### 1. softirq 软中断关闭

**功能描述** 检测内核关闭中断时间过长时触发，记录关闭软中断的内核调用栈、当前进程信息等关键数据，帮助分析中断相关延迟问题。

**数据存储** 事件数据自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**（部分展示）

```json
{
	"uploaded_time": "2025-06-11T16:05:16.251152703+08:00",
	"hostname": "***",
	"tracer_data": {
		"comm": "***-agent",
		"stack": "scheduler_tick/...",
		"now": 5532940660025295,
		"offtime": 237328905,
		"cpu": 1,
		"threshold": 100000000,
		"pid": 688073
	},
	"tracer_time": "2025-06-11 16:05:16.251 +0800",
	"tracer_type": "auto",
	"time": "2025-06-11 16:05:16.251 +0800",
	"region": "***",
	"tracer_name": "softirq"
}
```

**字段含义解释**

- **comm**：触发事件的进程名称
- **stack**：内核调用栈（显示关闭中断期间的函数调用路径）
- **now**：当前时间戳
- **offtime**：关闭中断的持续时间（纳秒）
- **cpu**：发生事件的 CPU 编号
- **threshold**：触发阈值（纳秒），超过该值则记录事件
- **pid**：触发事件的进程 ID

### 2. dropwatch 协议栈丢包

**功能描述** 检测内核网络协议栈中的丢包行为，输出丢包时的调用栈、网络地址等信息，用于排查网络丢包导致的业务异常。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**（部分展示）

```json
"tracer_data": {
	"comm": "kubelet",
	"stack": "kfree_skb/...",
	"saddr": "10.79.68.62",
	"pid": 1687046,
	"type": "common_drop",
	"queue_mapping": ...
}
```

**字段含义解释**：

- **comm**：触发丢包的进程名称
- **stack**：丢包发生时的内核调用栈
- **saddr**：源 IP 地址
- **pid**：进程 ID
- **type**：丢包类型（如 common_drop）
- **queue_mapping**：网卡队列映射信息（具体值视实际丢包场景而定）

### 3. net_rx_latency 协议栈延迟

**功能描述** 检测协议栈接收方向（网卡驱动 → 内核协议栈 → 用户态主动收包）的延迟事件。当单个数据包从网卡进入到用户态接收的整体延迟超过阈值（默认 90 秒）时触发，记录详细的网络上下文信息（如五元组、TCP 序列号、延迟位置等），帮助排查协议栈或应用接收延迟导致的业务超时、毛刺等问题。

**典型场景** 解决因协议栈接收延迟、应用响应慢等引起的网络性能问题。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**

```json
"tracer_data": {
	"comm": "nginx",
	"pid": 2921092,
	"saddr": "10.156.248.76",
	"daddr": "10.134.72.4",
	"sport": 9213,
	"dport": 49000,
	"seq": 1009085774,
	"ack_seq": 689410995,
	"state": "ESTABLISHED",
	"pkt_len": 26064,
	"where": "TO_USER_COPY",
	"latency_ms": 95973
},
```

**字段含义解释**：

- **comm**：触发事件的进程名称
- **pid**：触发事件的进程 ID
- **saddr / daddr**：源 IP / 目的 IP 地址
- **sport / dport**：源端口 / 目的端口
- **seq / ack_seq**：TCP 序列号 / 确认序列号
- **state**：TCP 连接状态（如 ESTABLISHED）
- **pkt_len**：数据包长度（字节）
- **where**：延迟发生的位置（例如 TO_USER_COPY 表示用户态拷贝阶段）
- **latency_ms**：实际延迟时间（毫秒）

### 4. oom 内存耗尽

**功能描述** 检测宿主机或容器内发生的 OOM（Out of Memory）事件，记录被 OOM Killer 杀掉的进程（victim）与触发 OOM 的进程（trigger）信息，以及对应的容器和 memory cgroup 详情，提供完整的故障快照。

**典型场景** 聚焦物理机或容器内存耗尽问题，快速定位内存不可用导致的业务故障。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**

```json
"tracer_data": {
	"victim_process_name": "java",
	"victim_pid": 3218745,
	"victim_container_hostname": "***.docker",
	"victim_container_id": "***",
	"victim_memcg_css": "0xff4b8d8be3818000",
	"trigger_process_name": "java",
	"trigger_pid": 3218804,
	"trigger_container_hostname": "***.docker",
	"trigger_container_id": "***",
	"trigger_memcg_css": "0xff4b8d8be3818000"
},
```

**字段含义解释**：

- **victim_process_name / victim_pid**：被 OOM Killer 杀掉的进程名称与 PID
- **victim_container_hostname / victim_container_id**：被杀进程所在的容器主机名与容器 ID
- **victim_memcg_css**：被杀进程对应的 memory cgroup 指针（十六进制）
- **trigger_process_name / trigger_pid**：触发 OOM 的进程名称与 PID
- **trigger_container_hostname / trigger_container_id**：触发进程所在的容器主机名与容器 ID
- **trigger_memcg_css**：触发进程对应的 memory cgroup 指针

### 5. softlockup 软锁死

**功能描述** 检测系统 softlockup 事件（CPU 长时间无法调度，默认阈值约 1 秒），提供导致锁死的目标进程信息、所在 CPU 以及该 CPU 的内核调用栈，并记录事件发生次数。

**典型场景** 解决系统出现 softlockup 导致的卡死或响应异常问题。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

### 6. hungtask 任务挂起/D 状态进程

**功能描述** 检测系统 hungtask 事件，捕获当前所有处于 D 状态（不可中断睡眠）的进程内核栈，并记录 D 进程总数及各 CPU 的回溯信息，用于保留故障现场。

**典型场景** 定位瞬时批量出现 D 状态进程的场景，便于后续问题跟踪和分析。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**

```json
"tracer_data": {
	"cpus_stack": "2025-06-10 09:57:14 sysrq: Show backtrace of all active CPUs\nNMI backtrace for cpu 33\n...",
	"pid": 2567042,
	"d_process_count": "...",
	"blocked_processes_stack": "..."
},
```

**字段含义解释**：

- **cpus_stack**：所有 CPU 的 NMI 回溯信息（多行文本，包含时间戳和栈内容）
- **pid**：触发 hungtask 检测的进程 PID
- **d_process_count**：当前系统 D 状态进程总数
- **blocked_processes_stack**：D 状态进程的内核栈信息

### 7. memory_reclaim_events 内存回收

**功能描述** 检测系统直接内存回收（direct reclaim）事件，当同一进程在 1 秒内直接回收时间超过阈值（默认约 900 ms）时触发，记录回收耗时、进程及容器信息。

**典型场景** 解决系统内存压力过大导致的业务进程卡顿等问题。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**

```json
	"tracer_data": {
	"comm": "chrome",
	"pid": 1896137,
	"deltatime": 1412702917
	},
```

**字段含义解释**：

- **comm**：触发内存回收的进程名称
- **pid**：触发进程的 PID
- **deltatime**：直接回收耗时（纳秒）

### 8. netdev_events 网络设备

**功能描述** 检测网卡链路状态变化事件（包括 down/up、MTU 变更、AdminDown、CarrierDown 等），输出接口名称、状态描述、MAC 地址等信息。

**典型场景** 及时感知网卡物理链路问题，解决因网卡故障导致的业务不可用。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**

```json
"tracer_data": {
	"ifname": "eth1",
	"linkstatus": "linkStatusAdminDown, linkStatusCarrierDown",
	"mac": "5c:6f:69:34:dc:72",
	"index": 3,
	"start": false
},
```

**字段含义解释**：

- **ifname**：网络接口名称（如 eth1）
- **linkstatus**：链路状态具体描述
- **mac**：网卡 MAC 地址
- **index**：接口索引
- **start**：接口是否处于启动状态（true/false）

### 9. netdev_bonding_lacp LACP 协议

**功能描述** 检测 bonding 模式下 LACP（Link Aggregation Control Protocol）协议的状态变化，记录详细的 bonding 配置信息，包括模式、MII 状态、Actor/Partner 信息、Slave 链路状态等（完整输出 /proc/net/bonding/bondX 内容）。

**典型场景** 界定 bonding 模式下物理机或交换机侧的故障，解决 LACP 协商抖动等问题。

**数据存储** 自动存储至 Elasticsearch 或物理机磁盘文件。

**示例数据**（content 字段为完整文本）

```json
"tracer_data": {
	"content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0...\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nMII Status: down\n..."
},
```

**字段含义解释**：

- **content**：完整的 bonding 接口状态信息（多行文本，包含所有 Slave 的 LACP 协商细节）
