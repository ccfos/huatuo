---
title: Instant Observability
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

{{% alert color="info" title="🎯 About HUATUO" %}}
<div style="text-align: center;">
HUATUO is an operating system deep observability project open-sourced by DiDi and incubated under CCF (China Computer Federation). It focuses on providing OS kernel-level deep observability for cloud-native general computing, AI computing, cloud services, and infrastructure services.
</div>
{{% /alert %}}

## 📖 Overview

HUATUO uses eBPF technology to observe anomalous events in real time across core Linux kernel subsystems, including CPU scheduling, memory management, the network protocol stack, and hardware error reporting. When the kernel encounters anomalies such as softlockup, OOM, or hardware MCE errors, eBPF programs hook into kernel functions (kprobes) or kernel tracepoints, capturing process information, kernel call stacks, and network context at the moment the event occurs. The data is passed to user-space handlers via the perf event ring buffer and persisted to Elasticsearch or local disk files.

Compared to traditional kernel log (dmesg/syslog) collection, eBPF-based event observation reduces the risk of data loss from log buffer overflow; it can capture transient anomalies that never appear in kernel logs (such as excessive scheduler tick intervals); and it provides container-level event correlation for precise root-cause analysis in cloud-native environments.

Twelve event types are continuously observed, covering CPU scheduling health (sched_tick, softlockup, hungtask), memory pressure (oom, memory_reclaim_events), the network protocol stack (dropwatch, tcp_retransmit, net_rx_latency, netdev_events, netdev_bonding_lacp, netdev_txqueue_timeout), and hardware reliability (ras).

## 🎯 Use Cases

**Kubernetes Container Memory Fault Diagnosis**: In scenarios where containers frequently restart due to OOM, the oom event records both the process killed by the OOM Killer (victim) and the process that triggered the OOM (trigger), including their memcg cgroup pointers and container IDs. Combined with time-series data, this enables fast root-cause analysis of containers involved in memory contention, reducing the time spent manually reviewing container logs.

**AI Training Cluster Hardware Fault Detection**: On GPU training servers, the ras event continuously collects MCE (Machine Check Exception), EDAC memory controller errors, and PCIe AER (Advanced Error Reporting) errors, classifying them by severity (Corrected / UncorrectedRecoverable / UncorrectedFatal). This enables early detection of hardware aging or single-point failures before training jobs are interrupted, reducing training task losses caused by hardware faults.

**Network Performance Jitter Analysis**: dropwatch observes packet drops in the kernel network stack, tcp_retransmit observes TCP retransmission activity, and net_rx_latency detects end-to-end receive-path latency for individual packets from the network card driver to user space. Separate thresholds are configured per stage (driver to kernel: 5ms, kernel to TCP: 10ms, TCP to user space: 115ms), precisely identifying which network layer causes business timeouts.

**Host Scheduling Health Observation**: The sched_tick (scheduler tick interval, default threshold 10ms), softlockup (CPU unable to schedule, ~1 second), and hungtask (D-state process hang) events jointly cover anomalies along the CPU scheduling path. When system stalls or response timeouts occur, kernel call stacks and other diagnostic data are automatically preserved, supporting offline analysis after the fault clears.

## 🚀 Usage

### Configuration

All events provide default values and are operational without any configuration. The following parameters can be tuned as needed:

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `sched_tick.interval_threshold` | `10000000` (10ms, nanoseconds) | Scheduler tick interval threshold |
| `memory_reclaim.blocked_threshold` | `900000000` (900ms, nanoseconds) | Direct memory reclaim time trigger threshold |
| `net_rx_latency.driver2net_rx` | `5` (ms) | Latency threshold from NIC driver to `__netif_receive_skb` |
| `net_rx_latency.driver2tcp` | `10` (ms) | Latency threshold from NIC driver to `tcp_v4_rcv` |
| `net_rx_latency.driver2userspace` | `115` (ms) | Latency threshold from NIC driver to user-space copy (`skb_copy_datagram_iovec`) |
| `net_rx_latency.excluded_host_netnamespace` | `true` | Whether to exclude the host network namespace (observe containers only by default) |
| `net_rx_latency.excluded_container_qos` | `[]` | List of container QoS levels to exclude |
| `dropwatch.filter` | `tcp` | tcpdump-style packet filter applied before dropwatch events are emitted |
| `dropwatch.max_events_per_second` | `100` | Maximum dropwatch events emitted per second; `0` disables rate limiting |
| `dropwatch.exclude_containers` | `[]` | Reserved field; the current dropwatch event path does not apply it |
| `netdev.device_list` | `[]` | List of network device names to monitor for link state changes |
| `ras.mce_thr_backoff` | `1800` (seconds) | MCE threshold interrupt (THR) event reporting cooldown to suppress interrupt storms |
| `issues_list` | `[]` | Known-issue suppression rules; matched against net_rx_latency titles and dropwatch kernel call stacks |

