---
title: Metrics Reference
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

This reference lists metric names, units, labels, sources, and measurement semantics. See [Kernel-Wide Insight](/docs/key-feature/kernel-wide-insight_en.md) for capabilities and troubleshooting scenarios.

## How to Read This Reference

Common metrics are grouped by CPU, memory, network, I/O, system events, and GPU for querying and troubleshooting. Actual output depends on kernel interfaces, device drivers, enabled collectors, and filters. The node's `/metrics` output is authoritative for the complete set. See [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md) for enablement.

- **Names**: tables omit the `huatuo_bamai_` prefix. For example, `cpu_util_total` is exposed as `huatuo_bamai_cpu_util_total`.
- **Labels**: all metrics include `host` and `region`. Container metrics also include `container_host`, `container_hostnamespace`, `container_level`, `container_name`, and `container_type`. Tables list only additional labels; `—` means none.
- **Counts and units**: counts measure events or objects, not latency. Use `rate()` or `increase()` for cumulative Counters. Some cumulative kernel statistics are exported as Gauges; interpret their field semantics rather than inferring from type or name alone.
- **Examples**: values illustrate output format, not diagnostic thresholds. A missing metric does not mean zero.

```text
# TYPE huatuo_bamai_runqlat_latency counter
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="0"} 35100
# TYPE huatuo_bamai_runqlat_container_latency counter
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="0"} 226
```

This example shows host and container scheduling-latency bucket counts. The `zone` ranges are listed below.

## CPU

### Scheduling Latency

Scheduling latency is the wait from becoming runnable to actually receiving CPU time. These eBPF metrics are cumulative Counters bucketed by `zone`: `0` = [0, 10) ms, `1` = [10, 20) ms, `2` = [20, 50) ms, and `3` = >=50 ms.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `runqlat_container_latency` | Scheduling-latency bucket count | Count | Container | eBPF | zone |
| `runqlat_latency` | Scheduling-latency bucket count | Count | Host | eBPF | zone |

### Softirq Latency

Measures the wait from softirq raise to processing on each CPU. Currently only `NET_RX` and `NET_TX` are emitted. This is a cumulative Counter with `zone` buckets: `0` = [0, 10) μs, `1` = [10, 100) μs, `2` = [100, 1000) μs, and `3` = >=1 ms.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `softirq_latency` | Softirq response-latency bucket count | Count | Host | eBPF | cpuid, type, zone |

### Utilization

Host utilization is normalized by online CPU cores; container utilization by available CPU capacity. Values range from 0 to 100%.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `cpu_util_sys` | Kernel-mode CPU utilization | % | Host | — |
| `cpu_util_usr` | User-mode CPU utilization | % | Host | — |
| `cpu_util_total` | Total CPU utilization | % | Host | — |
| `cpu_util_container_sys` | Kernel-mode CPU utilization | % | Container | — |
| `cpu_util_container_usr` | User-mode CPU utilization | % | Container | — |
| `cpu_util_container_total` | Total CPU utilization | % | Container | — |

### Available Cores

Container CPU capacity is the smaller of quota-derived cores and the effective CPU set size. Without a quota, it uses the effective CPU set size; if unavailable, it falls back to online host cores.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `cpu_util_container_cores` | Available CPU capacity (may be fractional) | Cores | Container | — |

### Throttling and Contention

Throttling statistics come from cgroup CPU interfaces and include only fields provided by the kernel. The wait ratio also requires kernel `wait_sum` and `kernel.sched_schedstats=1`.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `cpu_stat_container_nr_throttled` | Cumulative periods in which the cgroup was CPU-throttled | Count | Container | — |
| `cpu_stat_container_throttled_time` | Cumulative time the cgroup was throttled | Nanoseconds | Container | — |
| `cpu_stat_container_wait_sum_percent` | Cgroup wait time in the parent run queue as a share of schedulable time during the interval | % | Container | — |

The interval wait ratio is `wait-time delta / (wait-time delta + CPU-usage delta) × 100%`. It measures waiting as a share of schedulable time, not the share spent throttled.

References:

