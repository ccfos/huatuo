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

Container cores represent available capacity, not exclusively assigned physical cores. Existing utilization semantics and metric names are unchanged.

During collection, `cpu_util` and `memory_vmstat` retain available host and container data independently: container discovery failures do not block host metrics, and host read failures do not discard collected container metrics. Errors reach the collector manager, marking the scrape unsuccessful while still emitting partial data; failed metrics are not fabricated as zeros. Collector initialization dependencies and the existing log-and-skip policy for individual container read failures are unchanged.

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

### Configuration

`iotracing`, `iolatency`, `blk_throtl`, `iocost`, and `io_health` are enabled
by default. Add a feature name to the global `BlackList` and restart Huatuo
to disable it. Disabling `iotracing` stops both its disk metrics and automatic
diagnosis. Disabling `io_health` stops health events, evidence collection,
and its metrics independently of `iotracing`.

### Disk performance

The following metrics describe disk performance. They use the
`huatuo_bamai_iotracing_` prefix and are interval Gauges with a `disk` label:

|Metric suffix|Description|
|---|---|
|`read_bps`, `write_bps`|Read/write throughput in bytes per second|
|`read_iops`, `write_iops`|Read/write operations per second|
|`read_await_ms`, `write_await_ms`|Mean read/write completion time in milliseconds|
|`io_util_percent`|Percentage of time the disk is busy|
|`average_queue_size`|Average IO queue length|

Metrics cover whole disks and supported logical devices, excluding partitions
and pseudo devices such as loop, ram, zram, and fd.

### Latency and IO size distributions

`iolatency` measures read/write IO latency and size distributions for SCSI/SAS,
NVMe, and virtio disks and containers. Only disks with `iostats` and
request-time accounting enabled at startup are included. Disabling these settings
during operation is unsupported.
If software blk-crypto fallback is initialized at startup, the entire
`iolatency` collector remains disabled.

SCSI issued sizes use the kernel's 16-bit sector count and wrap at 32 MiB.
When requests merge different blkcgs, container issued-size attribution is
approximate; host accounting is unaffected.

Block device additions, removals, or recreation restart `iolatency`,
`blk_throtl`, and `iocost` collection. Data for all disks may be omitted during
the transition.

Bucket values count IOs in the collection interval at or below the `le` upper
bound; `+Inf` is the total. Latency bounds are in seconds and size bounds in
bytes. Metrics are interval Gauges.

```text
# TYPE huatuo_bamai_iolatency_q2d_seconds_bucket gauge
huatuo_bamai_iolatency_q2d_seconds_bucket{disk="sda",operation="read",host="hostname",region="dev",le="0.001"} 3
huatuo_bamai_iolatency_q2d_seconds_bucket{disk="sda",operation="read",host="hostname",region="dev",le="+Inf"} 5
# TYPE huatuo_bamai_iolatency_d2c_seconds_bucket gauge
huatuo_bamai_iolatency_d2c_seconds_bucket{disk="sda",operation="read",host="hostname",region="dev",le="+Inf"} 5
# TYPE huatuo_bamai_iolatency_q2g_seconds_bucket gauge
huatuo_bamai_iolatency_q2g_seconds_bucket{disk="sda",operation="read",host="hostname",region="dev",le="+Inf"} 5
# TYPE huatuo_bamai_iolatency_container_q2g_seconds_bucket gauge
huatuo_bamai_iolatency_container_q2g_seconds_bucket{container_host="etcd-hostname",container_hostnamespace="kube-system",container_level="burstable",container_name="etcd",container_type="normal",disk="sda",operation="read",host="hostname",region="dev",le="+Inf"} 2
```

