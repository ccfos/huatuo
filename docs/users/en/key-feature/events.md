---
title: Events
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---


HUATUO currently supports the following exception context capture events:

| Event Name        | Core Functionality               | Scenarios                                    |
| ------------------| -------------------------------- |----------------------------------------------|
| softirq           | Detects delayed response or prolonged disabling of host soft interrupts, and outputs kernel call stacks and process information when soft interrupts are disabled for extended periods., etc. | This type of issue severely impacts network transmission/reception, leading to business spikes or timeout issues |
| dropwatch         | Detects TCP packet loss and outputs host and network context information when packet loss occurs | This type of issue mainly causes business spikes and latency |
| net_rx_latency        | Captures latency events in network receive path from driver, protocol stack, to user-space receive process | For network latency issues in the receive direction where the exact delay location is unclear, net_rx_latency calculates latency at the driver, protocol stack, and user copy paths using skb NIC ingress timestamps, filters timeout packets via preset thresholds, and locates the delay position |
| oom               | Detects OOM events on the host or within containers | When OOM occurs at host level or container dimension, captures process information triggering OOM, killed process information, and container details to troubleshoot memory leaks, abnormal exits, etc. |
| softlockup        | When a softlockup occurs on the system, collects target process information and CPU details, and retrieves kernel stack information from all CPUs | System softlockup events |
| hungtask          | Provides count of all D-state processes in the system and kernel stack information | Used to locate transient D-state process scenarios, preserving the scene for later problem tracking |
| memreclaim        | Records process information when memory reclamation exceeds time threshold | When memory pressure is excessively high, if a process requests memory at this time, it may enter direct reclamation (synchronous phase), potentially causing business process stalls. Recording the direct reclamation entry time helps assess the severity of impact on the process |
| netdev            | Detects network device status changes | Network card flapping, slave abnormalities in bond environments, etc. |
| lacp              | Detects LACP status changes | Detects LACP negotiation status in bond mode 4 |


### Detect the long-term disabling of soft interrupts

**Feature Introduction**

The Linux kernel contains various contexts such as process context, interrupt context, soft interrupt context, and NMI context. These contexts may share data, so to ensure data consistency and correctness, kernel code might disable soft or hard interrupts. Theoretically, the duration of single interrupt or soft interrupt disabling shouldn't be too long. However, high-frequency system calls entering kernel mode and frequently executing interrupt disabling can also create a "long-term disable" phenomenon, slowing down system response. Issues related to "long interrupt or soft interrupt disabling" are very subtle with limited troubleshooting methods, yet have significant impact, typically manifesting as receive data timeouts in business applications. For this scenario, we built BPF-based detection capabilities for long hardware and software interrupt disables.

**Example**

Below is an example of captured  instances with overly long disabling interrupts, automatically uploaded to ES:

```json
{
  "_index": "***_2025-06-11",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-06-11T16:05:16.251152703+08:00",
    "hostname": "***",
    "tracer_data": {
      "comm": "observe-agent",
      "stack": "stack:\nscheduler_tick/ffffffffa471dbc0 [kernel]\nupdate_process_times/ffffffffa4789240 [kernel]\ntick_sched_handle.isra.8/ffffffffa479afa0 [kernel]\ntick_sched_timer/ffffffffa479b000 [kernel]\n__hrtimer_run_queues/ffffffffa4789b60 [kernel]\nhrtimer_interrupt/ffffffffa478a610 [kernel]\n__sysvec_apic_timer_interrupt/ffffffffa4661a60 [kernel]\nasm_call_sysvec_on_stack/ffffffffa5201130 [kernel]\nsysvec_apic_timer_interrupt/ffffffffa5090500 [kernel]\nasm_sysvec_apic_timer_interrupt/ffffffffa5200d30 [kernel]\ndump_stack/ffffffffa506335e [kernel]\ndump_header/ffffffffa5058eb0 [kernel]\noom_kill_process.cold.9/ffffffffa505921a [kernel]\nout_of_memory/ffffffffa48a1740 [kernel]\nmem_cgroup_out_of_memory/ffffffffa495ff70 [kernel]\ntry_charge/ffffffffa4964ff0 [kernel]\nmem_cgroup_charge/ffffffffa4968de0 [kernel]\n__add_to_page_cache_locked/ffffffffa4895c30 [kernel]\nadd_to_page_cache_lru/ffffffffa48961a0 [kernel]\npagecache_get_page/ffffffffa4897ad0 [kernel]\ngrab_cache_page_write_begin/ffffffffa4899d00 [kernel]\niomap_write_begin/ffffffffa49fddc0 [kernel]\niomap_write_actor/ffffffffa49fe980 [kernel]\niomap_apply/ffffffffa49fbd20 [kernel]\niomap_file_buffered_write/ffffffffa49fc040 [kernel]\nxfs_file_buffered_aio_write/ffffffffc0f3bed0 [xfs]\nnew_sync_write/ffffffffa497ffb0 [kernel]\nvfs_write/ffffffffa4982520 [kernel]\nksys_write/ffffffffa4982880 [kernel]\ndo_syscall_64/ffffffffa508d190 [kernel]\nentry_SYSCALL_64_after_hwframe/ffffffffa5200078 [kernel]",
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
    "tracer_name": "softirq",
    "es_index_time": 1749629116268
  },
  "fields": {
    "time": [
      "2025-06-11T08:05:16.251Z"
    ]
  },
  "_ignored": [
    "tracer_data.stack"
  ],
  "_version": 1,
  "sort": [
    1749629116251
  ]
}
```