### Supported Events

| Event Name (tracer_name) | Probe Type | Trigger Condition | Typical Scenarios |
| ------------------------ | ---------- | ----------------- | ----------------- |
| `sched_tick` | kprobe | Scheduler tick interval >= threshold (default 10ms) | System stalls, network latency, scheduling delays |
| `softlockup` | kprobe | CPU unable to schedule for extended time (~1 second) | Soft lockup, response anomalies |
| `hungtask` | kprobe | D-state process task hang | Transient mass D-state processes, IO blocking |
| `oom` | kprobe | OOM Killer triggered | Container/host memory exhaustion |
| `memory_reclaim_events` | kprobe | Container process direct reclaim time > threshold (default 900ms) | Business stalls caused by memory pressure |
| `ras` | tracepoint | CPU/MEM/PCIe hardware errors | Hardware fault detection |
| `dropwatch` | tracepoint | Kernel network stack packet drop | Business jitter caused by protocol stack drops |
| `tcp_retransmit` | tracepoint; optional kprobe for TLP | TCP retransmission or Tail Loss Probe | TCP loss, reordering, congestion, and latency diagnosis |
| `net_rx_latency` | kprobe | Protocol stack receive latency exceeds per-stage threshold | Business timeouts caused by receive latency |
| `netdev_events` | netlink | NIC link state change | Physical NIC link failures |
| `netdev_bonding_lacp` | kprobe | LACP protocol state change (IEEE 802.3ad mode only) | Fault boundary between physical machines and switches |
| `netdev_txqueue_timeout` | kprobe | NIC transmit queue timeout | NIC transmit queue hardware failure |

For tcp_retransmit usage, fields, classification, and drop correlation, refer to the [tcpshark documentation](/docs/best-practice/tcpshark_en.md).

### Fields

All event records include the following common fields:

- **hostname**: Physical machine hostname
- **region**: Availability zone where the physical machine is located
- **uploaded_time**: Data upload time
- **container_id**: Container ID if the event is associated with a container
- **container_hostname**: Container hostname if the event is associated with a container
- **container_host_namespace**: Kubernetes namespace of the container if the event is associated with a container
- **container_type**: Container type, e.g., `normal` for regular containers, `sidecar` for sidecar containers
- **container_qos**: Container QoS level
- **tracer_name**: Event name (e.g., `sched_tick`, `oom`)
- **tracer_id**: Tracing ID for this event
- **tracer_time**: Time when the tracing was triggered
- **tracer_type**: Trigger type — manual or automatic
- **tracer_data**: Event-specific private data (see individual event descriptions below)

### 1. sched_tick

**Description** Measures the interval between scheduler ticks. When the interval reaches the threshold, it records the current kernel call stack and process information. The event can reveal long IRQ-off sections, CPU stalls, or virtualization scheduling delays, but does not by itself prove that softirqs were disabled. `comm` and `pid` identify the task interrupted by the reporting tick; they do not identify the cause of the delay.

**Applicable Scenarios**

- The kernel or a driver disables local interrupts for too long, or hardirq/NMI processing monopolizes the CPU.
- A VM vCPU is descheduled by the host, including high steal time and scheduling stalls.
- Low-level anomalies such as SMI or firmware stalls, delayed clockevent delivery, or lost timer events.

