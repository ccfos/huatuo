---
title: 异常事件字段
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

本文说明异常事件的记录结构、字段和适用限制。功能介绍见[异常事件诊断](/docs/key-feature/instant-observability_zh.md)，采集设置见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)。

## 事件记录

结果按已启用的存储配置写入 Elasticsearch 或本地文件。查询时按 `tracer_type=event`、事件名称、主机或容器及时间范围筛选。下列 JSON 为结构示例，省略部分公共字段；其中阈值说明使用内置默认值。

容器字段仅在关联到容器时提供；具体事件数据位于 `tracer_data`。

| 字段 | 说明 |
| --- | --- |
| `hostname` | 节点主机名 |
| `region` | 节点所属区域 |
| `uploaded_timestamp` | 数据上传时间 |
| `container_id` | 容器 ID |
| `container_hostname` | 容器主机名 |
| `container_host_namespace` | 容器的 Kubernetes 命名空间 |
| `container_type` | 容器类型，例如 `normal` 普通容器，`sidecar` 边车容器等 |
| `container_qos` | 容器 QoS 级别 |
| `tracer_name` | 事件名称（如 `sched_tick`、`memory_oom_kill` 等） |
| `tracer_id` | 本次追踪 ID |
| `observed_timestamp` | 用户态观测时间 |
| `tracer_type` | 观测类型，即时事件记录固定为 `event` |
| `tracer_data` | 特定事件私有数据（详见各事件说明） |

## CPU 调度

### 调度 tick 间隔

观测器：`sched_tick`。

检测相邻调度 tick 的总间隔；间隔达到阈值时记录当前内核调用栈和进程信息。该事件可发现 IRQ 长时间关闭、CPU 停顿或虚拟化调度延迟等问题，但不能单独证明软中断被禁用。`comm` 和 `pid` 表示上报 tick 所中断的任务，不表示造成延迟的任务。

**适用场景**

- 内核或驱动长时间关闭本地中断，或 hardirq/NMI 长时间占用 CPU。
- 虚拟机 vCPU 被宿主机长时间抢占，包括高 steal time 和调度停顿。
- SMI、固件停顿、clockevent 延迟投递或丢失等底层异常。

**使用边界**

成功进入 NO_HZ 的正常停 tick 会被过滤。普通 CPU 高负载或 softirq backlog 本身不等同于 tick 延迟。

阈值应高于目标机器的正常 tick 周期；事件栈仅表示延迟结束后首个 tick 的现场，需要结合 IRQ、steal time 和硬件指标定位根因。

示例（省略部分公共字段）：

```json
{
    "uploaded_timestamp": "2025-06-11T16:05:16.251152703+08:00",
    "hostname": "***",
    "tracer_data": {
        "tick_interval_ns": 237328905,
        "tick_interval_threshold_ns": 10000000,
        "comm": "***-agent",
        "pid": 688073,
        "cpu": 1,
        "stack": "scheduler_tick/..."
    },
    "observed_timestamp": "2025-06-11T16:05:16.251+08:00",
    "tracer_type": "event",
    "region": "***",
    "tracer_name": "sched_tick"
}
```

| 字段 | 说明 |
| --- | --- |
| `comm` | 上报 tick 所中断任务的进程名称 |
| `stack` | 延迟结束后的首个调度 tick 上采集的内核调用栈 |
| `tick_interval_ns` | 相邻调度 tick 的总间隔（纳秒） |
| `cpu` | 发生事件的 CPU 编号 |
| `tick_interval_threshold_ns` | 调度 tick 间隔阈值（纳秒），达到该值则记录事件 |
| `pid` | 上报 tick 所中断任务的进程 ID |

### 软锁死

观测器：`softlockup`。

记录内核报告 softlockup 时的进程、CPU 和 NMI 回溯信息；这些信息不能单独确定致因进程。触发时间由内核 watchdog 配置决定。

重复事件的上报间隔从 10 分钟递增至最长 3 小时，期间仍累计事件计数。

示例（省略部分公共字段）：