|Metric|Description|Unit|Scope|
|---|---|---|---|
|iolatency_q2d_seconds_bucket|Queue-to-last-issue latency distribution|count|Host|
|iolatency_d2c_seconds_bucket|Last-issue-to-complete latency distribution|count|Host|
|iolatency_q2g_seconds_bucket|Queue-to-request-start latency distribution|count|Host|
|iolatency_container_q2d_seconds_bucket|Queue-to-last-issue latency distribution|count|Container|
|iolatency_container_d2c_seconds_bucket|Last-issue-to-complete latency distribution|count|Container|
|iolatency_container_q2g_seconds_bucket|Queue-to-request-start latency distribution|count|Container|
|iolatency_queued_io_size_bytes_bucket|Queued bio size distribution|count|Host|
|iolatency_issued_io_size_bytes_bucket|Request size; last dispatched size for SCSI, completion bytes for NVMe/virtio|count|Host|
|iolatency_container_queued_io_size_bytes_bucket|Queued bio size distribution|count|Container|
|iolatency_container_issued_io_size_bytes_bucket|Request size; last dispatched size for SCSI, completion bytes for NVMe/virtio|count|Container|
|iolatency_interval_start_timestamp_seconds|Start of the interval|Unix seconds|Host|
|iolatency_interval_end_timestamp_seconds|End of the interval|Unix seconds|Host|

The 17 latency bounds in seconds are `0.00005`, `0.0001`, `0.00025`, `0.0005`,
`0.001`, `0.002`, `0.004`, `0.008`, `0.016`, `0.032`, `0.064`, `0.128`,
`0.256`, `0.512`, `1`, `2`, and `+Inf`.

#### Grafana

The IO overview shows latency distributions and IO counts by collection
interval. Click a bar to view latency and size histograms for that window.
The panels use Business Charts (`volkovlabs-echarts-panel`).

#### Migrating former metrics

Update custom dashboards and alerts after upgrading. The table omits the
`huatuo_bamai_` prefix:

|Former metric|Replacement|
|---|---|
|`iolatency_blkdisk_d2c`|`iolatency_d2c_seconds_bucket`|
|`iolatency_container_blkdisk_d2c`|`iolatency_container_d2c_seconds_bucket`|
|`iolatency_blkdisk_q2c`, `iolatency_container_blkdisk_q2c`|No direct equivalent; select Q2G, Q2D, or D2C for the required stage|
|`iolatency_blkdisk_freeze`|`io_health_blkdisk_freeze`, still a Counter|

`disk` now contains names such as `sda` instead of device numbers such as `8:0`.
The new `operation` label distinguishes reads and writes. Former `zone`
numbers are replaced by cumulative `le` bounds in seconds, listed above;
they cannot be mapped by index. For a range `(a, b]`, subtract the `le=a`
value from the `le=b` value.

Former latency metrics counted only IOs of at least 20 ms. New metrics cover
all read/write IO on supported disks, and D2C starts at the last dispatch.
Adjust alerts for the new stages and bounds rather than only renaming metrics.

Latency buckets change from cumulative Counters to interval Gauges. Do not
apply `rate()` or `increase()` to them. Query directly for one interval;
for example, sum read D2C samples for a disk over the last five minutes:

```promql
sum_over_time(huatuo_bamai_iolatency_d2c_seconds_bucket{disk="sda",operation="read",le="+Inf"}[5m])
```

### Hardware