The local host also stores identical data:

```json
2025-06-11 16:05:16 *** Region=***
{
  "hostname": "***",
  "region": "***",
  "uploaded_time": "2025-06-11T16:05:16.251152703+08:00",
  "time": "2025-06-11 16:05:16.251 +0800",
  "tracer_name": "softirq",
  "tracer_time": "2025-06-11 16:05:16.251 +0800",
  "tracer_type": "auto",
  "tracer_data": {
    "offtime": 237328905,
    "threshold": 100000000,
    "comm": "observe-agent",
    "pid": 688073,
    "cpu": 1,
    "now": 5532940660025295,
    "stack": "stack:\nscheduler_tick/ffffffffa471dbc0 [kernel]\nupdate_process_times/ffffffffa4789240 [kernel]\ntick_sched_handle.isra.8/ffffffffa479afa0 [kernel]\ntick_sched_timer/ffffffffa479b000 [kernel]\n__hrtimer_run_queues/ffffffffa4789b60 [kernel]\nhrtimer_interrupt/ffffffffa478a610 [kernel]\n__sysvec_apic_timer_interrupt/ffffffffa4661a60 [kernel]\nasm_call_sysvec_on_stack/ffffffffa5201130 [kernel]\nsysvec_apic_timer_interrupt/ffffffffa5090500 [kernel]\nasm_sysvec_apic_timer_interrupt/ffffffffa5200d30 [kernel]\ndump_stack/ffffffffa506335e [kernel]\ndump_header/ffffffffa5058eb0 [kernel]\noom_kill_process.cold.9/ffffffffa505921a [kernel]\nout_of_memory/ffffffffa48a1740 [kernel]\nmem_cgroup_out_of_memory/ffffffffa495ff70 [kernel]\ntry_charge/ffffffffa4964ff0 [kernel]\nmem_cgroup_charge/ffffffffa4968de0 [kernel]\n__add_to_page_cache_locked/ffffffffa4895c30 [kernel]\nadd_to_page_cache_lru/ffffffffa48961a0 [kernel]\npagecache_get_page/ffffffffa4897ad0 [kernel]\ngrab_cache_page_write_begin/ffffffffa4899d00 [kernel]\niomap_write_begin/ffffffffa49fddc0 [kernel]\niomap_write_actor/ffffffffa49fe980 [kernel]\niomap_apply/ffffffffa49fbd20 [kernel]\niomap_file_buffered_write/ffffffffa49fc040 [kernel]\nxfs_file_buffered_aio_write/ffffffffc0f3bed0 [xfs]\nnew_sync_write/ffffffffa497ffb0 [kernel]\nvfs_write/ffffffffa4982520 [kernel]\nksys_write/ffffffffa4982880 [kernel]\ndo_syscall_64/ffffffffa508d190 [kernel]\nentry_SYSCALL_64_after_hwframe/ffffffffa5200078 [kernel]"
  }
}
```

### Protocol Stack Packet Loss Detection

**Feature Introduction**

