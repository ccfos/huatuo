---
title: 异常事件
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

当前版本支持 Linux 内核事件如下：

| 事件名称        | 核心功能               | 场景                                    |
| ---------------| --------------------- |----------------------------------------|
| softirq        |  检测内核关闭中断时间过长，输出关闭软中断的内核调用栈，进程信息等 | 解决系统异常，应用异常导致的系统卡顿，网络延迟，调度延迟等问题。 |
| softlockup     | 检测系统 softlockup 事件，提供目标进程，cpu 内核栈信息等 | 解决系统 softlockup 问题。|
| hungtask       | 检测系统 hungtask 事件，提供系统内所有 D 状态进程、栈信息等 | 定位瞬时批量出现 D 进程的场景，保留故障现场便于后期问题跟踪。 |
| oom            | 检测宿主或容器内 oom 事件 | 聚焦物理机或者容器内存耗尽问题，输出更详细故障快照，解决业务因内存不可用导致的故障。 |
| memory_reclaim_events | 检测系统内存直接回收事件，记录直接回收耗时，进程，容器等信息 | 解决系统因内存压力过大，导致的业务进程的卡顿等场景。|
| ras  | 检测 CPU、Memory、PCIe 等硬件故障事件，输出具体详细故障信息 | 及时感知和预测硬件故障，降低因物理硬件不可用导致的业务有损问题。|
| dropwatch      | 检测内核网络协议栈丢包问题，输出丢包调用栈、网络信息等 | 解决协议栈丢包导致的业务毛刺和延迟等问题。 |
| net_rx_latency | 检测协议栈收方向驱动、协议、用户主动收过程的延迟事件 | 解决因协议栈接收延迟，应用响应接收延迟等导致的业务超时，毛刺等问题。 |
| netdev_events  | 检测网卡链路状态变化，输出具体类型 | 感知网卡物理链路状态，解决因网卡故障导致的业务不可用问题。|
| netdev_bonding_lacp | 检测 bonding lacp 协议状态变化，记录详细事件信息 | 解决 bonding lacp 模式下协议协商问题，界定物理机，交换机故障边界。|
| netdev_txqueue_timeout | 检测网卡发送队列超时事件 | 定位网卡发送队列硬件故障问题。|


### 软中断关闭

Linux 内核存在进程上下文，中断上下文，软中断上下文，NMI 上下文等概念，这些上下文之间可能存在共享数据情况，因此为了确保数据的一致性，正确性，内核代码可能会关闭软中断或者硬中断。从理论角度，单次关闭中断或者软中断时间不能太长，但高频的系统调用，陷入内核态频繁执行关闭中断或软中断，同样会造"长时间关闭"的现象，拖慢了系统的响应。“关闭中断，软中断时间过长”这类问题非常隐蔽，且定位手段有限，同时影响又非常大，体现在业务应用上一般为接收数据超时。针对这种场景我们基于BPF技术构建了检测硬件中断，软件中断关闭过长的能力。

如下为抓取到的关闭中断过长的实例，这些信息被自动存储 ElasticSearch, 或者物理机磁盘文件.

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

### 协议栈丢包

在数据包收发过程中由于各类原因，可能出现丢包的现象，丢包可能会导致业务请求延迟，甚至超时。dropwatch 借助 eBPF 观测内核网络数据包丢弃情况，输出丢包网络上下文，如：源目的地址，源目的端口，seq, seqack, pid, comm, stack 信息等。dorpwatch 主要用于检测 TCP 协议相关的丢包，通过预先埋点过滤数据包，确定丢包位置以便于排查丢包根因。如下为抓取到的一案例：kubelet 在发送 SYN 时，由于设备丢包，导致数据包发送失败。

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

### 协议栈延迟

线上业务网络延迟问题是比较难定位的，任何方向，任何的阶段都有可能出现问题。比如收方向的延迟，驱动、协议栈、用户程序等都有可能出现问题，因此我们开发了 net_rx_latency 检测功能，借助 skb 入网卡的时间戳，在驱动，协议栈层，用户态层检查延迟时间，当收包延迟达到阈值时，借助 eBPF 获取网络上下文信息（五元组、延迟位置、进程信息等）。收方向传输路径示意：**网卡 -> 驱动 -> 协议栈 -> 用户主动收**。业务容器从内核收包延迟超过 90s，通过 net_rx_latency 追踪，输出如下：

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

### out_of_memory

程序运行时申请的内存超过了系统或进程可用的内存上限，导致系统或应用程序崩溃。常见于内存泄漏、大数据处理或资源配置不足的场景。通过在 oom 的内核流程插入 BPF 钩子，获取 oom 上下文的详细信息并传递到用户态。这些信息包括进程信息、被 kill 的进程信息、容器信息。

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

### softlockup

softlockup 是 Linux 内核检测到的一种异常状态，指某个 CPU 核心上的内核线程（或进程）长时间占用 CPU 且不调度，导致系统无法正常响应其他任务。如内核代码 bug、cpu 过载、设备驱动问题等都会导致 softlockup。当系统发生 softlockup 时，收集目标进程的信息以及 cpu 信息，获取各个 cpu 上的内核栈信息同时保存问题的发生次数。

### hungtask

D 状态进程（也称为不可中断睡眠状态，Uninterruptible）是一种特殊的进程状态，表示进程因等待某些系统资源而阻塞，且不能被信号或外部中断唤醒。常见场景如：磁盘 I/O 操作、内核阻塞、硬件故障等。hungtask 捕获系统内所有 D 状态进程的内核栈并保存 D 进程的数量。用于定位瞬间出现一些 D 进程的场景，可以在现场消失后仍然分析到问题根因。

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

### 内存回收

内存压力过大时，如果此时进程申请内存，有可能进入直接回收，此时处于同步回收阶段，可能会造成业务进程的卡顿，在此记录进程进入直接回收的时间，有助于我们判断此进程被直接回收影响的剧烈程度。memreclaim event 计算同一个进程在 1s 周期，若进程处在直接回收状态超过 900ms， 则记录其上下文信息。

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

### 网络设备

网卡状态变化通常容易造成严重的网络问题，直接影响整机网络质量，如 down/up, MTU 改变等。以 down 状态为例，可能是有权限的进程操作、底层线缆、光模块、对端交换机等问题导致，netdev event 用于检测网络设备的状态变化，目前已实现网卡 down, up 的监控，并区分管理员或底层原因导致的网卡状态变化。 一次管理员操作导致 eth1 网卡 down 时，输出如下：

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

### bonding lacp

Bond 是 Linux 系统内核提供的一种将多个物理网络接口绑定为一个逻辑接口的技术。通过绑定，可以实现带宽叠加、故障切换或负载均衡。LACP 是 IEEE 802.3ad 标准定义的协议，用于动态管理链路聚合组（LAG）。目前没有优雅获取物理机LACP 协议协商异常事件的方法，HUATUO 实现了 lacp event，通过 BPF 在协议关键路径插桩检测到链路聚合状态发生变化时，触发事件记录相关信息。

在宿主网卡 eth1 出现物理层 down/up 抖动时，lacp 动态协商状态异常，输出如下：

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