- [CFS bandwidth statistics](https://docs.kernel.org/scheduler/sched-bwc.html#statistics)
- [cgroup v2 CPU interface](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html#cpu-interface-files)

### Burst Usage

Counts periods and time using burst CPU capacity. Emitted only when the kernel provides the corresponding fields.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `cpu_stat_container_burst_time` | Cumulative time using burst CPU capacity | Nanoseconds | Container | — |
| `cpu_stat_container_nr_bursts` | Cumulative periods using burst CPU capacity | Count | Container | — |

### Load

Hosts report 1-, 5-, and 15-minute load averages. Containers report current runnable and uninterruptible task counts, which use a different time basis.

| Metric | Description | Unit | Scope | Additional Labels | Notes |
| --- | --- | --- | --- | --- | --- |
| `loadavg_load1` | System load average over the past 1 minute | Tasks | Host | — | — |
| `loadavg_load5` | System load average over the past 5 minutes | Tasks | Host | — | — |
| `loadavg_load15` | System load average over the past 15 minutes | Tasks | Host | — | — |
| `loadavg_container_nr_running` | Current runnable tasks, including running tasks and tasks waiting for CPU | Count | Container | — | cgroup v1 only |
| `loadavg_container_nr_uninterruptible` | Uninterruptible tasks in the container | Count | Container | — | cgroup v1 only |

## Memory

### Reclaim

Memory reclaim and compaction can block tasks. These metrics record host cumulative durations and container direct-reclaim event counts.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `memory_free_allocpages_stall` | Cumulative host direct-reclaim duration | Milliseconds | Host | eBPF | — |
| `memory_free_compaction_stall` | Cumulative host memory-compaction duration | Milliseconds | Host | eBPF | — |
| `memory_reclaim_container_directstall` | Container direct-reclaim event count | Count | Container | eBPF | — |

`memory_others_container_directstall_time`, `memory_others_container_asyncreclaim_time`, and `memory_others_container_local_direct_reclaim_time` depend on DiDi custom-kernel interfaces `memory.directstall_stat`, `memory.asynreclaim_stat`, and `memory.local_direct_reclaim_time`.

Mainline and common distribution kernels without these interfaces do not emit the metrics; no additional module is needed. Use `memory_reclaim_container_directstall` for container direct-reclaim counts on standard kernels.

### Memory State

Container fields come from `memory.stat`; host fields from `/proc/vmstat`. Both are subject to `MetricCollector.Vmstat` filters. Field availability and meanings depend on kernel and cgroup versions.

**Container memory**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_container_active_file` | Active file memory | Bytes | Container | — |
| `memory_vmstat_container_active_anon` | Active anonymous memory | Bytes | Container | — |
| `memory_vmstat_container_inactive_file` | Inactive file memory | Bytes | Container | — |
| `memory_vmstat_container_inactive_anon` | Inactive anonymous memory | Bytes | Container | — |
| `memory_vmstat_container_file_dirty` | Modified file memory not yet written to disk | Bytes | Container | — |
| `memory_vmstat_container_file_writeback` | File memory being written back | Bytes | Container | — |
| `memory_vmstat_container_dirty` | Modified memory not yet written to disk | Bytes | Container | — |
| `memory_vmstat_container_writeback` | Memory being written back | Bytes | Container | — |
| `memory_vmstat_container_pgdeactivate` | Pages moved from the active to inactive LRU | Pages | Container | — |
| `memory_vmstat_container_pgrefill` | Total pages scanned on the active LRU | Pages | Container | — |
| `memory_vmstat_container_pgscan_direct` | Total pages scanned on the inactive LRU during direct reclaim | Pages | Container | — |
| `memory_vmstat_container_pgscan_kswapd` | Total pages scanned on the inactive LRU by kswapd | Pages | Container | — |
| `memory_vmstat_container_pgsteal_direct` | Total pages reclaimed from the inactive LRU by direct reclaim | Pages | Container | — |
| `memory_vmstat_container_pgsteal_kswapd` | Total pages reclaimed from the inactive LRU by kswapd | Pages | Container | — |
| `memory_vmstat_container_unevictable` | Bytes in unevictable pages | Bytes | Container | — |

**Host memory**

The names below include `memory_vmstat_`. Underlying values are exported unchanged rather than uniformly converted to bytes. `pgpgin` and `pgpgout` use the kernel's KiB units; see the [Linux vmstat implementation](https://github.com/torvalds/linux/blob/v6.6/mm/vmstat.c#L1714-L1717).

**Page state and LRU**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_nr_free_pages` | Free pages immediately allocatable by the buddy system | Pages | Host | — |
| `memory_vmstat_nr_inactive_anon` | Inactive anonymous pages | Pages | Host | — |
| `memory_vmstat_nr_inactive_file` | Inactive file pages | Pages | Host | — |
| `memory_vmstat_nr_active_anon` | Active anonymous pages | Pages | Host | — |
| `memory_vmstat_nr_active_file` | Active file pages | Pages | Host | — |
| `memory_vmstat_nr_unevictable` | Unevictable pages | Pages | Host | — |
| `memory_vmstat_nr_mlock` | Pages locked by mlock() | Pages | Host | — |
| `memory_vmstat_nr_shmem` | Pages used by tmpfs/shmem | Pages | Host | — |
| `memory_vmstat_nr_slab_reclaimable` | Pages used by reclaimable Slab | Pages | Host | — |
| `memory_vmstat_nr_slab_unreclaimable` | Pages used by unreclaimable Slab | Pages | Host | — |

**Dirty pages and writeback**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_nr_dirty` | Current dirty pages | Pages | Host | — |
| `memory_vmstat_nr_writeback` | Pages being written back | Pages | Host | — |
| `memory_vmstat_nr_dirty_threshold` | Dirty-page throttling threshold | Pages | Host | — |
| `memory_vmstat_nr_dirty_background_threshold` | Background writeback threshold | Pages | Host | — |

**Page faults and swapping**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_pgfault` | Total page faults | Count | Host | — |
| `memory_vmstat_pgmajfault` | Major page faults | Count | Host | — |
| `memory_vmstat_pgpgin` | Data read from block devices | KiB | Host | — |
| `memory_vmstat_pgpgout` | Data written to block devices | KiB | Host | — |
| `memory_vmstat_pswpin` | Pages swapped in | Pages | Host | — |
| `memory_vmstat_pswpout` | Pages swapped out | Pages | Host | — |

**Reclaim and scanning**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_pgscan_kswapd` | Pages scanned by kswapd | Pages | Host | — |
| `memory_vmstat_pgscan_direct` | Pages scanned by direct reclaim | Pages | Host | — |
| `memory_vmstat_pgscan_khugepaged` | Pages scanned by khugepaged | Pages | Host | — |
| `memory_vmstat_pgsteal_kswapd` | Pages reclaimed by kswapd | Pages | Host | — |
| `memory_vmstat_pgsteal_direct` | Pages reclaimed by direct reclaim | Pages | Host | — |
| `memory_vmstat_pgsteal_khugepaged` | Pages reclaimed by khugepaged | Pages | Host | — |

**Transparent Huge Pages (THP)**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_thp_fault_alloc` | Successful THP allocations on page faults | Count | Host | — |
| `memory_vmstat_thp_fault_fallback` | THP allocation failures on faults that fall back to ordinary pages | Count | Host | — |
| `memory_vmstat_thp_collapse_alloc` | Successful THP collapses by khugepaged | Count | Host | — |
| `memory_vmstat_thp_collapse_alloc_failed` | Failed THP collapses by khugepaged | Count | Host | — |

**NUMA**

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_vmstat_numa_hit` | Pages allocated on the preferred NUMA node | Pages | Host | — |
| `memory_vmstat_numa_miss` | Pages allocated on this node when another node was preferred | Pages | Host | — |
| `memory_vmstat_numa_foreign` | Pages allocated elsewhere when this node was preferred | Pages | Host | — |
| `memory_vmstat_numa_local` | Pages allocated on the current CPU's NUMA node | Pages | Host | — |
| `memory_vmstat_numa_other` | Pages allocated outside the current CPU's NUMA node | Pages | Host | — |
| `memory_vmstat_numa_pages_migrated` | Total pages successfully migrated by automatic NUMA balancing | Pages | Host | — |

References:

- [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)
- [cgroup v1 memory controller](https://docs.kernel.org/admin-guide/cgroup-v1/memory.html)
- [Transparent Huge Pages](https://docs.kernel.org/admin-guide/mm/transhuge.html)

### Memory Events

These fields come from `memory.events` and count reclaim, memory-limit, and OOM events. Mainline kernels provide it in cgroup v2; DiDi custom kernels may also provide it in cgroup v1. Nothing is emitted if the interface is absent. Fields count different conditions and must not be added as independent events.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `memory_events_container_low` | Reclaims under pressure below memory.low, usually indicating overcommitted low-memory protection | Count | Container | — |
| `memory_events_container_high` | Throttling and direct-reclaim events after exceeding memory.high | Count | Container | — |
| `memory_events_container_max` | Attempts to exceed memory.max | Count | Container | — |
| `memory_events_container_oom` | Events where memory reaches the limit, allocation is about to fail, and OOM handling is considered | Count | Container | — |
| `memory_events_container_oom_kill` | Processes in this cgroup killed by any OOM killer | Count | Container | — |
| `memory_events_container_oom_group_kill` | Group OOM kill events | Count | Container | — |

### Free Memory Blocks

Shows free block counts by NUMA node, memory zone, and order. `order=n` means each block contains `2^n` contiguous pages. The value counts blocks, not pages.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `memory_buddyinfo_blocks` | Free blocks for the specified NUMA node, zone, and order | Blocks | Host | procfs | node, order, zone |

## Network

### TCP Memory

Reports TCP memory usage and its proportion of the limit.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `tcp_memory_limit_pages` | Total allowed TCP memory | Pages | Host | — |
| `tcp_memory_usage_bytes` | TCP memory in use | Bytes | Host | — |
| `tcp_memory_usage_pages` | TCP memory in use | Pages | Host | — |
| `tcp_memory_usage_percent` | TCP memory usage as a percentage of the total TCP memory limit | % | Host | — |

### Connection Tracking

Host nf_conntrack table usage and capacity come from `/proc/sys/net/netfilter/nf_conntrack_count` and `/proc/sys/net/netfilter/nf_conntrack_max`. The repository configuration disables `net_conntrack` through `BlackList` by default.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `net_conntrack_entry_count` | Current nf_conntrack table entries | Entries | Host | — |
| `net_conntrack_entry_max` | Maximum nf_conntrack table entries | Entries | Host | — |

### ARP Neighbors

Counts ARP neighbor entries in host and container network namespaces.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `arp_entries` | ARP entries in the host network namespace | Count | Host namespace | — |
| `arp_total` | Total ARP entries across all network namespaces on the host | Count | Host | — |
| `arp_container_entries` | ARP entries in the container network namespace | Count | Container | — |

### Transmit Queues

Qdisc metrics describe backlog, transmitted traffic, drops, and requeues, not queueing latency directly. Only root Qdiscs of matching devices are collected; `noqueue` is skipped. The repository configuration disables `netdev_qdisc` through `BlackList` by default.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `netdev_qdisc_backlog` | Bytes queued for transmission | Bytes | Host | device, kind |
| `netdev_qdisc_current_queue_length` | Current queued packets | Count | Host | device, kind |
| `netdev_qdisc_overlimits_total` | Overlimit events | Count | Host | device, kind |
| `netdev_qdisc_requeues_total` | Requeues because the NIC/driver temporarily cannot transmit | Count | Host | device, kind |
| `netdev_qdisc_drops_total` | Packets dropped due to full queues, rate-limit policies, or similar causes | Count | Host | device, kind |
| `netdev_qdisc_bytes_total` | Cumulative transmitted bytes | Bytes | Host | device, kind |
| `netdev_qdisc_packets_total` | Transmitted packets | Count | Host | device, kind |

### Hardware Drops

Counts receive-side hardware drops for supported NIC drivers. It prefers `rx_missed_errors`; otherwise it attempts to subtract eBPF software-drop counts from `rx_dropped`. The repository configuration disables `netdev_hw` through `BlackList` by default.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `netdev_hw_rx_dropped_total` | Cumulative receive-side hardware drops | Count | Host | sysfs, eBPF | device, driver |

### NIC Statistics

Collects NIC statistics through proc or netlink. Common metrics follow [Linux `/proc/net/dev`](https://github.com/torvalds/linux/blob/v6.6/net/core/net-procfs.c#L72-L92) semantics:

- `receive_dropped`：`rx_dropped + rx_missed_errors`。
- `receive_frame_errors`：`rx_length_errors + rx_over_errors + rx_crc_errors + rx_frame_errors`。
- `transmit_carrier_errors`：`tx_carrier_errors + tx_aborted_errors + tx_window_errors + tx_heartbeat_errors`。

Netlink also provides detailed error metrics and `receive_nohandler`. Detailed errors may already be included in the aggregates above and must not be added again. Tables show host names; container names insert `container_` after `netdev_`.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `netdev_receive_bytes_total` | Total bytes successfully received | Bytes | Host, container | device |
| `netdev_receive_packets_total` | Total packets successfully received | Count | Host, container | device |
| `netdev_receive_multicast_total` | Received multicast packets | Count | Host, container | device |
| `netdev_receive_compressed_total` | Received compressed packets | Count | Host, container | device |
| `netdev_receive_frame_errors_total` | Total receive length, overflow, CRC, and frame-alignment errors | Count | Host, container | device |
| `netdev_receive_errors_total` | Total receive errors | Count | Host, container | device |
| `netdev_receive_dropped_total` | Receive drops, including missed errors | Count | Host, container | device |
| `netdev_receive_fifo_errors_total` | Receive FIFO/ring-buffer overflow errors | Count | Host, container | device |
| `netdev_transmit_bytes_total` | Total bytes successfully transmitted | Bytes | Host, container | device |
| `netdev_transmit_packets_total` | Total packets successfully transmitted | Count | Host, container | device |
| `netdev_transmit_collisions_total` | Transmit collisions | Count | Host, container | device |
| `netdev_transmit_errors_total` | Total transmit errors | Count | Host, container | device |
| `netdev_transmit_dropped_total` | Packets dropped during transmission due to full queues, policies, or similar causes | Count | Host, container | device |
| `netdev_transmit_fifo_errors_total` | Transmit FIFO/ring-buffer errors | Count | Host, container | device |
| `netdev_transmit_carrier_errors_total` | Total transmit carrier, aborted, window, and heartbeat errors | Count | Host, container | device |
| `netdev_transmit_compressed_total` | Transmitted compressed packets | Count | Host, container | device |

### TCP Statistics

Reads network-namespace `/proc/<pid>/net/netstat` and `snmp`, subject to `MetricCollector.Netstat` filters. Tables show host names; container names insert `container_` after `netstat_`.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `netstat_TcpExt_ArpFilter` | Packets dropped by ARP filtering rules | Count | Host, container | — |
| `netstat_TcpExt_BusyPollRxPackets` | Packets received through busy polling | Count | Host, container | — |
| `netstat_TcpExt_DelayedACKLocked` | Delayed ACK timer expirations postponed because user space holds the socket lock | Count | Host, container | — |
| `netstat_TcpExt_DelayedACKLost` | Already-acknowledged packets received, possibly due to ACK loss or duplicate network packets | Count | Host, container | — |
| `netstat_TcpExt_DelayedACKs` | Delayed ACK timer expirations that sent an ACK | Count | Host, container | — |
| `netstat_TcpExt_EmbryonicRsts` | Packets with RST/SYN flags received in SYN_RECV | Count | Host, container | — |
| `netstat_TcpExt_ListenDrops` | Packets dropped by LISTEN sockets, including accept-queue overflow and memory allocation failures | Count | Host, container | — |
| `netstat_TcpExt_ListenOverflows` | TCP accept-queue overflows | Count | Host, container | — |
| `netstat_TcpExt_OfoPruned` | Out-of-order queue pruning due to memory shortage | Count | Host, container | — |
| `netstat_TcpExt_OutOfWindowIcmps` | Received ICMP errors unrelated to the current TCP window | Count | Host, container | — |
| `netstat_TcpExt_PruneCalled` | Buffer cleanup triggered by memory shortage | Count | Host, container | — |
| `netstat_TcpExt_RcvPruned` | Receive queue pruning (packet drops) due to memory shortage | Count | Host, container | — |
| `netstat_TcpExt_SyncookiesFailed` | SYN cookies that failed validation | Count | Host, container | — |
| `netstat_TcpExt_SyncookiesRecv` | Received SYN cookies | Count | Host, container | — |
| `netstat_TcpExt_SyncookiesSent` | Sent SYN cookies | Count | Host, container | — |
| `netstat_TcpExt_TCPACKSkippedChallenge` | Other ACKs skipped while processing Challenge ACKs | Count | Host, container | — |
| `netstat_TcpExt_TCPACKSkippedFinWait2` | ACKs skipped in FIN-WAIT-2 | Count | Host, container | — |
| `netstat_TcpExt_TCPACKSkippedPAWS` | ACKs skipped after PAWS check failures | Count | Host, container | — |
| `netstat_TcpExt_TCPACKSkippedSeq` | ACKs skipped due to sequence checks | Count | Host, container | — |
| `netstat_TcpExt_TCPACKSkippedTimeWait` | ACKs skipped in TIME-WAIT | Count | Host, container | — |
| `netstat_TcpExt_TCPAbortOnClose` | Connections aborted when applications close with unread data | Count | Host, container | — |
| `netstat_TcpExt_TCPAbortOnData` | Connections aborted with data in flight, sending RST | Count | Host, container | — |
| `netstat_TcpExt_TCPAbortOnLinger` | Connections aborted after a LINGER timeout | Count | Host, container | — |
| `netstat_TcpExt_TCPAbortOnMemory` | Connections closed due to memory problems | Count | Host, container | — |
| `netstat_TcpExt_TCPAbortOnTimeout` | Connections closed after timer retransmission limits are exceeded | Count | Host, container | — |
| `netstat_TcpExt_TCPLossFailures` | Failed recovery from packet loss | Count | Host, container | — |
| `netstat_TcpExt_TCPLossProbeRecovery` | Packet losses detected and recovered through TLP | Count | Host, container | — |
| `netstat_TcpExt_TCPLossProbes` | Tail Loss Probe (TLP) packets sent | Count | Host, container | — |
| `netstat_TcpExt_TCPLossUndo` | Loss-state undo operations during recovery | Count | Host, container | — |
| `netstat_TcpExt_TCPLostRetransmit` | Lost retransmissions | Count | Host, container | — |

Only selected TcpExt fields are listed. See Linux kernel documentation for other fields and version differences.

References:

- [TCP/SNMP counters](https://docs.kernel.org/networking/snmp_counter.html)

### Socket Usage

Reads network-namespace `/proc/<pid>/net/sockstat`. Tables show host names; container names insert `container_` after `sockstat_`. `mem` and `mem_bytes` are emitted only for the host.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `sockstat_sockets_used` | Sockets currently in use | Count | Host, container | — |
| `sockstat_TCP_inuse` | TCP sockets in connection states such as ESTABLISHED and LISTEN, excluding TIME_WAIT | Count | Host, container | — |
| `sockstat_TCP_orphan` | TCP sockets no longer associated with user-space file handles | Count | Host, container | — |
| `sockstat_TCP_tw` | TCP sockets in TIME_WAIT | Count | Host, container | — |
| `sockstat_TCP_alloc` | Total allocated TCP socket objects | Count | Host, container | — |
| `sockstat_TCP_mem` | Kernel memory pages currently used by TCP sockets | Pages | Host | — |
| `sockstat_UDP_inuse` | UDP sockets currently bound to local ports | Count | Host, container | — |

## I/O

`iolatency` uses eBPF to measure block I/O latency distributions and disk freeze events.

- `q2c`: enqueue to completion, covering the entire I/O lifecycle.
- `d2c`: driver dispatch to completion, closer to device and driver time.
- `freeze`: disk freeze event count.

### Request Latency

These metrics are cumulative Counters. The current implementation counts only requests for which both `q2c` and `d2c` are available and at least 20 ms. The buckets therefore do not represent all I/O requests.

`zone` buckets: `0` = [20, 30] ms, `1` = (30, 50] ms, `2` = (50, 100] ms, `3` = (100, 200] ms, `4` = (200, 400] ms, and `5` = >400 ms. `disk` uses major:minor device numbers, such as `253:1`.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `iolatency_blkdisk_q2c` | Enqueue-to-completion latency bucket count | Count | Host | disk, zone |
| `iolatency_blkdisk_d2c` | Driver-dispatch-to-completion latency bucket count | Count | Host | disk, zone |
| `iolatency_container_blkdisk_q2c` | Enqueue-to-completion latency bucket count | Count | Container | disk, zone |
| `iolatency_container_blkdisk_d2c` | Driver-dispatch-to-completion latency bucket count | Count | Container | disk, zone |

### Disk Freezes

These cumulative Counters record block-device freeze events.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `iolatency_blkdisk_freeze` | Host disk freeze event count | Count | Host | disk |

### Disk Statistics

`diskio` reads `/proc/diskstats` and `/proc/stat` for per-device request counts, throughput, cumulative durations, and host CPU iowait.

The repository configuration disables `diskio` through `BlackList` by default. Remove it and restart `huatuo-bamai` to enable it.

Request counts, bytes, and I/O durations are cumulative Counters. Use `rate()` to calculate IOPS, throughput, and duration growth rates. Mean request duration is the duration growth rate divided by request rate; exclude intervals with zero requests. `io_in_progress` and `disk_iowait_percent` are Gauges.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `diskio_read_requests_total` | Cumulative completed read requests; use `rate()` for read IOPS | Count | Host | device |
| `diskio_write_requests_total` | Cumulative completed write requests; use `rate()` for write IOPS | Count | Host | device |
| `diskio_read_bytes_total` | Cumulative bytes read; use `rate()` for read throughput | Bytes | Host | device |
| `diskio_written_bytes_total` | Cumulative bytes written; use `rate()` for write throughput | Bytes | Host | device |
| `diskio_read_time_seconds_total` | Cumulative time spent on completed reads | Seconds | Host | device |
| `diskio_write_time_seconds_total` | Cumulative time spent on completed writes | Seconds | Host | device |
| `diskio_io_in_progress` | Current in-flight I/O requests (queue depth) | Count | Host | device |
| `diskio_disk_iowait_percent` | CPU time spent waiting for I/O as a share of the collection interval | % | Host | — |

### XFS

`xfs` collects per-device host statistics from sysfs with the additional label `device`. `xfs_log_free_bytes` is a Gauge; metrics ending in `_total` are cumulative Counters.

| Metric | Description | Unit |
| --- | --- | --- |
| `xfs_log_free_bytes` | Free log space | Bytes |
| `xfs_log_space_sleep_total` | Cumulative waits for log space | Count |
| `xfs_alloc_blocks_total` | Cumulative allocated blocks | Blocks |
| `xfs_alloc_extents_total` | Cumulative allocated extents | Count |
| `xfs_inode_attempts_total` | Cumulative inode-cache requests | Count |
| `xfs_inode_missed_total` | Cumulative inode-cache misses | Count |
| `xfs_buf_busy_locked_total` | Cumulative busy-buffer lock events | Count |
| `xfs_buf_locked_waited_total` | Cumulative buffer-lock waits | Count |

`xfs_log_free_bytes` is omitted when free log space is unavailable. Restart `huatuo-bamai` after adding an XFS mount.

### Disk busy time and average queue depth

The `diskio` collector also exports two per-device counters (seconds):

| Metric | Meaning |
| --- | --- |
| `huatuo_bamai_diskio_io_time_seconds_total` | Time with I/O in progress |
| `huatuo_bamai_diskio_io_time_weighted_seconds_total` | Time weighted by the number of in-flight I/Os |

Remove `diskio` from `BlackList` to enable the collector. Both counters use
`device`, `host`, and `region` labels and require no additional file reads.

```promql
# Approximate percentage of time busy, per device
100 * rate(huatuo_bamai_diskio_io_time_seconds_total[5m])
# Average number of I/Os in progress over the interval
rate(huatuo_bamai_diskio_io_time_weighted_seconds_total[5m])
```

These measurements complement CPU iowait and the instantaneous
`io_in_progress` gauge. Busy time alone does not measure the capacity of
parallel devices such as NVMe or RAID. Linux can undercount busy time with
concurrent requests; see the [kernel I/O statistics documentation](https://docs.kernel.org/admin-guide/iostats.html).
Use `rate()` to handle counter resets, and select either whole devices or
partitions to avoid double-counting the same I/O.

## System Events

### Soft Lockups

Records soft lockup events detected by the kernel.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `softlockup_total` | System soft lockup event count | Count | Host | eBPF | — |

### Hung Tasks

Records hung-task events detected by the kernel.

| Metric | Description | Unit | Scope | Source | Additional Labels |
| --- | --- | --- | --- | --- | --- |
| `hungtask_total` | System hung-task event count | Count | Host | eBPF | — |

## GPU

### MetaX

The repository configuration disables `metax_gpu` through `BlackList` by default. Meet the device and SDK dependencies before enabling it.

| Metric | Description | Unit | Additional Labels |
| --- | --- | --- | --- |
| `metax_gpu_sdk_info` | GPU SDK information | — | version |
| `metax_gpu_driver_info` | GPU driver information | — | version |
| `metax_gpu_info` | GPU identity and configuration | — | gpu, model, uuid, bios_version, bdf, mode, die_count |
| `metax_gpu_board_power_watts` | GPU board power | W | gpu |
| `metax_gpu_pcie_link_speed_gt_per_second` | Current GPU PCIe link speed | GT/s | gpu |
| `metax_gpu_pcie_link_width_lanes` | Current GPU PCIe link width | Lanes | gpu |
| `metax_gpu_pcie_receive_bytes_per_second` | GPU PCIe receive throughput | B/s | gpu |
| `metax_gpu_pcie_transmit_bytes_per_second` | GPU PCIe transmit throughput | B/s | gpu |
| `metax_gpu_metaxlink_link_speed_gt_per_second` | Current GPU MetaXLink speed | GT/s | gpu, metaxlink |
| `metax_gpu_metaxlink_link_width_lanes` | Current GPU MetaXLink width | Lanes | gpu, metaxlink |
| `metax_gpu_metaxlink_receive_bytes_per_second` | GPU MetaXLink receive throughput | B/s | gpu, metaxlink |
| `metax_gpu_metaxlink_transmit_bytes_per_second` | GPU MetaXLink transmit throughput | B/s | gpu, metaxlink |
| `metax_gpu_metaxlink_receive_bytes_total` | Total GPU MetaXLink bytes received | Bytes | gpu, metaxlink |
| `metax_gpu_metaxlink_transmit_bytes_total` | Total GPU MetaXLink bytes transmitted | Bytes | gpu, metaxlink |
| `metax_gpu_metaxlink_aer_errors_total` | GPU MetaXLink AER errors | Count | gpu, metaxlink, error_type |
| `metax_gpu_status` | GPU status | — | gpu, die |
| `metax_gpu_temperature_celsius` | GPU temperature | °C | gpu, die |
| `metax_gpu_utilization_percent` | GPU utilization (0–100) | % | gpu, die, ip |
| `metax_gpu_memory_total_bytes` | Total GPU memory | Bytes | gpu, die |
| `metax_gpu_memory_used_bytes` | Used GPU memory | Bytes | gpu, die |
| `metax_gpu_clock_mhz` | GPU clock frequency | MHz | gpu, die, ip |
| `metax_gpu_clocks_throttling` | GPU clock-throttling reasons | — | gpu, die, reason |
| `metax_gpu_dpm_performance_level` | GPU DPM performance level | — | gpu, die, ip |
| `metax_gpu_ecc_memory_errors_total` | GPU ECC memory errors | Count | gpu, die, memory_type, error_type |
| `metax_gpu_ecc_memory_retired_pages_total` | GPU ECC retired memory pages | Count | gpu, die |

### MThreads

MThreads GPU collection requires the MTML management library. Startup searches for `libmtml.so.2`, then `libmtml.so`.

Remove `mthreads_gpu` from `BlackList` and restart `huatuo-bamai` to enable it. `EnableHealth`, `EnablePCIe`, and `EnableMTLink` in `MetricCollector.Mthreads` control the corresponding metric groups.

| Metric | Description | Unit | Additional Labels |
| --- | --- | --- | --- |
| `mthreads_gpu_library_info` | MTML library information | — | version |
| `mthreads_gpu_device_info` | GPU information | — | gpu, name, brand, serial |
| `mthreads_gpu_device_pci_info` | GPU PCI information | — | gpu, sbdf |
| `mthreads_gpu_device_spec` | GPU specifications | — | gpu, cores |
| `mthreads_gpu_device_bios_version` | GPU BIOS version | — | gpu, bios |
| `mthreads_gpu_device_musa_capability` | GPU MUSA compute capability | — | gpu, musa_capability |
| `mthreads_gpu_memory_info` | GPU memory information | — | gpu, type, vendor, bus_width |
| `mthreads_gpu_pcie_link_max_speed_gt_per_sec` | Maximum GPU PCIe link speed (hardware capability) | GT/s | gpu |
| `mthreads_gpu_pcie_link_max_width_lanes` | Maximum GPU PCIe link width (hardware capability) | Lanes | gpu |
| `mthreads_gpu_device_power_watts` | GPU device power | W | gpu |
| `mthreads_gpu_gpu_temperature_celsius` | GPU temperature | °C | gpu |
| `mthreads_gpu_memory_temperature_celsius` | GPU memory temperature | °C | gpu |
| `mthreads_gpu_gpu_utilization_percent` | GPU utilization (0-100) | % | gpu |
| `mthreads_gpu_memory_utilization_percent` | GPU memory utilization (0-100) | % | gpu |
| `mthreads_gpu_memory_total_bytes` | Total GPU memory | Bytes | gpu |
| `mthreads_gpu_memory_used_bytes` | Used GPU memory | Bytes | gpu |
| `mthreads_gpu_gpu_clock_mhz` | GPU clock frequency | MHz | gpu |
| `mthreads_gpu_gpu_max_clock_mhz` | Maximum GPU clock frequency | MHz | gpu |
| `mthreads_gpu_memory_clock_mhz` | GPU memory clock frequency | MHz | gpu |
| `mthreads_gpu_memory_max_clock_mhz` | Maximum GPU memory clock frequency | MHz | gpu |
| `mthreads_gpu_gpu_voltage_volts` | GPU voltage | V | gpu |
| `mthreads_gpu_gpu_power_limit_watts` | Enforced GPU power limit | W | gpu |
| `mthreads_gpu_gpu_power_default_limit_watts` | Default GPU power-management limit | W | gpu |
| `mthreads_gpu_fan_rpm` | GPU fan speed | RPM | gpu, fan |
| `mthreads_gpu_fan_speed_percent` | GPU fan speed percentage | % | gpu, fan |
| `mthreads_gpu_gpu_pstate` | GPU performance state (0=P0) | — | gpu |
| `mthreads_gpu_vpu_utilization_percent` | GPU VPU utilization (0-100) | % | gpu |
| `mthreads_gpu_vpu_encoder_utilization_percent` | GPU VPU encoder utilization (0-100) | % | gpu |
| `mthreads_gpu_vpu_decoder_utilization_percent` | GPU VPU decoder utilization (0-100) | % | gpu |
| `mthreads_gpu_vpu_clock_mhz` | GPU VPU clock frequency | MHz | gpu |
| `mthreads_gpu_pcie_link_speed_gt_per_sec` | Current GPU PCIe link speed | GT/s | gpu |
| `mthreads_gpu_pcie_link_width_lanes` | Current GPU PCIe link width | Lanes | gpu |
| `mthreads_gpu_pcie_replay_total` | GPU PCIe replay counter | Count | gpu |
| `mthreads_gpu_mtlink_state` | GPU MtLink state (0=DOWN,1=UP,2=DOWNGRADE) | — | gpu, link |
| `mthreads_gpu_mtlink_link_bandwidth_gb_s` | Maximum GPU MtLink bandwidth per link (static specification, not live throughput) | GB/s | gpu |
| `mthreads_gpu_mtlink_link_count` | Maximum supported GPU MtLink links (static specification) | Links | gpu |