During packet transmission and reception, packets may be lost due to various reasons, potentially causing business request delays or even timeouts. dropwatch uses eBPF to observe kernel network packet discards, outputting packet loss network context such as source/destination addresses, source/destination ports, seq, seqack, pid, comm, stack information, etc. dropwatch mainly detects TCP protocol-related packet loss, using pre-set probes to filter packets and determine packet loss locations for root cause analysis.

**Example**

Information captured by dropwatch is automatically uploaded to ES. Below is an example where kubelet failed to send data packet due to device packet loss:

```json
{
  "_index": "***_2025-06-11",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-06-11T16:58:15.100223795+08:00",
    "hostname": "***",
    "tracer_data": {
      "comm": "kubelet",
      "stack": "kfree_skb/ffffffff9a0cd5c0 [kernel]\nkfree_skb/ffffffff9a0cd5c0 [kernel]\nkfree_skb_list/ffffffff9a0cd670 [kernel]\n__dev_queue_xmit/ffffffff9a0ea020 [kernel]\nip_finish_output2/ffffffff9a18a720 [kernel]\n__ip_queue_xmit/ffffffff9a18d280 [kernel]\n__tcp_transmit_skb/ffffffff9a1ad890 [kernel]\ntcp_connect/ffffffff9a1ae610 [kernel]\ntcp_v4_connect/ffffffff9a1b3450 [kernel]\n__inet_stream_connect/ffffffff9a1d25f0 [kernel]\ninet_stream_connect/ffffffff9a1d2860 [kernel]\n__sys_connect/ffffffff9a0c1170 [kernel]\n__x64_sys_connect/ffffffff9a0c1240 [kernel]\ndo_syscall_64/ffffffff9a2ea9f0 [kernel]\nentry_SYSCALL_64_after_hwframe/ffffffff9a400078 [kernel]",
      "saddr": "10.79.68.62",
      "pid": 1687046,
      "type": "common_drop",
      "queue_mapping": 11,
      "dport": 2052,
      "pkt_len": 74,
      "ack_seq": 0,
      "daddr": "10.179.142.26",
      "state": "SYN_SENT",
      "src_hostname": "***",
      "sport": 15402,
      "dest_hostname": "***",
      "seq": 1902752773,
      "max_ack_backlog": 0
    },
    "tracer_time": "2025-06-11 16:58:15.099 +0800",
    "tracer_type": "auto",
    "time": "2025-06-11 16:58:15.099 +0800",
    "region": "***",
    "tracer_name": "dropwatch",
    "es_index_time": 1749632295120
  },
  "fields": {
    "time": [
      "2025-06-11T08:58:15.099Z"
    ]
  },
  "_ignored": [
    "tracer_data.stack"
  ],
  "_version": 1,
  "sort": [
    1749632295099
  ]
}
```

The local host also stores identical data:

```json
2025-06-11 16:58:15 Host=*** Region=***
{
  "hostname": "***",
  "region": "***",
  "uploaded_time": "2025-06-11T16:58:15.100223795+08:00",
  "time": "2025-06-11 16:58:15.099 +0800",
  "tracer_name": "dropwatch",
  "tracer_time": "2025-06-11 16:58:15.099 +0800",
  "tracer_type": "auto",
  "tracer_data": {
    "type": "common_drop",
    "comm": "kubelet",
    "pid": 1687046,
    "saddr": "10.79.68.62",
    "daddr": "10.179.142.26",
    "sport": 15402,
    "dport": 2052,
    "src_hostname": ***",
    "dest_hostname": "***",
    "max_ack_backlog": 0,
    "seq": 1902752773,
    "ack_seq": 0,
    "queue_mapping": 11,
    "pkt_len": 74,
    "state": "SYN_SENT",
    "stack": "kfree_skb/ffffffff9a0cd5c0 [kernel]\nkfree_skb/ffffffff9a0cd5c0 [kernel]\nkfree_skb_list/ffffffff9a0cd670 [kernel]\n__dev_queue_xmit/ffffffff9a0ea020 [kernel]\nip_finish_output2/ffffffff9a18a720 [kernel]\n__ip_queue_xmit/ffffffff9a18d280 [kernel]\n__tcp_transmit_skb/ffffffff9a1ad890 [kernel]\ntcp_connect/ffffffff9a1ae610 [kernel]\ntcp_v4_connect/ffffffff9a1b3450 [kernel]\n__inet_stream_connect/ffffffff9a1d25f0 [kernel]\ninet_stream_connect/ffffffff9a1d2860 [kernel]\n__sys_connect/ffffffff9a0c1170 [kernel]\n__x64_sys_connect/ffffffff9a0c1240 [kernel]\ndo_syscall_64/ffffffff9a2ea9f0 [kernel]\nentry_SYSCALL_64_after_hwframe/ffffffff9a400078 [kernel]"
  }
}
```

