---
title: Abnormal Event Fields
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 3
---

This reference covers abnormal event structures, fields, and limitations. See [Abnormal Event Diagnosis](/docs/key-feature/instant-observability_en.md) for capabilities and [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md) for collection settings.

## Event Records

Results are written to Elasticsearch or local files according to enabled storage settings. Filter by `tracer_type=event`, event name, host or container, and time range. The JSON examples illustrate structure and omit some common fields. Threshold descriptions use built-in defaults.

Container fields are provided only when a container is associated. Event-specific data is in `tracer_data`.

| Field | Description |
| --- | --- |
| `hostname` | Node hostname |
| `region` | Node region |
| `uploaded_timestamp` | Data upload time |
| `container_id` | Container ID |
| `container_hostname` | Container hostname |
| `container_host_namespace` | Container's Kubernetes namespace |
| `container_type` | Container type, such as `normal` or `sidecar` |
| `container_qos` | Container QoS class |
| `tracer_name` | Event name, such as `sched_tick` or `memory_oom_kill` |
| `tracer_id` | ID of this trace |
| `observed_timestamp` | User-space observation time |
| `tracer_type` | Observation type; always `event` for these records |
| `tracer_data` | Event-specific data described below |

## CPU Scheduling

### Scheduling Tick Intervals

Observer: `sched_tick`.

Measures the total interval between consecutive scheduling ticks. When the interval reaches the threshold, it records the current kernel stack and process information. This can reveal prolonged IRQ disabling, CPU stalls, or virtualization scheduling delays, but does not alone prove that softirqs were disabled. `comm` and `pid` identify the task interrupted by the reporting tick, not the task responsible for the delay.

**Use cases**

- Kernel or driver code disables local interrupts for a long time, or hardirq/NMI work occupies the CPU for too long.
- The host preempts a VM's vCPU for an extended period, including high steal time and scheduling stalls.
- Low-level anomalies such as SMI, firmware stalls, or delayed or lost clockevent delivery.

**Limitations**

Normal tick suppression after successfully entering NO_HZ is filtered out. High CPU load or a softirq backlog alone does not imply tick delay.

Set the threshold above the machine's normal tick interval. The event stack captures only the first tick after the delay ends; use IRQ, steal-time, and hardware metrics to investigate the cause.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `comm` | Process name of the task interrupted by the reporting tick |
| `stack` | Kernel stack captured at the first scheduling tick after the delay |
| `tick_interval_ns` | Total interval between consecutive scheduling ticks (nanoseconds) |
| `cpu` | CPU ID where the event occurred |
| `tick_interval_threshold_ns` | Tick interval threshold (nanoseconds); reaching it records an event |
| `pid` | Process ID of the task interrupted by the reporting tick |

### Soft Lockups

Observer: `softlockup`.

Records the process, CPU, and NMI backtraces when the kernel reports a soft lockup. These details alone cannot identify the responsible process. Kernel watchdog settings determine trigger timing.

The reporting interval for repeated events increases from 10 minutes to at most 3 hours. Events are still counted during this interval.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `cpu` | CPU ID where the soft lockup occurred |
| `pid` | Process PID when the soft lockup was reported |
| `comm` | Process name when the soft lockup was reported |
| `cpus_stack` | NMI backtraces from all CPUs as multiline text with timestamps and stacks |

### Hung Tasks

Observer: `hungtask`.