**Usage Boundaries** Normal tick suppression after a successful NO_HZ transition is excluded. Ordinary CPU load or a softirq backlog alone does not imply tick delay. Set the threshold above the target system's normal tick period. The captured stack represents the first tick after the delay and should be correlated with IRQ, steal-time, and hardware metrics.

**Data Storage** Event data is automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "uploaded_time": "2025-06-11T16:05:16.251152703+08:00",
    "hostname": "***",
    "tracer_data": {
        "tick_interval_ns": 237328905,
        "tick_interval_threshold_ns": 10000000,
        "comm": "***-agent",
        "pid": 688073,
        "cpu": 1,
        "stack": "scheduler_tick/..."
    },
    "tracer_time": "2025-06-11 16:05:16.251 +0800",
    "tracer_type": "auto",
    "time": "2025-06-11 16:05:16.251 +0800",
    "region": "***",
    "tracer_name": "sched_tick"
}
```

**Fields**

- **comm**: Name of the process that triggered the event
- **stack**: Kernel call stack captured on the first scheduler tick after the delay
- **tick_interval_ns**: Total interval between adjacent scheduler ticks (nanoseconds)
- **cpu**: CPU number where the event occurred
- **tick_interval_threshold_ns**: Inclusive scheduler tick interval threshold (nanoseconds)
- **pid**: Process ID of the task interrupted by the reporting tick

### 2. dropwatch

**Description** Detects packet drop behavior in the kernel network protocol stack. Outputs the kernel call stack, network 5-tuple, and TCP state at the time of the drop. Optional call-stack filters can suppress locally validated noise patterns. The `type` field is reserved for TCP drop-type compatibility and is currently unset.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "tracer_data": {
        "observed_timestamp": "2026-07-23T02:14:40.304775546Z",
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
                "flags": "ACK",
                "sk_state": "ESTABLISHED"
            }
        },
        "stack": "kfree_skb/..."
    }
}
```

**Fields**

- **type**: Reserved drop type, currently unset and omitted from JSON; reserved codes are `1` (common drop), `2` (SYN flood), `3` (SYN queue overflow), and `4` (accept queue overflow)
- **drop_reason**: Kernel packet-drop reason
- **source**: Event source (`tools` for standalone dropwatch or `events` when launched by huatuo-bamai)
- **comm**: Name of the process that triggered the packet drop
- **pid**: Process ID
- **net_namespace_cookie / net_namespace_inum**: Network namespace values used for container resolution
- **netdev_queue_mapping**: NIC queue index
- **netdev_linkstatus**: List of NIC link status flags
- **netdev_name**: Network device name
- **netdev_ifindex**: Network interface index
- **packet_len_bytes**: Packet length (bytes)
- **layers.ipv4.saddr / layers.ipv4.daddr**: Source and destination IP addresses
- **layers.tcp.sport / layers.tcp.dport**: Source and destination ports
- **layers.tcp.seq / layers.tcp.ack_seq**: TCP sequence and acknowledgment sequence numbers
- **layers.tcp.sk_state**: TCP connection state at the time of the drop
- **stack**: Kernel call stack at the time of the drop

### 3. net_rx_latency

**Description** Detects latency events on the protocol stack receive path (NIC driver → kernel protocol stack → user-space receive). Three observation points are set along the receive path; when the latency of any stage exceeds the corresponding threshold (defaults: driver to kernel 5ms, kernel to TCP 10ms, TCP to user space 115ms), the event is recorded with the network 5-tuple, TCP sequence number, latency stage, and latency duration. All TCP states are observed, not limited to ESTABLISHED—receive latency events in SYN, FIN, TIME_WAIT, and other non-ESTABLISHED states are also captured. The host network namespace is excluded by default, observing only container network traffic.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
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

**Fields**