### Protocol Stack Receive Latency

**Feature Introduction**

Online business network latency issues are difficult to locate, as problems can occur in any direction or stage. For example, receive direction latency might be caused by issues in drivers, protocol stack, or user programs. Therefore, we developed net_rx_latency detection functionality, leveraging skb NIC ingress timestamps to check latency at driver, protocol stack, and user-space layers. When receive latency reaches thresholds, eBPF captures network context information (five-tuple, latency location, process info, etc.). Receive path: **NIC -> Driver -> Protocol Stack -> User Active Receive**

**Example**

A business container received packets from the kernel with a latency over 90 seconds, tracked via net_rx_latency, ES query output:

```json
{
  "_index": "***_2025-06-11",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "tracer_data": {
      "dport": 49000,
      "pkt_len": 26064,
      "comm": "nginx",
      "ack_seq": 689410995,
      "saddr": "10.156.248.76",
      "pid": 2921092,
      "where": "TO_USER_COPY",
      "state": "ESTABLISHED",
      "daddr": "10.134.72.4",
      "sport": 9213,
      "seq": 1009085774,
      "latency_ms": 95973
    },
    "container_host_namespace": "***",
    "container_hostname": "***.docker",
    "es_index_time": 1749628496541,
    "uploaded_time": "2025-06-11T15:54:56.404864955+08:00",
    "hostname": "***",
    "container_type": "normal",
    "tracer_time": "2025-06-11 15:54:56.404 +0800",
    "time": "2025-06-11 15:54:56.404 +0800",
    "region": "***",
    "container_level": "1",
    "container_id": "***",
    "tracer_name": "net_rx_latency"
  },
  "fields": {
    "time": [
      "2025-06-11T07:54:56.404Z"
    ]
  },
  "_version": 1,
  "sort": [
    1749628496404
  ]
}
```

The local host also stores identical data:

```json
2025-06-11 15:54:46 Host=*** Region=*** ContainerHost=***.docker ContainerID=*** ContainerType=normal ContainerLevel=1
{
  "hostname": "***",
  "region": "***",
  "container_id": "***",
  "container_hostname": "***.docker",
  "container_host_namespace": "***",
  "container_type": "normal",
  "container_level": "1",
  "uploaded_time": "2025-06-11T15:54:46.129136232+08:00",
  "time": "2025-06-11 15:54:46.129 +0800",
  "tracer_time": "2025-06-11 15:54:46.129 +0800",
  "tracer_name": "net_rx_latency",
  "tracer_data": {
    "comm": "nginx",
    "pid": 2921092,
    "where": "TO_USER_COPY",
    "latency_ms": 95973,
    "state": "ESTABLISHED",
    "saddr": "10.156.248.76",
    "daddr": "10.134.72.4",
    "sport": 9213,
    "dport": 49000,
    "seq": 1009024958,
    "ack_seq": 689410995,
    "pkt_len": 20272
  }
}
```

### Host/Container Memory Overused

**Feature Introduction**

When programs request more memory than available system or process limits during runtime, it can cause system or application crashes. Common in memory leaks, big data processing, or insufficient resource configuration scenarios. By inserting BPF hooks in the OOM kernel flow, detailed OOM context information is captured and passed to user space, including process information, killed process information, and container details.

**Example**

When OOM occurs in a container, captured information:

```json
{
  "_index": "***_cases_2025-06-11",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-06-11T17:09:07.236482841+08:00",
    "hostname": "***",
    "tracer_data": {
      "victim_process_name": "java",
      "trigger_memcg_css": "0xff4b8d8be3818000",
      "victim_container_hostname": "***.docker",
      "victim_memcg_css": "0xff4b8d8be3818000",
      "trigger_process_name": "java",
      "victim_pid": 3218745,
      "trigger_pid": 3218804,
      "trigger_container_hostname": "***.docker",
      "victim_container_id": "***",
      "trigger_container_id": "***",
    "tracer_time": "2025-06-11 17:09:07.236 +0800",
    "tracer_type": "auto",
    "time": "2025-06-11 17:09:07.236 +0800",
    "region": "***",
    "tracer_name": "oom",
    "es_index_time": 1749632947258
  },
  "fields": {
    "time": [
      "2025-06-11T09:09:07.236Z"
    ]
  },
  "_version": 1,
  "sort": [
    1749632947236
  ]
}
```