```json
{
    "tracer_data": {
        "cpu": 15,
        "pid": 12345,
        "comm": "kworker/15:0",
        "cpus_stack": "2025-06-10 14:30:22 sysrq: Show backtrace of all active CPUs\nNMI backtrace for cpu 15\n..."
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `cpu` | 发生 softlockup 的 CPU 编号 |
| `pid` | 报告 softlockup 时的进程 PID |
| `comm` | 报告 softlockup 时的进程名称 |
| `cpus_stack` | 所有 CPU 的 NMI 回溯信息（多行文本，包含时间戳和调用栈） |

### 任务阻塞

观测器：`hungtask`。

检测系统 hungtask 事件，采集 D 状态（不可中断睡眠）进程的内核栈及 CPU 回溯信息。重复事件的上报间隔从 10 分钟递增至最长 3 小时，期间仍累计事件计数。内核未提供 `hung_task_timeout_secs` 时不注册观测器；将该值设为 `0` 会禁用内核 hungtask 检测。

示例（省略部分公共字段）：

```json
{
    "tracer_data": {
        "tid": 2567042,
        "comm": "kworker/u48:2",
        "cpus_stack": "2025-06-10 09:57:14 sysrq: Show backtrace of all active CPUs\nNMI backtrace for cpu 33\n...",
        "blocked_processes_stack": "task:java            state:D stack:    0 pid: 12345 ..."
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `tid` | 触发 hungtask 检测的任务 TID |
| `comm` | 触发 hungtask 检测的任务名称 |
| `cpus_stack` | 所有 CPU 的 NMI 回溯信息（多行文本，包含时间戳和调用栈） |
| `blocked_processes_stack` | D 状态进程的内核栈信息 |
| `hung_task_timeout_secs` | 内核 hungtask 检测超时设置，单位为秒 |

## 内存

### OOM 终止

观测器：`memory_oom_kill`。

记录 OOM Killer 的触发进程（`trigger`）与被终止进程（`victim`），以及可获取的容器、cgroup 和宿主机内存信息。触发进程不一定是主要内存消耗者，需结合使用量和内存限制分析。

示例（省略部分字段）：

```json
{
    "tracer_name": "memory_oom_kill",
    "tracer_data": {
        "trigger": {
            "memory_cgroup_css_addr": "0xff4b8d8be3818000",
            "container_id": "example-container",
            "container_hostname": "app-pod",
            "pid": 3218804,
            "comm": "java"
        },
        "victim": {
            "memory_cgroup_css_addr": "0xff4b8d8be3818000",
            "container_id": "example-container",
            "container_hostname": "app-pod",
            "pid": 3218745,
            "comm": "java",
            "cgroup": {
                "path": "kubepods.slice/...",
                "current": 2143289344,
                "max": 2147483648,
                "stat": {
                    "anon": 1509949440,
                    "file": 83886080
                },
                "events": {
                    "oom": 1,
                    "oom_kill": 1
                }
            }
        },
        "memory_snapshot": {
            "host_meminfo": {
                "MemAvailable": 3355443200,
                "Cached": 1073741824,
                "Slab": 268435456
            }
        }
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `trigger` / `victim` | 触发进程与被终止进程，均使用下列进程字段 |
| `pid` / `comm` | PID 与进程命令名 |
| `container_id` / `container_hostname` | 关联到的容器 ID 与主机名；未关联时省略 |
| `memory_cgroup_css_addr` | memory cgroup 指针的十六进制表示 |
| `cgroup.path` | 容器 cgroup 路径 |
| `cgroup.current` / `cgroup.max` | 读取快照时的内存使用量与上限，单位为字节 |
| `cgroup.stat` / `cgroup.events` | 原始 cgroup 内存统计与事件计数；字段依 cgroup 版本而异 |
| `memory_snapshot.host_meminfo` | 宿主机 `/proc/meminfo` 选定字段，统一转换为字节 |

`cgroup` 位于 `trigger` 或 `victim` 内，仅在容器关联和快照读取成功时提供。宿主机快照也可能缺失；这些信息在用户态读取，并非与 OOM 发生时刻严格同步。

### 直接回收

观测器：`memory_reclaim_events`。

记录容器线程单次直接内存回收的耗时。`try_to_free_pages` 返回时，若本次调用超过阈值（默认 900 毫秒），则上报进程、线程和容器信息。未关联到容器的事件会被过滤。黑名单使用注册名 `memory_reclaim_events`，落盘记录的 `tracer_name` 为 `memory_reclaim`。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "memory_reclaim",
    "tracer_data": {
        "pid": 1896137,
        "tid": 1896138,
        "comm": "java",
        "reclaim_duration_ns": 1412702917
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `comm` | 触发直接内存回收的进程名称 |
| `pid` | 触发进程的 PID |
| `tid` | 触发线程的 TID |
| `reclaim_duration_ns` | 直接回收耗时（纳秒） |

## 网络

### 协议栈丢包

观测器：`dropwatch`。

检测内核网络协议栈中的丢包行为，输出丢包时的内核调用栈、网络五元组、TCP 状态等信息。可通过可选的调用栈过滤器抑制经本机验证的噪声模式。`type` 字段为兼容 TCP 丢包类型而预留，当前未赋值。

示例（省略部分公共字段）：

```json
{
    "observed_timestamp": "2026-07-23T02:14:40.304775546Z",
    "tracer_data": {
        "drop_reason": "SKB_DROP_REASON_NOT_SPECIFIED",
        "source": "events",
        "comm": "kubelet",
        "pid": 1687046,
        "net_namespace_cookie": 123456789,
        "net_namespace_inum": 402653184,
        "netdev_queue_mapping": 3,
        "netdev_linkstatus": ["linkStatusUp"],
        "netdev_name": "eth0",
        "netdev_ifindex": 2,
        "packet_eth_proto": "0x0800",
        "packet_len_bytes": 1460,
        "layers": {
            "label": "IPv4/TCP",
            "ipv4": {
                "saddr": "10.79.68.62",
                "daddr": "10.134.72.4",
                "protocol": "TCP"
            },
            "tcp": {
                "sport": 8080,
                "dport": 49000,
                "seq": 1009085774,
                "ack_seq": 689410995,
                "sk_state": "ESTABLISHED"
            }
        },
        "stack": "kfree_skb/..."
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `type` | 预留丢包类型，当前未赋值且不会出现在 JSON 中；预留编码为 `1`（通用丢包）、`2`（SYN 洪泛）、`3`（半连接队列溢出）、`4`（全连接队列溢出） |
| `drop_reason` | 内核数据包丢弃原因 |
| `source` | 事件来源（独立运行 dropwatch 时为 `tools`，由 huatuo-bamai 启动时为 `events`） |
| `comm` | 触发丢包的进程名称 |
| `pid` | 进程 ID |
| `net_namespace_cookie` / `net_namespace_inum` | 用于容器归属解析的网络命名空间值 |
| `netdev_queue_mapping` | 网卡队列索引 |
| `netdev_linkstatus` | 网卡链路状态标志列表 |
| `netdev_name` | 网卡设备名称 |
| `netdev_ifindex` | 网卡接口索引 |
| `packet_len_bytes` | 数据包长度（字节） |
| `packet_eth_proto` | 以太网协议类型的十六进制表示 |
| `layers.ipv4.saddr` / `layers.ipv4.daddr` | 源 IP 和目的 IP 地址 |
| `layers.tcp.sport` / `layers.tcp.dport` | 源端口和目的端口 |
| `layers.tcp.seq` / `layers.tcp.ack_seq` | TCP 序列号和确认序列号 |
| `layers.tcp.sk_state` | 丢包时 TCP 连接状态 |
| `stack` | 丢包发生时的内核调用栈 |

### 接收延迟

观测器：`net_rx_latency`。

记录 TCP 报文从驱动接收点到 `__netif_receive_skb`、`tcp_v4_rcv` 和用户态拷贝点的延迟。这三个值均从驱动起算，不是相邻观测点之间的耗时；默认阈值依次为 5、10、115 毫秒。达到对应阈值时记录端点、TCP 状态、序列号及延迟值。

默认排除宿主机网络命名空间，仅观测容器网络。

示例（省略部分公共字段）：

```json
{
    "observed_timestamp": "2025-06-11T00:00:00Z",
    "tracer_data": {
        "comm": "nginx",
        "pid": 2921092,
        "latency_stage": "RX_STAGE_USERCOPY",
        "latency_ms": 95973,
        "latency_threshold_ms": 115,
        "tcp_state": "ESTABLISHED",
        "tcp_saddr": "10.156.248.76",
        "tcp_daddr": "10.134.72.4",
        "tcp_sport": 9213,
        "tcp_dport": 49000,
        "tcp_seq": 1009085774,
        "tcp_ack_seq": 689410995,
        "net_namespace_cookie": 123456789,
        "net_namespace_inum": 402653184,
        "packet_len_bytes": 26064
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `comm` | 触发事件的进程名称 |
| `pid` | 触发事件的进程 ID |
| `latency_stage` | 延迟发生的阶段（`RX_STAGE_NETIF` 网卡到内核 / `RX_STAGE_TCPV4` 驱动到 TCP / `RX_STAGE_USERCOPY` 驱动到用户态拷贝） |
| `latency_ms` | 实际延迟时间（毫秒） |
| `latency_threshold_ms` | 触发事件的延迟阈值（毫秒） |
| `tcp_state` | TCP 连接状态（支持所有状态，如 `ESTABLISHED`、`SYN_SENT`、`FIN_WAIT`、`TIME_WAIT` 等） |
| `tcp_saddr` / `tcp_daddr` | 源 IP / 目的 IP 地址 |
| `tcp_sport` / `tcp_dport` | 源端口 / 目的端口 |
| `tcp_seq` / `tcp_ack_seq` | TCP 序列号 / 确认序列号 |
| `net_namespace_cookie` | 网络命名空间 cookie（内核 ≥ 5.14 可用，用于高效容器关联） |
| `net_namespace_inum` | 网络命名空间 inum |
| `packet_len_bytes` | 数据包长度（字节） |

### 网卡状态

观测器：`netdev_events`。

通过订阅内核 netlink RTM_NEWLINK 消息，检测网卡链路状态标志变化（如 AdminDown、CarrierDown），输出接口名称、链路状态、MAC 地址及驱动信息。观测器启动时会扫描 `EventTracing.Netdev.DeviceList` 中配置的网卡当前状态作为基线，后续仅上报状态变化事件。

示例（省略部分公共字段）：

```json
{
    "tracer_data": {
        "ifname": "eth1",
        "index": 3,
        "linkstatus": "linkStatusAdminDown, linkStatusCarrierDown",
        "mac": "5c:6f:69:34:dc:72",
        "start": false,
        "driver": "ixgbe",
        "driver_version": "5.1.0-k",
        "firmware_version": "3.25 0x80000421 1.2163.0"
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `ifname` | 网络接口名称（如 `eth1`） |
| `index` | 接口索引号 |
| `linkstatus` | 链路状态变化描述（可包含多个状态） |
| `mac` | 网卡 MAC 地址 |
| `start` | 启动扫描标记；当前只保存实时变化事件，值为 `false` |
| `driver` | 网卡驱动名称 |
| `driver_version` | 网卡驱动版本 |
| `firmware_version` | 网卡固件版本 |

### LACP 状态

观测器：`netdev_bonding_lacp`。

仅在系统存在 IEEE 802.3ad bonding 接口时启用。LACP（Link Aggregation Control Protocol）状态变化时，读取并记录 `/proc/net/bonding/` 下所有 bonding 接口的状态，包括模式、MII 状态、Actor/Partner 协商参数和 Slave 链路状态。

落盘记录的 `tracer_name` 为 `lacp`，配置黑名单使用注册名 `netdev_bonding_lacp`。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "lacp",
    "tracer_data": {
        "content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0...\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nMII Status: down\n..."
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `content` | 完整的 bonding 接口状态信息（多行文本，包含所有 Slave 的 LACP 协商细节，等同于 `/proc/net/bonding/bondX` 文件内容） |

### 发送队列超时

观测器：`netdev_txqueue_timeout`。

检测网卡发送队列超时（TX queue timeout）事件，记录发生超时的队列索引、设备名称和驱动名称，用于排查发送方向的设备、驱动或链路异常。

示例（省略部分公共字段）：

```json
{
    "tracer_data": {
        "queue_index": 3,
        "device_name": "eth0",
        "driver_name": "ixgbe"
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `queue_index` | 发生超时的发送队列索引 |
| `device_name` | 网卡设备名称 |
| `driver_name` | 网卡驱动名称 |

## 兼容性

### OOM 名称迁移

OOM kill 追踪器现命名为 `memory_oom_kill`（此前为 `memory_oom`，更早为 `oom`），需同步更新黑名单、事件过滤器和告警查询。

指标前缀现为 `huatuo_bamai_memory_oom_kill_`，替代 `huatuo_bamai_memory_oom_` 及更早的 `huatuo_bamai_oom_`，需同步更新指标仪表盘和记录规则。

历史事件保留原名称，跨版本查询需匹配这三个名称；内置事件仪表盘已覆盖三者。

### attribute hungtask events and counts to containers

| 事件名称（tracer_name） | 探针类型 | 触发条件 | 典型场景 |
| ----------------------- | -------- | -------- | -------- |
| `hungtask` | raw tracepoint；回退 tracepoint（仅主机） | D 状态进程任务挂起 | 瞬时批量 D 进程、IO 阻塞 |

`hungtask_container_total` 使用 raw tracepoint 在事件时刻记录的阻塞任务 cgroup 身份匹配容器，已匹配追踪附带 `ContainerID`，不再事后通过 TID 查询归属。容器元数据缺失或不支持身份采集时，仅保留主机总计。容器计数不受追踪保存退避影响；CPU 与阻塞任务栈仍为全机快照。层级匹配、回退及计数语义见 [HungTask 指标](kernel-wide-insight_zh.md#hungtask)。

        K1["kprobe 挂钩\n(sched_tick / softlockup\n memory_oom_kill / memory_reclaim_events\n net_rx_latency / netdev_txqueue_timeout\n tcp_retransmit TLP，可选)"]
        K2["raw tracepoint / tracepoint 挂钩\n(hungtask: raw tracepoint，\n 回退 tracepoint，仅主机\n ras: MCE / EDAC / AER / ACPI\n dropwatch: skb/kfree_skb\n tcp_retransmit:\n tcp/tcp_retransmit_skb /\n tcp/tcp_retransmit_synack)"]