- **comm**: Name of the process that triggered the event
- **pid**: Process ID that triggered the event
- **latency_stage**: Stage where latency occurred (`RX_STAGE_NETIF` driver-to-kernel / `RX_STAGE_TCPV4` kernel-to-TCP / `RX_STAGE_USERCOPY` TCP-to-user-space)
- **latency_ms**: Actual latency (milliseconds)
- **latency_threshold_ms**: Latency threshold that triggered the event (milliseconds)
- **tcp_state**: TCP connection state (all states are supported, e.g., `ESTABLISHED`, `SYN_SENT`, `FIN_WAIT`, `TIME_WAIT`)
- **tcp_saddr / tcp_daddr**: Source IP / Destination IP address
- **tcp_sport / tcp_dport**: Source port / Destination port
- **tcp_seq / tcp_ack_seq**: TCP sequence number / Acknowledgment sequence number
- **net_namespace_cookie**: Network namespace cookie (available on kernel ≥ 5.14, used for efficient container association)
- **net_namespace_inum**: Network namespace inum
- **packet_len_bytes**: Packet length (bytes)

### 4. oom

**Description** Detects OOM (Out of Memory) events on the host or inside containers. Records information about the process killed by the OOM Killer (victim) and the process that triggered the OOM (trigger), along with the corresponding container and memory cgroup details, providing a complete fault snapshot. Host-level and per-container OOM count metrics are also maintained.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "tracer_data": {
        "trigger_memcg_css": "0xff4b8d8be3818000",
        "trigger_container_id": "***",
        "trigger_container_hostname": "***.docker",
        "trigger_pid": 3218804,
        "trigger_process_name": "java",
        "victim_memcg_css": "0xff4b8d8be3818000",
        "victim_container_id": "***",
        "victim_container_hostname": "***.docker",
        "victim_pid": 3218745,
        "victim_process_name": "java",
        "cgroup_memory_limit": 2147483648,
        "cgroup_memory_usage": 2143289344,
        "memory_snapshot": {
            "top_processes": [
                {
                    "pid": 3218745,
                    "process_name": "java",
                    "vm_rss": 1604321280,
                    "rss_anon": 1509949440,
                    "rss_file": 83886080,
                    "rss_shmem": 0,
                    "vm_swap": 0,
                    "total": 1593835520
                }
            ],
            "host_meminfo": {
                "MemAvailable": 3355443200,
                "Cached": 1073741824,
                "Slab": 268435456
            },
            "victim_cgroup": {
                "container_id": "***",
                "cgroup_path": "kubepods.slice/...",
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
        }
    }
}
```

**Fields**

- **victim_process_name / victim_pid**: Name and PID of the process killed by the OOM Killer
- **victim_container_hostname / victim_container_id**: Hostname and container ID where the killed process resided
- **victim_memcg_css**: Memory cgroup pointer (hex) of the killed process
- **trigger_process_name / trigger_pid**: Name and PID of the process that triggered OOM
- **trigger_container_hostname / trigger_container_id**: Hostname and container ID where the triggering process resided
- **trigger_memcg_css**: Memory cgroup pointer (hex) of the triggering process
- **cgroup_memory_limit / cgroup_memory_usage**: Memory limit and usage reported by the kernel event
- **memory_snapshot.top_processes**: Top processes by RSS/swap at the OOM moment, including `RssAnon`, `RssFile`, `RssShmem`, `VmRSS`, and `VmSwap`
- **memory_snapshot.host_meminfo**: Key host `/proc/meminfo` values, such as `MemAvailable`, `Cached`, `Slab`, swap, and anon/file activity
- **memory_snapshot.trigger_cgroup / victim_cgroup**: Trigger/victim container cgroup path, current/max memory, `memory.stat`, and `memory.events`

### 5. softlockup

**Description** Detects softlockup events (CPU unable to be scheduled for an extended period, approximately 1 second). Provides information about the target process causing the lockup, the CPU where it occurred, and NMI backtrace information for all CPUs. A backoff strategy is applied: the reporting interval increases from 10 minutes up to a maximum of 3 hours during an event storm to prevent duplicate reports. A softlockup occurrence counter metric is also maintained.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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

**Fields**

- **cpu**: CPU number where the softlockup occurred
- **pid**: PID of the process that triggered the softlockup
- **comm**: Name of the process that triggered the softlockup
- **cpus_stack**: NMI backtrace for all CPUs (multi-line text containing timestamps and call stacks)

### 6. hungtask

**Description** Detects hungtask events. Captures the kernel stacks of all processes in D state (uninterruptible sleep) and NMI backtrace for all CPUs to preserve the fault scene. A backoff strategy is applied: the reporting interval increases from 10 minutes up to a maximum of 3 hours during an event storm. A hungtask occurrence counter metric is also maintained. Note: some Linux distributions (e.g., Fedora 42) disable hungtask detection by default, in which case this observer will not start.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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

**Fields**

- **tid**: TID of the task that triggered hungtask detection
- **comm**: Name of the task that triggered hungtask detection
- **cpus_stack**: NMI backtrace for all CPUs (multi-line text containing timestamps and call stacks)
- **blocked_processes_stack**: Kernel stack information of D-state processes

### 7. memory_reclaim_events

**Description** Detects direct memory reclaim events for container processes. Triggered when the direct reclaim time of the same process within 1 second exceeds the configured threshold (default 900ms). Records the reclaim duration, process, and container information. **Note: this observer only records events for container processes; host process events are filtered out.**

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "tracer_data": {
        "pid": 1896137,
        "tid": 1896138,
        "comm": "java",
        "reclaim_duration_ns": 1412702917
    }
}
```