Additionally, oom event implements `Collector` interface, which enables collecting statistics on host OOM occurrences via Prometheus, distinguishing between events from the host and containers.

### Kernel Softlockup

**Feature Introduction**

Softlockup is an abnormal state detected by the Linux kernel where a kernel thread (or process) on a CPU core occupies the CPU for a long time without scheduling, preventing the system from responding normally to other tasks. Causes include kernel code bugs, CPU overload, device driver issues, and others. When a softlockup occurs in the system, information about the target process and CPU is collected, kernel stack information from all CPUs is retrieved, and the number of occurrences of the issue is recorded.

### Process Blocking

**Feature Introduction**

A D-state process (also known as Uninterruptible Sleep) is a special process state indicating that the process is blocked while waiting for certain system resources and cannot be awakened by signals or external interrupts. Common scenarios include disk I/O operations, kernel blocking, hardware failures, etc. hungtask captures the kernel stacks of all D-state processes within the system and records the count of such processes. It is used to locate transient scenarios where D-state processes appear momentarily, enabling root cause analysis even after the scenario has resolved.

**Example**

```json
{
  "_index": "***_2025-06-10",
  "_type": "_doc",
  "_id": "8yyOV5cBGoYArUxjSdvr",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-06-10T09:57:12.202191192+08:00",
    "hostname": "***",
    "tracer_data": {
      "cpus_stack": "2025-06-10 09:57:14 sysrq: Show backtrace of all active CPUs\n2025-06-10 09:57:14 NMI backtrace for cpu 33\n2025-06-10 09:57:14 CPU: 33 PID: 768309 Comm: huatuo-bamai Kdump: loaded Tainted: G S      W  OEL    5.10.0-216.0.0.115.v1.0.x86_64 #1\n2025-06-10 09:57:14 Hardware name: Inspur SA5212M5/YZMB-00882-104, BIOS 4.1.12 11/27/2019\n2025-06-10 09:57:14 Call Trace:\n2025-06-10 09:57:14  dump_stack+0x57/0x6e\n2025-06-10 09:57:14  nmi_cpu_backtrace.cold.0+0x30/0x65\n2025-06-10 09:57:14  ? lapic_can_unplug_cpu+0x80/0x80\n2025-06-10 09:57:14  nmi_trigger_cpumask_backtrace+0xdf/0xf0\n2025-06-10 09:57:14  arch_trigger_cpumask_backtrace+0x15/0x20\n2025-06-10 09:57:14  sysrq_handle_showallcpus+0x14/0x90\n2025-06-10 09:57:14  __handle_sysrq.cold.8+0x77/0xe8\n2025-06-10 09:57:14  write_sysrq_trigger+0x3d/0x60\n2025-06-10 09:57:14  proc_reg_write+0x38/0x80\n2025-06-10 09:57:14  vfs_write+0xdb/0x250\n2025-06-10 09:57:14  ksys_write+0x59/0xd0\n2025-06-10 09:57:14  do_syscall_64+0x39/0x80\n2025-06-10 09:57:14  entry_SYSCALL_64_after_hwframe+0x62/0xc7\n2025-06-10 09:57:14 RIP: 0033:0x4088ae\n2025-06-10 09:57:14 Code: 48 83 ec 38 e8 13 00 00 00 48 83 c4 38 5d c3 cc cc cc cc cc cc cc cc cc cc cc cc cc 49 89 f2 48 89 fa 48 89 ce 48 89 df 0f 05 <48> 3d 01 f0 ff ff 76 15 48 f7 d8 48 89 c1 48 c7 c0 ff ff ff ff 48\n2025-06-10 09:57:14 RSP: 002b:000000c000adcc60 EFLAGS: 00000212 ORIG_RAX: 0000000000000001\n2025-06-10 09:57:14 RAX: ffffffffffffffda RBX: 0000000000000013 RCX: 00000000004088ae\n2025-06-10 09:57:14 RDX: 0000000000000001 RSI: 000000000274ab18 RDI: 0000000000000013\n2025-06-10 09:57:14 RBP: 000000c000adcca0 R08: 0000000000000000 R09: 0000000000000000\n2025-06-10 09:57:14 R10: 0000000000000000 R11: 0000000000000212 R12: 000000c000adcdc0\n2025-06-10 09:57:14 R13: 0000000000000002 R14: 000000c000caa540 R15: 0000000000000000\n2025-06-10 09:57:14 Sending NMI from CPU 33 to CPUs 0-32,34-95:\n2025-06-10 09:57:14 NMI backtrace for cpu 52 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 54 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 7 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 81 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 60 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 2 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 21 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 69 skipped: idling at intel_idle+0x6f/0xc0\n2025-06-10 09:57:14 NMI backtrace for cpu 58 skipped: idling at intel_idle+0x6f/
      ...
      "pid": 2567042
    },
    "tracer_time": "2025-06-10 09:57:12.202 +0800",
    "tracer_type": "auto",
    "time": "2025-06-10 09:57:12.202 +0800",
    "region": "***",
    "tracer_name": "hungtask",
    "es_index_time": 1749520632297
  },
  "fields": {
    "time": [
      "2025-06-10T01:57:12.202Z"
    ]
  },
  "_ignored": [
    "tracer_data.blocked_processes_stack",
    "tracer_data.cpus_stack"
  ],
  "_version": 1,
  "sort": [
    1749520632202
  ]
}
```