```bash
# HELP huatuo_bamai_io_health_blkdisk_freeze the disk freeze event count
# TYPE huatuo_bamai_io_health_blkdisk_freeze counter
huatuo_bamai_io_health_blkdisk_freeze{disk="sda",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Scope|Labels|
|---|---|---|---|---|
|io_health_blkdisk_freeze|Host disk freeze count; resets on disk replacement|count|Host|host, region, disk|

Select this metric under `Overview → Selected Metric` on the host dashboard.

### blk-throttle waits

`blk_throtl` counts IOs delayed by blk-throttle and their mean queue wait.
Each IO is counted when it leaves the queue; its wait starts at its first
queue entry.

|Metric|Description|
|---|---|
|`blk_throtl_delayed_io_count`|IOs that finished waiting in the collection interval|
|`blk_throtl_average_wait_ms`|Mean queue wait for those IOs, in milliseconds|
|`blk_throtl_container_delayed_io_count`|Container IOs that finished waiting in the collection interval|
|`blk_throtl_container_average_wait_ms`|Mean container queue wait, in milliseconds|

Metrics distinguish `disk` and `operation` (`read` or `write`). `scope=host`
is the host total; `scope=other` is its portion without container attribution.
Container series use container labels instead of `scope`. All four metrics
are interval Gauges.

Container discovery is optional; host collection does not require kubelet.
Failed lookups or invalid labels send the current data to `other`; recovered
associations do not replay past intervals. After temporary collection failure,
the first successful collection rebuilds the baseline and the next resumes
output, without filling the failed interval. Errors that stop collection
require troubleshooting and a Huatuo restart.

After cgroup release, uncollected tail wait deltas are included in Host and
Other without container attribution. Results can undercount if one series on
one CPU completes at least 67,108,864 waits, or accumulates about 2.749 million
seconds of waiting, between successful collections.

### IOCOST waits

`iocost` measures waits caused by insufficient IOCOST budget. Waiting starts
when the IO enters the wait queue and ends at its final wakeup; the full wait
belongs to the collection interval in which it ends.

|Metric|Description|
|---|---|
|`iocost_waitq_io_count`|Read/write IOs that finished waiting in the interval|
|`iocost_average_wait_ms`|Mean wait for those IOs, in milliseconds|
|`iocost_container_waitq_io_count`|Container read/write IOs that finished waiting in the interval|
|`iocost_container_average_wait_ms`|Mean container IO wait, in milliseconds|

These interval Gauges use `disk` (`sda`, `nvme0n1`, etc.) and `operation` (`read` or
`write`). `scope=host` includes all containers; `scope=other` is the portion
without container attribution.

Container discovery is optional; host collection does not require kubelet.
Failed lookups or invalid labels send the current data to `other`; recovered
associations do not replay past intervals. After temporary collection failure,
the first successful collection rebuilds the baseline and the next resumes
output, without filling the failed interval. Errors that stop collection
require troubleshooting and a Huatuo restart.

After cgroup release, uncollected tail wait deltas are included in Host and
Other without container attribution. Results can undercount if one series on
one CPU completes at least 67,108,864 waits, or accumulates about 2.749 million
seconds of waiting, between successful collections. For example, 10,240 waits
for one series, each lasting 270 seconds and settled on that CPU in the same
interval, exceed the wait-time capacity.

IOCOST requires a non-PREEMPT_RT Linux x86_64 kernel with IOCOST functions,
BPF/BTF, and kprobes.

### Device health

`io_health` records these events:

|Event type|Trigger|
|---|---|
|`block_error`|A normal block request completes with an error|
|`nvme_timeout`|An NVMe request times out|
|`nvme_reset`|An NVMe reset request is accepted, not necessarily completed; event only, without evidence commands|
|`nvme_state_change`|An NVMe controller changes state|
|`scsi_timeout`|A SCSI command times out|
|`scsi_dispatch_error`|SCSI command dispatch fails|
|`md_sync_action`|An MD sync action changes|
|`md_degraded`|The number of unavailable MD members changes|
|`md_member_state`|An MD member changes state or disappears|

If an NVMe state name is unavailable, `new_state` is `unknown` and
`new_state_raw` retains the numeric state.

Block errors, NVMe timeouts, SCSI errors, and MD members entering `faulty`,
`blocked`, `write_error`, or `removed` trigger evidence collection. A uniquely
resolved NVMe target uses read-only `nvme-cli` commands; SCSI uses read-only
`smartctl` commands. Ambiguous targets retain the event without scanning other
devices. MD monitoring does not require `mdadm`.
Evidence commands use the resolved physical disk or NVMe controller; the
event's `device` can retain an MD member's partition name.
Evidence commands for the same device are suppressed while running and for
a 60-second cooldown; raw events remain independent.

The `huatuo_bamai_io_health_` Counters track events since process startup:

|Metric suffix|Labels|Description|
|---|---|---|
|`block_errors_total`|disk, operation, status|Block-layer errors|
|`nvme_timeouts_total`|disk|NVMe timeouts|
|`nvme_resets_total`|disk|Accepted NVMe reset requests|
|`scsi_timeouts_total`|disk|SCSI timeouts|
|`scsi_dispatch_errors_total`|disk, status|SCSI dispatch errors|
|`collection_errors_total`|disk, reason|Evidence collection failures|
|`event_persistence_failures_total`|reason|Event save errors|

MD and NVMe controller state changes are stored as events, without a current
state Gauge. Counters update before events are saved; save failures are
counted by `event_persistence_failures_total{reason="save_error"}`.

Accepted health events are stored as individual documents. Unavailable or
inapplicable optional fields are omitted:

|Field|Description|
|---|---|
|`type`|Event type|
|`device`|Event device name; MD member events may retain a partition name|
|`array`, `member`|MD array and member names|
|`old_state`, `new_state`|MD state transition; NVMe provides only the new state|
|`new_state_raw`|Numeric NVMe controller state|
|`operation`, `io_error_status`, `sector`|Block operation, error, and starting sector|
|`collection_status`|`ok`, `partial`, `unsupported`, `timeout`, or `error`|
|`nvme`, `scsi`|Protocol health evidence collected after the event|

NVMe evidence includes `critical_warning`, `media_errors_total`, and up to
eight error-log entries with status code, NSID, and LBA. SCSI evidence includes
overall health, ASC/ASCQ, temperature, defect counts, read/write/verify error
counts, and pending-defect counts with up to eight sample LBAs.

### Disk Statistics

`diskio` reads `/proc/diskstats` and `/proc/stat` for per-device request counts, throughput, cumulative durations, and host CPU iowait.

Its disk-name label changes from `device` to `disk`; update selectors and
legends in custom queries. Metric names and types are unchanged.

The repository configuration disables `diskio` through `BlackList` by default. Remove it and restart `huatuo-bamai` to enable it.

Request counts, bytes, and I/O durations are cumulative Counters. Use `rate()` to calculate IOPS, throughput, and duration growth rates. Mean request duration is the duration growth rate divided by request rate; exclude intervals with zero requests. `io_in_progress` and `disk_iowait_percent` are Gauges.

| Metric | Description | Unit | Scope | Additional Labels |
| --- | --- | --- | --- | --- |
| `diskio_read_requests_total` | Cumulative completed read requests; use `rate()` for read IOPS | Count | Host | disk |
| `diskio_write_requests_total` | Cumulative completed write requests; use `rate()` for write IOPS | Count | Host | disk |
| `diskio_read_bytes_total` | Cumulative bytes read; use `rate()` for read throughput | Bytes | Host | disk |
| `diskio_written_bytes_total` | Cumulative bytes written; use `rate()` for write throughput | Bytes | Host | disk |
| `diskio_read_time_seconds_total` | Cumulative time spent on completed reads | Seconds | Host | disk |
| `diskio_write_time_seconds_total` | Cumulative time spent on completed writes | Seconds | Host | disk |
| `diskio_io_in_progress` | Current in-flight I/O requests (queue depth) | Count | Host | disk |
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

### add container reclaim and compaction stall times

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|memory_free_container_allocpages_stall|Container task time in global direct reclaim| milliseconds|Container| eBPF | container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_free_container_compaction_stall|Container task time in direct compaction| milliseconds|Container| eBPF | same container labels|

Host values already used milliseconds; only the documented unit changes, not names, scaling or Gauge types. Container stalls record the full kernfs ID of the task's effective memory cgroup at operation entry and match it against existing cgroup v1/v2 discovery. The ID includes the cgroup generation, preventing attribution to a different cgroup after CSS address reuse. Replacing a memory CSS without recreating its cgroup directory preserves that cgroup's identity and cumulative values. They measure time suffered by tasks in global reclaim/compaction, not memory reclaimed for that cgroup or memcg limit reclaim counts. Host totals include unassigned tasks; do not add host and container values.

Only BPF records matching discovered normal containers are exported; absent records are not zero-filled. Container discovery/map failures retain host values and report an error. ID resolution ignores containers that disappear during collection (ENOENT); other resolution failures are reported while retaining host values and successfully matched container values. Timing and container aggregate LRU maps are each bounded to 10240 entries: pressure can lose timings or evict aggregates, and BPF reload resets values. Handle resets when computing deltas. Container collection uses only the existing reclaim tracepoints and compaction kprobes; kernfs-ID matching needs no `cgroup_mkdir` cleanup probe. Values accumulate during collection, not necessarily from host boot.

If loading or attaching the full BPF object fails at startup, Huatuo cleans it up and retries once in host-only mode: the BPF constant `enable_container_stalls` is set to 0 to disable container cgroup reads and accounting, using the same probes. The original two host stall metrics remain available with a fallback warning; container zeros are not fabricated and default filters are unchanged. Host-only mode still requires BPF/BTF, reclaim tracepoints and compaction kprobes; it cannot bypass these prerequisites. Failure of the host-only retry is reported as a startup error.

> **Note**: The `memory_others_container_directstall_time`, `memory_others_container_asyncreclaim_time`, and `memory_others_container_local_direct_reclaim_time` metrics read memory cgroup extension interfaces provided by a vendor-custom kernel (`memory.directstall_stat`, `memory.asynreclaim_stat`, `memory.local_direct_reclaim_time`). Mainline and common distribution kernels do not expose these interfaces, so these metrics are simply not emitted there — this is expected, and no extra kernel module can provide them. To observe container direct reclaim behavior on standard kernels, use the eBPF-based `memory_reclaim_container_directstall` listed above.

### add a host-wide memcg reclaim count

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|memory_reclaim_directstall|Cumulative host-wide memcg limit reclaim events| count| Host| eBPF | host, region|

`memory_reclaim_directstall` shares `mm_vmscan_memcg_reclaim_begin` and the kswapd exclusion with the container count. A per-CPU host count is incremented before CSS attribution and summed at collection. It includes cgroups not discovered as normal containers, rather than summing exported container metrics. It is neither a global direct reclaim count nor a duration; without memcg reclaim events it remains zero. Container deletion and container map capacity do not affect the host count; BPF reload resets it. The Gauge type, existing hook requirements and default filters are unchanged, with no additional probes. Host and container collection failures are isolated; failed reads do not emit fabricated zeros.

### attribute hungtask events and counts to containers

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|hungtask_container_total|Events attributed to blocked container tasks|count|Container|BPF + cgroup file handles|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

Container attribution snapshots the blocked task's cgroup identity at the `sched_process_hang` raw tracepoint, rather than using the detecting `khungtaskd`. cgroup v1/Hybrid uses the CPU hierarchy; v2 uses the unified hierarchy. Up to 16 non-root ancestors carry full kernfs IDs including generations and are matched against discovered normal containers' cgroup file handles, choosing the nearest matching container. This supports descendant cgroups without a later `/proc/<tid>/cgroup` lookup, so task exit, TID reuse and subsequent migration do not change the recorded identity.

Missing metadata, unreadable handles, failed identity reads or an unmatched container beyond the 16-level limit leave events in the host total only. If raw tracepoint loading or attachment fails, the collector retries the original tracepoint with host-only counting and a warning; it does not fall back to TID lookup. The host path still requires the original BPF/BTF and hungtask tracepoint support. Event-time identity does not guarantee successful container discovery.

Both metrics are Counters updated before trace backoff. Tracing keeps host-wide backoff and system-wide stack snapshots; matched records carry `ContainerID`, without filtering stacks to that container. Container counts last for the observed container lifetime, are removed after successful discovery confirms exit, and reset on agent restart. Discovery failure retains the host metric and cached container counts. Do not add host and container counts; repeated reports for the same blocked task are separate events.