**Fields**

- **comm**: Name of the process that triggered direct memory reclaim
- **pid**: PID of the triggering process
- **tid**: TID of the triggering thread
- **reclaim_duration_ns**: Direct reclaim duration (nanoseconds)

### 8. ras

**Description** Detects hardware errors from CPU, memory, and PCIe subsystems via kernel tracepoints. Supports five hardware error sources: MCE (Machine Check Exception), EDAC (memory controller), ACPI/GHES (non-standard hardware errors), PCIe AER (Advanced Error Reporting), and MCE threshold interrupts (THR). Errors are classified by severity: `Corrected`, `UncorrectedRecoverable`, `UncorrectedDeferred`, and `UncorrectedFatal`. MCE threshold interrupt events use a cooldown period (default 30 minutes) to suppress interrupt storm-driven duplicate reports.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**MCE Sample Data**

```json
{
    "tracer_data": {
        "dev": "CPU/MEM",
        "event": "MCE",
        "type": "UncorrectedRecoverable",
        "observed_timestamp": "2025-06-11T00:00:00Z",
        "info": "{\"mcg_cpu_cap\":4096,\"banks_msr_status\":9295429630892703744,\"cpu\":2,\"socketid\":0,\"bank\":5}"
    }
}
```

**PCIe AER Sample Data**

```json
{
    "tracer_data": {
        "dev": "PCIe 0000:3b:00.0",
        "event": "AER",
        "type": "UncorrectedRecoverable",
        "observed_timestamp": "2025-06-11T00:00:00Z",
        "info": "{\"dev_name\":\"0000:3b:00.0\",\"err_type\":\"UncorrectedRecoverable\",\"err_reason\":\"Completion Timeout\",\"tlp_header\":\"not available\"}"
    }
}
```

**Fields**

- **dev**: Hardware device where the error occurred (e.g., `CPU/MEM`, `PCIe 0000:3b:00.0`)
- **event**: Error type (`MCE` / `EDAC` / `NON_STANDARD` / `AER` / `MCE_THRESHOLD`)
- **type**: Error severity (`Corrected` / `UncorrectedRecoverable` / `UncorrectedDeferred` / `UncorrectedFatal` / `Info`)
- **observed_timestamp**: UTC time when the hardware error occurred
- **info**: JSON-formatted detailed error information; content varies by event type

### 9. netdev_events

**Description** Detects NIC link state change events by subscribing to kernel netlink RTM_NEWLINK messages. Captures events including down/up transitions, MTU changes, AdminDown, and CarrierDown, along with interface name, link status, MAC address, and driver information. At startup, the observer scans the current state of all devices in `device_list` as a baseline; only state changes are reported thereafter.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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

**Fields**

