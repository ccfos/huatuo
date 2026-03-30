---
title: Events
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

The HUATUO platform uses eBPF technology to detect various abnormal events in the Linux kernel in real time, helping users quickly locate issues related to the system, applications, and hardware.

## Supported Events

| Event Name             | Core Function                                                | Typical Scenarios                                            |
| ---------------------- | ------------------------------------------------------------ | ------------------------------------------------------------ |
| softirq                | Detects excessively long softirq disable time in the kernel, outputs call stack and process information | Resolves system stalls, network latency, and scheduling delays |
| softlockup             | Detects softlockup events and provides target process and kernel stack information | Locates and resolves system softlockup issues                |
| hungtask               | Detects hungtask events, outputs all D-state processes and their stack information | Captures transient mass D-state process scenarios and preserves fault scenes |
| oom                    | Detects OOM events in the host or containers                 | Focuses on memory exhaustion issues and provides detailed fault snapshots |
| memory_reclaim_events  | Detects direct memory reclaim events, records reclaim duration, process and container information | Resolves business stalls caused by memory pressure           |
| ras                    | Detects hardware faults in CPU, Memory, PCIe, etc.           | Timely awareness of hardware failures to reduce business impact |
| dropwatch              | Detects packet drops in the kernel network protocol stack, outputs call stack and network context | Resolves business jitters and latency caused by protocol stack packet drops |
| net_rx_latency         | Detects latency events in the protocol stack receive path (driver → protocol → user space) | Resolves business timeouts and jitters caused by receive latency |
| netdev_events          | Detects network device link status changes                   | Detects physical link failures on network cards              |
| netdev_bonding_lacp    | Detects bonding LACP protocol status changes                 | Identifies fault boundaries between physical machines and switches |
| netdev_txqueue_timeout | Detects network card transmit queue timeout events           | Locates hardware failures in network card transmit queues    |

## Event Details

### Common Fields

- **hostname**: Physical machine hostname
- **region**: Availability zone where the physical machine is located
- **uploaded_time**: Data upload time
- **container_id**: Container ID if the event is associated with a container
- **container_hostname**: Container hostname if the event is associated with a container
- **container_host_namespace**: Kubernetes namespace of the container if the event is associated with a container
- **container_type**: Container type, e.g., `normal` for regular containers, `sidecar` for sidecar containers, etc.
- **container_qos**: Container QoS level
- **tracer_name**: Event name
- **tracer_id**: Tracing ID for this event
- **tracer_time**: Time when tracing was triggered
- **tracer_type**: Trigger type — manual or automatic
- **tracer_data**: Tracer-specific private data

### 1. softirq

**Description**  
Detects when the kernel disables interrupts for too long. Records the kernel call stack during the disable period, current process information, and other key data to help analyze interrupt-related latency issues.

**Data Storage**  
Event data is automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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

**Fields**

- **comm**: Name of the process that triggered the event
- **pid**: Process ID that triggered the event
- **saddr / daddr**: Source IP / Destination IP
- **sport / dport**: Source port / Destination port
- **seq / ack_seq**: TCP sequence number / Acknowledgment sequence number
- **state**: TCP connection state (e.g., ESTABLISHED)
- **pkt_len**: Packet length (bytes)
- **where**: Location where the latency occurred (e.g., TO_USER_COPY indicates user-space copy stage)
- **latency_ms**: Actual latency (milliseconds)

### 2. dropwatch

**Description** Detects packet drop behavior in the kernel network protocol stack. Outputs the call stack and network address information at the time of the drop to help troubleshoot business anomalies caused by network packet loss.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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

**Fields**

- **comm**: Name of the process that triggered the packet drop
- **stack**: Kernel call stack at the time of the drop
- **saddr**: Source IP address
- **pid**: Process ID
- **type**: Drop type (e.g., common_drop)
- **queue_mapping**: Network card queue mapping information (specific values depend on the actual drop scenario)

### 3. net_rx_latency

**Description** Detects latency events in the protocol stack receive path (network card driver → kernel protocol stack → user-space active receive). Triggers when the overall latency of a single packet from the network card to user-space reception exceeds the threshold (default 90 seconds). Records detailed network context information (such as 5-tuple, TCP sequence number, latency location, etc.) to help diagnose business timeouts and jitters caused by protocol stack or application receive delays.