Additionally, the hungtask event implements the `Collector` interface, which also enables collecting statistics on host hungtask occurrences via Prometheus.

### Container/Host Memory Reclamation

**Feature Introduction**

When memory pressure is excessively high, if a process requests memory at this time, it may enter direct reclamation. This phase involves synchronous reclamation and may cause business process stalls. Recording the time when a process enters direct reclamation helps us assess the severity of impact from direct reclamation on that process. The memreclaim event calculates whether the same process remains in direct reclamation for over 900ms within a 1-second cycle; if so, it records the process's contextual information.



**Example**

When a business container's chrome process enters direct reclamation, the ES query output is as follows:

```json
{
  "_index": "***_cases_2025-06-11",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "tracer_data": {
      "comm": "chrome",
      "deltatime": 1412702917,
      "pid": 1896137
    },
    "container_host_namespace": "***",
    "container_hostname": "***.docker",
    "es_index_time": 1749641583290,
    "uploaded_time": "2025-06-11T19:33:03.26754495+08:00",
    "hostname": "***",
    "container_type": "normal",
    "tracer_time": "2025-06-11 19:33:03.267 +0800",
    "time": "2025-06-11 19:33:03.267 +0800",
    "region": "***",
    "container_level": "102",
    "container_id": "921d0ec0a20c",
    "tracer_name": "directreclaim"
  },
  "fields": {
    "time": [
      "2025-06-11T11:33:03.267Z"
    ]
  },
  "_version": 1,
  "sort": [
    1749641583267
  ]
}
```

### Network Device Status

**Feature Introduction**

Network card status changes often cause severe network issues, directly impacting overall host network quality, such as down/up states, MTU changes, etc. Taking the down state as an example, possible causes include operations by privileged processes, underlying cable issues, optical module failures, peer switch problems, etc. The netdev event is designed to detect network device status changes and currently implements monitoring for network card down/up events, distinguishing between administrator-initiated and underlying cause-induced status changes.

**Example**

When an administrator operation causes the eth1 network card to go down, the ES query event output is as follows:

```json
{
  "_index": "***_cases_2025-05-30",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-05-30T17:47:50.406913037+08:00",
    "hostname": "localhost.localdomain",
    "tracer_data": {
      "ifname": "eth1",
      "start": false,
      "index": 3,
      "linkstatus": "linkStatusAdminDown, linkStatusCarrierDown",
      "mac": "5c:6f:69:34:dc:72"
    },
    "tracer_time": "2025-05-30 17:47:50.406 +0800",
    "tracer_type": "auto",
    "time": "2025-05-30 17:47:50.406 +0800",
    "region": "***",
    "tracer_name": "netdev_event",
    "es_index_time": 1748598470407
  },
  "fields": {
    "time": [
      "2025-05-30T09:47:50.406Z"
    ]
  },
  "_version": 1,
  "sort": [
    1748598470406
  ]
}
```

### LACP Protocol Status

**Feature Introduction**