Matched hungtask events carry a container ID; unresolved events retain host output. Stack snapshots remain system-wide. See [container counts](metrics_en.md#attribute-hungtask-events-and-counts-to-containers).

Detects system hung-task events and collects kernel stacks of D-state (uninterruptible sleep) processes and CPU backtraces. The repeat reporting interval increases from 10 minutes to at most 3 hours, while events continue to be counted. The observer is not registered if the kernel does not provide `hung_task_timeout_secs`. Setting that value to `0` disables kernel hung-task detection.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `tid` | TID of the task that triggered hung-task detection |
| `comm` | Name of the task that triggered hung-task detection |
| `cpus_stack` | NMI backtraces from all CPUs as multiline text with timestamps and stacks |
| `blocked_processes_stack` | Kernel stacks of D-state processes |
| `hung_task_timeout_secs` | Kernel hung-task detection timeout in seconds |

## Memory

### OOM Kills

Observer: `memory_oom_kill`.

Records the process that triggered the OOM Killer (`trigger`), the killed process (`victim`), and available container, cgroup, and host memory information. The triggering process is not necessarily the main memory consumer; analyze usage and memory limits together.

Example with some fields omitted:

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

| Field | Description |
| --- | --- |
| `trigger` / `victim` | Triggering and killed processes, both using the process fields below |
| `pid` / `comm` | PID and process command name |
| `container_id` / `container_hostname` | Associated container ID and hostname; omitted without an association |
| `memory_cgroup_css_addr` | Hexadecimal memory cgroup pointer |
| `cgroup.path` | Container cgroup path |
| `cgroup.current` / `cgroup.max` | Memory usage and limit at snapshot time, in bytes |
| `cgroup.stat` / `cgroup.events` | Raw cgroup memory statistics and event counts; fields depend on the cgroup version |
| `memory_snapshot.host_meminfo` | Selected host `/proc/meminfo` fields, converted to bytes |

`cgroup` appears inside `trigger` or `victim` only when container attribution and snapshot reads succeed. The host snapshot may also be absent. These values are read in user space and are not strictly synchronized with the OOM event.

### Direct Reclaim

Observer: `memory_reclaim_events`.

Records the duration of an individual direct memory reclaim by a thread. When `try_to_free_pages` returns after exceeding the threshold (default: 900 milliseconds), it reports process, thread, and container details. Events without a resolved container are retained in the host stream with an output cooldown. Blacklists use the registration name `memory_reclaim_events`; stored records use `tracer_name=memory_reclaim`.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `comm` | Process name that triggered direct reclaim |
| `pid` | Triggering process PID |
| `tid` | Triggering thread TID |
| `reclaim_duration_ns` | Direct reclaim duration (nanoseconds) |

An empty container ID can mean a host task or unresolved container attribution. Unresolved events are limited to one output per second per tracer; excess events are discarded. Resolved container events are unaffected and are not duplicated in the host stream.

## Network

### Protocol Stack Drops

Observer: `dropwatch`.

Detects packet drops in the kernel network stack and reports kernel stacks, network five-tuples, TCP state, and other details. An optional stack filter can suppress noise patterns verified on the local system. `type` is reserved for TCP drop-type compatibility and is currently unset.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `type` | Reserved drop type, currently unset and absent from JSON. Reserved codes: `1` general drop, `2` SYN flood, `3` SYN backlog overflow, `4` accept queue overflow |
| `drop_reason` | Kernel packet-drop reason |
| `source` | `tools` for standalone dropwatch, `events` when started by huatuo-bamai |
| `comm` | Process name at the drop |
| `pid` | Process ID |
| `net_namespace_cookie` / `net_namespace_inum` | Network namespace values for container attribution |
| `netdev_queue_mapping` | NIC queue index |
| `netdev_linkstatus` | NIC link-state flags |
| `netdev_name` | NIC device name |
| `netdev_ifindex` | NIC interface index |
| `packet_len_bytes` | Packet length (bytes) |
| `packet_eth_proto` | Ethernet protocol type in hexadecimal |
| `layers.ipv4.saddr` / `layers.ipv4.daddr` | Source and destination IP addresses |
| `layers.tcp.sport` / `layers.tcp.dport` | Source and destination ports |
| `layers.tcp.seq` / `layers.tcp.ack_seq` | TCP sequence and acknowledgment numbers |
| `layers.tcp.sk_state` | TCP connection state at the drop |
| `stack` | Kernel stack at the drop |

### Receive Latency

Observer: `net_rx_latency`.

Records TCP packet latency from the driver receive point to `__netif_receive_skb`, `tcp_v4_rcv`, and the user-copy point. All three durations start at the driver, not at the preceding observation point. Default thresholds are 5, 10, and 115 milliseconds, respectively. Reaching a threshold records endpoints, TCP state, sequence numbers, and latency.

The host network namespace is excluded by default; only container networking is observed.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `comm` | Process name at the event |
| `pid` | Process ID at the event |
| `latency_stage` | Stage: `RX_STAGE_NETIF` (NIC to kernel), `RX_STAGE_TCPV4` (driver to TCP), or `RX_STAGE_USERCOPY` (driver to user copy) |
| `latency_ms` | Measured latency (milliseconds) |
| `latency_threshold_ms` | Trigger threshold (milliseconds) |
| `tcp_state` | TCP connection state; all states are supported, including `ESTABLISHED`, `SYN_SENT`, `FIN_WAIT`, and `TIME_WAIT` |
| `tcp_saddr` / `tcp_daddr` | Source/destination IP addresses |
| `tcp_sport` / `tcp_dport` | Source/destination ports |
| `tcp_seq` / `tcp_ack_seq` | TCP sequence/acknowledgment numbers |
| `net_namespace_cookie` | Network namespace cookie, available on kernels >= 5.14 for efficient container association |
| `net_namespace_inum` | Network namespace inum |
| `packet_len_bytes` | Packet length (bytes) |

### NIC State

Observer: `netdev_events`.

Subscribes to kernel netlink RTM_NEWLINK messages to detect NIC link-flag changes such as AdminDown or CarrierDown. It reports interface name, link state, MAC address, and driver details. At startup, it scans devices in `EventTracing.Netdev.DeviceList` to establish a baseline, then reports only state changes.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `ifname` | Network interface name, such as `eth1` |
| `index` | Interface index |
| `linkstatus` | Link-state change description, possibly containing multiple states |
| `mac` | NIC MAC address |
| `start` | Startup-scan flag; currently `false` because only live changes are saved |
| `driver` | NIC driver name |
| `driver_version` | NIC driver version |
| `firmware_version` | NIC firmware version |

### LACP State

Observer: `netdev_bonding_lacp`.

Enabled only when the system has an IEEE 802.3ad bonding interface. On LACP (Link Aggregation Control Protocol) state changes, it reads and records all bonding interfaces under `/proc/net/bonding/`, including mode, MII state, Actor/Partner negotiation parameters, and slave link states.

Stored records use `tracer_name=lacp`; configuration blacklists use the registration name `netdev_bonding_lacp`.

Example with some common fields omitted:

```json
{
    "tracer_name": "lacp",
    "tracer_data": {
        "content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0...\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nMII Status: down\n..."
    }
}
```

| Field | Description |
| --- | --- |
| `content` | Complete bonding interface status as multiline text, including all slaves' LACP negotiation details; equivalent to `/proc/net/bonding/bondX` contents |

### Transmit Queue Timeouts

Observer: `netdev_txqueue_timeout`.

Detects NIC TX queue timeouts and records the affected queue index, device name, and driver name to investigate transmit-side device, driver, or link anomalies.

Example with some common fields omitted:

```json
{
    "tracer_data": {
        "queue_index": 3,
        "device_name": "eth0",
        "driver_name": "ixgbe"
    }
}
```

| Field | Description |
| --- | --- |
| `queue_index` | Transmit queue index that timed out |
| `device_name` | NIC device name |
| `driver_name` | NIC driver name |

## Compatibility

### OOM Name Migration

The OOM kill tracer is now `memory_oom_kill`, previously `memory_oom` and earlier `oom`. Update blacklists, event filters, and alert queries together.

The metric prefix is now `huatuo_bamai_memory_oom_kill_`, replacing `huatuo_bamai_memory_oom_` and the earlier `huatuo_bamai_oom_`. Update metric dashboards and recording rules accordingly.

Historical events retain their original names. Cross-version queries must match all three; the built-in event dashboard already covers them.