- **ifname**: Network interface name (e.g., `eth1`)
- **index**: Interface index number
- **linkstatus**: Link state change description (may contain multiple states)
- **mac**: NIC MAC address
- **start**: Whether this is a baseline event scanned at startup (`true`: startup scan, `false`: real-time change event)
- **driver**: NIC driver name
- **driver_version**: NIC driver version
- **firmware_version**: NIC firmware version

### 10. netdev_bonding_lacp

**Description** Detects LACP (Link Aggregation Control Protocol, IEEE 802.3ad) protocol state changes in bonding mode. Reads and records the complete status of all bonding interfaces under `/proc/net/bonding/`, including mode, MII status, Actor/Partner negotiation parameters, and slave link states. **This observer is only activated automatically when an IEEE 802.3ad bonding interface is present on the system.**

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "tracer_data": {
        "content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0...\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nMII Status: down\n..."
    }
}
```

**Fields**

- **content**: Complete bonding interface status information (multi-line text containing LACP negotiation details for all slaves, equivalent to the `/proc/net/bonding/bondX` file content)

### 11. netdev_txqueue_timeout

**Description** Detects NIC transmit queue timeout (TX queue timeout) events. Records the queue index, device name, and driver name where the timeout occurred, used to identify hardware failures on the NIC transmit path.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
{
    "tracer_data": {
        "queue_index": 3,
        "device_name": "eth0",
        "driver_name": "ixgbe"
    }
}
```

**Fields**

- **queue_index**: Index of the transmit queue where the timeout occurred
- **device_name**: Network device name
- **driver_name**: NIC driver name

## ⚙️ How It Works

### Architecture

HUATUO's anomalous event observation is built on eBPF technology. Event data is collected in kernel space with minimal performance overhead, and processed by user-space daemons for formatting, filtering, container association, and persistent storage.

```mermaid
graph TB
    subgraph "Linux Kernel"
        direction TB
        K1["kprobe hooks\n(sched_tick / softlockup / hungtask\n oom / memory_reclaim_events\n net_rx_latency / netdev_txqueue_timeout\n tcp_retransmit TLP, optional)"]
        K2["tracepoint hooks\n(ras: MCE / EDAC / AER / ACPI\n dropwatch: skb/kfree_skb\n tcp_retransmit:\n tcp/tcp_retransmit_skb /\n tcp/tcp_retransmit_synack)"]
        K3["netlink subscription\n(netdev_events: RTM_NEWLINK)"]
        K4["kprobe hooks\n(netdev_bonding_lacp: 802.3ad)"]
        PEB["Perf Event Ring Buffer\n(8192 pages)"]
    end

    subgraph "HUATUO User Space"
        direction TB
        EH["Go event handler goroutines\n(one per event type)"]
        CF["Filters\n(threshold / noise reduction / known-issue filtering)"]
        CM["Container association\n(CSS → ContainerID\n NetNS → ContainerID)"]
    end

    subgraph "Storage"
        ES["Elasticsearch"]
        DISK["Local disk files"]
    end

    K1 --> PEB
    K2 --> PEB
    K4 --> PEB
    PEB --> EH
    K3 --> EH
    EH --> CF
    CF --> CM
    CM --> ES
    CM --> DISK
```

### Event Processing Flow

```mermaid
sequenceDiagram
    participant K as Linux Kernel
    participant B as eBPF Program
    participant P as Perf Event Buffer
    participant H as Go Event Handler
    participant F as Filter
    participant S as Storage

    K->>B: kprobe / tracepoint fires
    B->>B: Collect event context<br/>(process info / kernel stack / network context)
    B->>P: Write to perf event ring buffer
    H->>P: Read event data (blocking)
    H->>F: Format and apply filters<br/>(threshold / noise / known issues)
    F->>H: Events that passed filtering
    H->>H: Associate container information<br/>(CSS / NetNS mapping)
    H->>S: Persist to storage<br/>(Elasticsearch / local files)
```

{{% alert color="info" %}}
<div style="text-align: center;">
🌟 Star us: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
<br><br>
👀 Follow our official WeChat account<br>
<img src="/img/contact-weixin.png" alt="WeChat QR code" style="max-width: 200px; margin-top: 10px;">
</div>
{{% /alert %}}