Bond is a technology provided by the Linux system kernel that bundles multiple physical network interfaces into a single logical interface. Through bonding, bandwidth aggregation, failover, or load balancing can be achieved. LACP is a protocol defined by the IEEE 802.3ad standard for dynamically managing Link Aggregation Groups (LAG). Currently, there is no elegant method to obtain physical host LACP protocol negotiation exception events. HUATUO implements the lacp event, which uses BPF to instrument key protocol paths. When a change in link aggregation status is detected, it triggers an event to record relevant information.

**Example**

When the host network card eth1 experiences physical layer down/up fluctuations, the LACP dynamic negotiation status becomes abnormal. The ES query output is as follows:

```json
{
  "_index": "***_cases_2025-05-30",
  "_type": "_doc",
  "_id": "***",
  "_score": 0,
  "_source": {
    "uploaded_time": "2025-05-30T17:47:48.513318579+08:00",
    "hostname": "***",
    "tracer_data": {
      "content": "/proc/net/bonding/bond0\nEthernet Channel Bonding Driver: v4.18.0 (Apr 7, 2025)\n\nBonding Mode: load balancing (round-robin)\nMII Status: down\nMII Polling Interval (ms): 0\nUp Delay (ms): 0\nDown Delay (ms): 0\nPeer Notification Delay (ms): 0\n/proc/net/bonding/bond4\nEthernet Channel Bonding Driver: v4.18.0 (Apr 7, 2025)\n\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nTransmit Hash Policy: layer3+4 (1)\nMII Status: up\nMII Polling Interval (ms): 100\nUp Delay (ms): 0\nDown Delay (ms): 0\nPeer Notification Delay (ms): 1000\n\n802.3ad info\nLACP rate: fast\nMin links: 0\nAggregator selection policy (ad_select): stable\nSystem priority: 65535\nSystem MAC address: 5c:6f:69:34:dc:72\nActive Aggregator Info:\n\tAggregator ID: 1\n\tNumber of ports: 2\n\tActor Key: 21\n\tPartner Key: 50013\n\tPartner Mac Address: 00:00:5e:00:01:01\n\nSlave Interface: eth0\nMII Status: up\nSpeed: 25000 Mbps\nDuplex: full\nLink Failure Count: 0\nPermanent HW addr: 5c:6f:69:34:dc:72\nSlave queue ID: 0\nSlave active: 1\nSlave sm_vars: 0x172\nAggregator ID: 1\nAggregator active: 1\nActor Churn State: none\nPartner Churn State: none\nActor Churned Count: 0\nPartner Churned Count: 0\ndetails actor lacp pdu:\n    system priority: 65535\n    system mac address: 5c:6f:69:34:dc:72\n    port key: 21\n    port priority: 255\n    port number: 1\n    port state: 63\ndetails partner lacp pdu:\n    system priority: 200\n    system mac address: 00:00:5e:00:01:01\n    oper key: 50013\n    port priority: 32768\n    port number: 16397\n    port state: 63\n\nSlave Interface: eth1\nMII Status: up\nSpeed: 25000 Mbps\nDuplex: full\nLink Failure Count: 17\nPermanent HW addr: 5c:6f:69:34:dc:73\nSlave queue ID: 0\nSlave active: 0\nSlave sm_vars: 0x172\nAggregator ID: 1\nAggregator active: 1\nActor Churn State: monitoring\nPartner Churn State: monitoring\nActor Churned Count: 2\nPartner Churned Count: 2\ndetails actor lacp pdu:\n    system priority: 65535\n    system mac address: 5c:6f:69:34:dc:72\n    port key: 21\n    port priority: 255\n    port number: 2\n    port state: 15\ndetails partner lacp pdu:\n    system priority: 200\n    system mac address: 00:00:5e:00:01:01\n    oper key: 50013\n    port priority: 32768\n    port number: 32781\n    port state: 31\n"
    },
    "tracer_time": "2025-05-30 17:47:48.513 +0800",
    "tracer_type": "auto",
    "time": "2025-05-30 17:47:48.513 +0800",
    "region": "***",
    "tracer_name": "lacp",
    "es_index_time": 1748598468514
  },
  "fields": {
    "time": [
      "2025-05-30T09:47:48.513Z"
    ]
  },
  "_ignored": [
    "tracer_data.content"
  ],
  "_version": 1,
  "sort": [
    1748598468513
  ]
}
```