**Typical Scenarios** Resolves network performance issues caused by protocol stack receive latency or slow application response.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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
}
```

**Fields**

- **comm**: Name of the process that triggered the event
- **pid**: Process ID that triggered the event
- **saddr / daddr**: Source IP / Destination IP
- **sport / dport**: Source port / Destination port
- **seq / ack_seq**: TCP sequence number / Acknowledgment sequence number
- **state**: TCP connection state (e.g., ESTABLISHED)
- **pkt_len**: Packet length (bytes)
- **where**: Location where the latency occurred (e.g., TO_USER_COPY indicates user-space copy stage)
- **latency_ms**: Actual latency (milliseconds)

### 4. oom

**Description** Detects OOM (Out of Memory) events occurring on the host or inside containers. Records information about the process killed by the OOM Killer (victim) and the process that triggered the OOM (trigger), along with corresponding container and memory cgroup details, providing a complete fault snapshot.

**Typical Scenarios** Focuses on memory exhaustion issues on physical machines or containers to quickly locate business failures caused by unavailable memory.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

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
}
```

**Fields**

- **victim_process_name / victim_pid**: Name and PID of the process killed by the OOM Killer
- **victim_container_hostname / victim_container_id**: Hostname and container ID where the killed process resides
- **victim_memcg_css**: Memory cgroup pointer (hex) of the killed process
- **trigger_process_name / trigger_pid**: Name and PID of the process that triggered OOM
- **trigger_container_hostname / trigger_container_id**: Hostname and container ID where the triggering process resides
- **trigger_memcg_css**: Memory cgroup pointer (hex) of the triggering process

### 5. softlockup

**Description** Detects softlockup events (CPU unable to schedule for a long time, default threshold approximately 1 second). Provides information about the target process causing the lockup, the CPU where it occurred, the kernel call stack of that CPU, and records the number of occurrences.

**Typical Scenarios** Resolves system freezes or response anomalies caused by softlockup.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

### 6. hungtask

**Description** Detects hungtask events, captures kernel stacks of all processes in D state (uninterruptible sleep), and records the total number of D-state processes and backtrace information for each CPU to preserve the fault scene.

**Typical Scenarios** Locates transient scenarios where a large number of D-state processes appear, facilitating subsequent problem tracking and analysis.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
"tracer_data": {
	"cpus_stack": "2025-06-10 09:57:14 sysrq: Show backtrace of all active CPUs\nNMI backtrace for cpu 33\n...",
	"pid": 2567042,
	"d_process_count": "...",
	"blocked_processes_stack": "..."
}
```

**Fields**

- **cpus_stack**: NMI backtrace information for all CPUs (multi-line text containing timestamps and stack content)
- **pid**: PID of the process that triggered the hungtask detection
- **d_process_count**: Total number of D-state processes in the current system
- **blocked_processes_stack**: Kernel stack information of D-state processes

### 7. memory_reclaim_events

**Description** Detects direct memory reclaim events. Triggers when the direct reclaim time of the same process exceeds the threshold (default approximately 900 ms) within 1 second. Records the reclaim duration, process, and container information.

**Typical Scenarios** Resolves business process stalls caused by excessive system memory pressure.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
"tracer_data": {
	"comm": "chrome",
	"pid": 1896137,
	"deltatime": 1412702917
}
```

**Fields**

- **comm**: Name of the process that triggered memory reclaim
- **pid**: PID of the process that triggered reclaim
- **deltatime**: Direct reclaim duration (nanoseconds)

### 8. netdev_events

**Description** Detects network card link status change events (including down/up, MTU changes, AdminDown, CarrierDown, etc.). Outputs interface name, status description, MAC address, and other information.

**Typical Scenarios** Timely detection of physical link issues on network cards to resolve business unavailability caused by network card failures.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data**

```json
"tracer_data": {
	"ifname": "eth1",
	"linkstatus": "linkStatusAdminDown, linkStatusCarrierDown",
	"mac": "5c:6f:69:34:dc:72",
	"index": 3,
	"start": false
}
```

**Fields**

- **ifname**: Network interface name (e.g., eth1)
- **linkstatus**: Detailed link status description
- **mac**: Network card MAC address
- **index**: Interface index
- **start**: Whether the interface is in start state (true/false)

### 9. netdev_bonding_lacp

**Description** Detects status changes of the LACP (Link Aggregation Control Protocol) in bonding mode. Records detailed bonding configuration information, including mode, MII status, Actor/Partner information, slave link status, etc. (outputs the complete content of /proc/net/bonding/bondX).

**Typical Scenarios** Identifies faults on the physical machine or switch side in bonding mode and resolves LACP negotiation jitter issues.

**Data Storage** Automatically stored in Elasticsearch or as files on the physical machine disk.

**Sample Data** (the content field contains the full text)

```json
"tracer_data": {
	"content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0...\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nMII Status: down\n..."
}
```

**Fields**

- **content**: Complete bonding interface status information (multi-line text containing LACP negotiation details for all slaves)
