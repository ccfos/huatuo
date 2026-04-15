---
title: Metrics
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

Metrics supported in the current version:

## CPU

### Scheduling

The following metrics allow observation of process scheduling latency, i.e., the time from when a process becomes runnable (placed in the run queue) until it actually starts executing on the CPU.

```bash
# HELP huatuo_bamai_runqlat_container_latency cpu run queue latency for the containers
# TYPE huatuo_bamai_runqlat_container_latency gauge
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="0"} 226
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="1"} 0
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="2"} 0
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="3"} 0

# HELP huatuo_bamai_runqlat_latency cpu run queue latency for the host
# TYPE huatuo_bamai_runqlat_latency gauge
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="0"} 35100
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="1"} 0
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="2"} 0
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="3"} 0
```

|Metric|Description|Unit|Target|Source| Labels|
|---|---|---|---|---|---|
|runqlat_container_latency|scheduling latency histogram buckets: <br>zone0: 0–10 ms<br>zone1: 10–20 ms<br>zone2: 20–50 ms<br>zone3: 50+ ms|count|Container| eBPF |container_host, container_hostnamespace, container_level, container_name, container_type, host, region, zone |
|runqlat_latency|scheduling latency histogram buckets:<br>zone0, 0~10ms<br>zone1, 10-20ms <br>zone2, 20-50ms <br>zone3, 50+ms |count|Host| eBPF | host, region, zone|

### SoftIRQ

SoftIRQ response latency on different CPUs (currently only NET_RX and NET_TX are collected).

```bash
# HELP huatuo_bamai_softirq_latency softirq latency
# TYPE huatuo_bamai_softirq_latency gauge
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_RX",zone="0"} 125
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_RX",zone="1"} 2
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_RX",zone="2"} 0
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_RX",zone="3"} 0
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_TX",zone="0"} 0
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_TX",zone="1"} 0
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_TX",zone="2"} 0
huatuo_bamai_softirq_latency{cpuid="0",host="hostname",region="dev",type="NET_TX",zone="3"} 0
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_RX",zone="0"} 110
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_RX",zone="1"} 0
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_RX",zone="2"} 1
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_RX",zone="3"} 0
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_TX",zone="0"} 0
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_TX",zone="1"} 0
huatuo_bamai_softirq_latency{cpuid="1",host="hostname",region="dev",type="NET_TX",zone="2"} 0
```

|Metric|Description|Unit|Target|Source| Labels|
|---|---|---|---|---|---|
|softirq_latency|SoftIRQ response latency histogram buckets:<br>zone0, 0-10us<br>zone1, 10-100us<br>zone2, 100-1000us<br>zone3, 1+ms |count|Host| eBPF |cpuid, host, region, type, zone|


### Utilization

Metrics showing CPU usage on hosts and containers (Prometheus format):

```bash
# HELP huatuo_bamai_cpu_util_sys cpu sys for the host
# TYPE huatuo_bamai_cpu_util_sys gauge
huatuo_bamai_cpu_util_sys{host="hostname",region="dev"} 6.268857848549965e-06
# HELP huatuo_bamai_cpu_util_total cpu total for the host
# TYPE huatuo_bamai_cpu_util_total gauge
huatuo_bamai_cpu_util_total{host="hostname",region="dev"} 1.7736934944144352e-05
# HELP huatuo_bamai_cpu_util_usr cpu usr for the host
# TYPE huatuo_bamai_cpu_util_usr gauge
huatuo_bamai_cpu_util_usr{host="hostname",region="dev"} 1.1468077095594387e-05

# HELP huatuo_bamai_cpu_util_container_sys cpu sys for the containers
# TYPE huatuo_bamai_cpu_util_container_sys gauge
huatuo_bamai_cpu_util_container_sys{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1.6708593420881415e-07
# HELP huatuo_bamai_cpu_util_container_total cpu total for the containers
# TYPE huatuo_bamai_cpu_util_container_total gauge
huatuo_bamai_cpu_util_container_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 3.379584661890774e-07
# HELP huatuo_bamai_cpu_util_container_usr cpu usr for the containers
# TYPE huatuo_bamai_cpu_util_container_usr gauge
huatuo_bamai_cpu_util_container_usr{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1.7087253017325962e-07
```

|Metric|Description|Unit|Target| Labels|
|---|---|---|---|---|
|cpu_util_sys| CPU system (kernel) time %|%| Host | host, region |
|cpu_util_usr| CPU user time %|%| Host | host, region |
|cpu_util_total| CPU total utilization % |%| Host | host, region |
|cpu_util_container_sys| Container CPU system time %|%|Container|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |
|cpu_util_container_usr| Container CPU user time %|%|Container|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |
|cpu_util_container_total| Container CPU total %|%|Container|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |

### Allocation

Container CPU resource configuration:

```bash
# HELP huatuo_bamai_cpu_util_container_cores cpu core number for the containers
# TYPE huatuo_bamai_cpu_util_container_cores gauge
huatuo_bamai_cpu_util_container_cores{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="Burstable",container_name="coredns",container_type="Normal",host="hostname",region="dev"} 6
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|cpu_util_container_cores| Number of CPU cores|cores| Container | (same as above) |

### Contention

Metrics reflecting container throttling and contention:

```bash
# HELP huatuo_bamai_cpu_stat_container_nr_throttled throttle nr for the containers
# TYPE huatuo_bamai_cpu_stat_container_nr_throttled gauge
huatuo_bamai_cpu_stat_container_nr_throttled{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_throttled_time throttle time for the containers
# TYPE huatuo_bamai_cpu_stat_container_throttled_time gauge
huatuo_bamai_cpu_stat_container_throttled_time{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|cpu_stat_container_nr_throttled| Number of times the cgroup was throttled |count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|cpu_stat_container_throttled_time| Total time the cgroup was throttled|nanoseconds|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

Ref:
- https://docs.kernel.org/scheduler/sched-bwc.html#statistics
- https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html#cpu-interface-files

Future metrics (Didi kernel extensions – not yet public):

```bash
# HELP huatuo_bamai_cpu_stat_container_wait_rate wait rate for the containers
# TYPE huatuo_bamai_cpu_stat_container_wait_rate gauge
huatuo_bamai_cpu_stat_container_wait_rate{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_throttle_wait_rate throttle wait rate for the containers
# TYPE huatuo_bamai_cpu_stat_container_throttle_wait_rate gauge
huatuo_bamai_cpu_stat_container_throttle_wait_rate{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_inner_wait_rate inner wait rate for the containers
# TYPE huatuo_bamai_cpu_stat_container_inner_wait_rate gauge
huatuo_bamai_cpu_stat_container_inner_wait_rate{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_exter_wait_rate exter wait rate for the containers
# TYPE huatuo_bamai_cpu_stat_container_exter_wait_rate gauge
huatuo_bamai_cpu_stat_container_exter_wait_rate{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

### Burst Behavior

Metrics showing burst usage beyond quota:

```bash
# HELP huatuo_bamai_cpu_stat_container_nr_bursts burst nr for the containers
# TYPE huatuo_bamai_cpu_stat_container_nr_bursts gauge
huatuo_bamai_cpu_stat_container_nr_bursts{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
huatuo_bamai_cpu_stat_container_nr_bursts{container_host="coredns-855c4dd65d-mnpqf",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_burst_time burst time for the containers
# TYPE huatuo_bamai_cpu_stat_container_burst_time gauge
huatuo_bamai_cpu_stat_container_burst_time{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
huatuo_bamai_cpu_stat_container_burst_time{container_host="coredns-855c4dd65d-mnpqf",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|cpu_stat_container_burst_time|Cumulative wall-clock time spent above quota across all periods|count|Container|container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|cpu_stat_container_nr_bursts|Number of periods in which usage exceeded quota|count|Container|container_host, container_hostnamespace, container_level, container_name, container_type, host, region |

### Load

Load average and runnable/uninterruptible task counts:
```bash
# HELP huatuo_bamai_loadavg_load1 system load average, 1 minute
# TYPE huatuo_bamai_loadavg_load1 gauge
huatuo_bamai_loadavg_load1{host="hostname",region="dev"} 0.3
# HELP huatuo_bamai_loadavg_load15 system load average, 15 minutes
# TYPE huatuo_bamai_loadavg_load15 gauge
huatuo_bamai_loadavg_load15{host="hostname",region="dev"} 0.22
# HELP huatuo_bamai_loadavg_load5 system load average, 5 minutes
# TYPE huatuo_bamai_loadavg_load5 gauge
huatuo_bamai_loadavg_load5{host="hostname",region="dev"} 0.2
# HELP huatuo_bamai_loadavg_container_nr_running nr_running of container
# TYPE huatuo_bamai_loadavg_container_nr_running gauge
huatuo_bamai_loadavg_container_nr_running{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_loadavg_container_nr_uninterruptible nr_uninterruptible of container
# TYPE huatuo_bamai_loadavg_container_nr_uninterruptible gauge
huatuo_bamai_loadavg_container_nr_uninterruptible{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|---|
|loadavg_load1|1-minute system load average|count|Host| host, region ||
|loadavg_load5|5-minute system load average|count|Host| host, region ||
|loadavg_load15|15-minute system load average|count|Host| host, region ||
|loadavg_container_container_nr_running|Number of running tasks in container|count|Container| host, region |cgroup v1 only|
|loadavg_container_container_nr_uninterruptible|Number of uninterruptible tasks in container|count|Container| host, region |cgroup v1 only|

## Memory System

### Reclaim

Metrics showing time spent stalled due to memory reclaim/compaction:

```bash
# HELP huatuo_bamai_memory_free_allocpages_stall time stalled in alloc pages
# TYPE huatuo_bamai_memory_free_allocpages_stall gauge
huatuo_bamai_memory_free_allocpages_stall{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_free_compaction_stall time stalled in memory compaction
# TYPE huatuo_bamai_memory_free_compaction_stall gauge
huatuo_bamai_memory_free_compaction_stall{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_reclaim_container_directstall counter of cgroup reclaim when try_charge
# TYPE huatuo_bamai_memory_reclaim_container_directstall gauge
huatuo_bamai_memory_reclaim_container_directstall{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|memory_free_allocpages_stall|Time stalled waiting for page allocation| nanoseconds|Host| eBPF | host, region|
|memory_free_compaction_stall|Time stalled in memory compaction| nanoseconds|Host| eBPF | host, region|
|memory_reclaim_container_directstall|Number of direct reclaim events in container| count| Container| eBPF | container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### State

From cgroup memory.stat:

```bash
# HELP huatuo_bamai_memory_vmstat_container_active_anon cgroup memory.stat active_anon
# TYPE huatuo_bamai_memory_vmstat_container_active_anon gauge
huatuo_bamai_memory_vmstat_container_active_anon{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1.47456e+07
# HELP huatuo_bamai_memory_vmstat_container_active_file cgroup memory.stat active_file
# TYPE huatuo_bamai_memory_vmstat_container_active_file gauge
huatuo_bamai_memory_vmstat_container_active_file{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 2.3617536e+07
# HELP huatuo_bamai_memory_vmstat_container_file_dirty cgroup memory.stat file_dirty
# TYPE huatuo_bamai_memory_vmstat_container_file_dirty gauge
huatuo_bamai_memory_vmstat_container_file_dirty{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_file_writeback cgroup memory.stat file_writeback
# TYPE huatuo_bamai_memory_vmstat_container_file_writeback gauge
huatuo_bamai_memory_vmstat_container_file_writeback{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_inactive_anon cgroup memory.stat inactive_anon
# TYPE huatuo_bamai_memory_vmstat_container_inactive_anon gauge
huatuo_bamai_memory_vmstat_container_inactive_anon{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_inactive_file cgroup memory.stat inactive_file
# TYPE huatuo_bamai_memory_vmstat_container_inactive_file gauge
huatuo_bamai_memory_vmstat_container_inactive_file{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 65536
# HELP huatuo_bamai_memory_vmstat_container_pgdeactivate cgroup memory.stat pgdeactivate
# TYPE huatuo_bamai_memory_vmstat_container_pgdeactivate gauge
huatuo_bamai_memory_vmstat_container_pgdeactivate{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_pgrefill cgroup memory.stat pgrefill
# TYPE huatuo_bamai_memory_vmstat_container_pgrefill gauge
huatuo_bamai_memory_vmstat_container_pgrefill{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_pgscan_direct cgroup memory.stat pgscan_direct
# TYPE huatuo_bamai_memory_vmstat_container_pgscan_direct gauge
huatuo_bamai_memory_vmstat_container_pgscan_direct{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_pgscan_kswapd cgroup memory.stat pgscan_kswapd
# TYPE huatuo_bamai_memory_vmstat_container_pgscan_kswapd gauge
huatuo_bamai_memory_vmstat_container_pgscan_kswapd{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_pgsteal_direct cgroup memory.stat pgsteal_direct
# TYPE huatuo_bamai_memory_vmstat_container_pgsteal_direct gauge
huatuo_bamai_memory_vmstat_container_pgsteal_direct{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_pgsteal_kswapd cgroup memory.stat pgsteal_kswapd
# TYPE huatuo_bamai_memory_vmstat_container_pgsteal_kswapd gauge
huatuo_bamai_memory_vmstat_container_pgsteal_kswapd{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_shmem cgroup memory.stat shmem
# TYPE huatuo_bamai_memory_vmstat_container_shmem gauge
huatuo_bamai_memory_vmstat_container_shmem{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_shmem_thp cgroup memory.stat shmem_thp
# TYPE huatuo_bamai_memory_vmstat_container_shmem_thp gauge
huatuo_bamai_memory_vmstat_container_shmem_thp{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_container_unevictable cgroup memory.stat unevictable
# TYPE huatuo_bamai_memory_vmstat_container_unevictable gauge
huatuo_bamai_memory_vmstat_container_unevictable{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|memory_vmstat_container_active_file|Active file-backed memory|Bytes | Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_active_anon|Active anonymous memory|Bytes | Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_inactive_file|Inactive file-backed memory|Bytes | Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_inactive_anon|Inactive anonymous memory|Bytes | Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_file_dirty|Dirty file pages not yet written back|Bytes |Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_file_writeback|File pages currently being written back|Bytes |Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_unevictable|Unevictable pages (mlocked, hugetlbfs, etc.)|Bytes|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|... (pgscan_direct, pgsteal_kswapd, etc.)|Standard vmstat reclaim / scanning counters|Bytes |Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|


Host memory state.

```bash
# HELP huatuo_bamai_memory_vmstat_allocstall_device /proc/vmstat allocstall_device
# TYPE huatuo_bamai_memory_vmstat_allocstall_device gauge
huatuo_bamai_memory_vmstat_allocstall_device{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_allocstall_dma /proc/vmstat allocstall_dma
# TYPE huatuo_bamai_memory_vmstat_allocstall_dma gauge
huatuo_bamai_memory_vmstat_allocstall_dma{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_allocstall_dma32 /proc/vmstat allocstall_dma32
# TYPE huatuo_bamai_memory_vmstat_allocstall_dma32 gauge
huatuo_bamai_memory_vmstat_allocstall_dma32{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_allocstall_movable /proc/vmstat allocstall_movable
# TYPE huatuo_bamai_memory_vmstat_allocstall_movable gauge
huatuo_bamai_memory_vmstat_allocstall_movable{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_allocstall_normal /proc/vmstat allocstall_normal
# TYPE huatuo_bamai_memory_vmstat_allocstall_normal gauge
huatuo_bamai_memory_vmstat_allocstall_normal{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_nr_active_anon /proc/vmstat nr_active_anon
# TYPE huatuo_bamai_memory_vmstat_nr_active_anon gauge
huatuo_bamai_memory_vmstat_nr_active_anon{host="hostname",region="dev"} 155449
# HELP huatuo_bamai_memory_vmstat_nr_active_file /proc/vmstat nr_active_file
# TYPE huatuo_bamai_memory_vmstat_nr_active_file gauge
huatuo_bamai_memory_vmstat_nr_active_file{host="hostname",region="dev"} 212425
# HELP huatuo_bamai_memory_vmstat_nr_dirty /proc/vmstat nr_dirty
# TYPE huatuo_bamai_memory_vmstat_nr_dirty gauge
huatuo_bamai_memory_vmstat_nr_dirty{host="hostname",region="dev"} 19047
# HELP huatuo_bamai_memory_vmstat_nr_dirty_background_threshold /proc/vmstat nr_dirty_background_threshold
# TYPE huatuo_bamai_memory_vmstat_nr_dirty_background_threshold gauge
huatuo_bamai_memory_vmstat_nr_dirty_background_threshold{host="hostname",region="dev"} 379858
# HELP huatuo_bamai_memory_vmstat_nr_dirty_threshold /proc/vmstat nr_dirty_threshold
# TYPE huatuo_bamai_memory_vmstat_nr_dirty_threshold gauge
huatuo_bamai_memory_vmstat_nr_dirty_threshold{host="hostname",region="dev"} 760646
# HELP huatuo_bamai_memory_vmstat_nr_free_pages /proc/vmstat nr_free_pages
# TYPE huatuo_bamai_memory_vmstat_nr_free_pages gauge
huatuo_bamai_memory_vmstat_nr_free_pages{host="hostname",region="dev"} 3.20535e+06
# HELP huatuo_bamai_memory_vmstat_nr_inactive_anon /proc/vmstat nr_inactive_anon
# TYPE huatuo_bamai_memory_vmstat_nr_inactive_anon gauge
huatuo_bamai_memory_vmstat_nr_inactive_anon{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_nr_inactive_file /proc/vmstat nr_inactive_file
# TYPE huatuo_bamai_memory_vmstat_nr_inactive_file gauge
huatuo_bamai_memory_vmstat_nr_inactive_file{host="hostname",region="dev"} 428518
# HELP huatuo_bamai_memory_vmstat_nr_mlock /proc/vmstat nr_mlock
# TYPE huatuo_bamai_memory_vmstat_nr_mlock gauge
huatuo_bamai_memory_vmstat_nr_mlock{host="hostname",region="dev"} 6821
# HELP huatuo_bamai_memory_vmstat_nr_shmem /proc/vmstat nr_shmem
# TYPE huatuo_bamai_memory_vmstat_nr_shmem gauge
huatuo_bamai_memory_vmstat_nr_shmem{host="hostname",region="dev"} 541
# HELP huatuo_bamai_memory_vmstat_nr_shmem_hugepages /proc/vmstat nr_shmem_hugepages
# TYPE huatuo_bamai_memory_vmstat_nr_shmem_hugepages gauge
huatuo_bamai_memory_vmstat_nr_shmem_hugepages{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_nr_shmem_pmdmapped /proc/vmstat nr_shmem_pmdmapped
# TYPE huatuo_bamai_memory_vmstat_nr_shmem_pmdmapped gauge
huatuo_bamai_memory_vmstat_nr_shmem_pmdmapped{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_nr_slab_reclaimable /proc/vmstat nr_slab_reclaimable
# TYPE huatuo_bamai_memory_vmstat_nr_slab_reclaimable gauge
huatuo_bamai_memory_vmstat_nr_slab_reclaimable{host="hostname",region="dev"} 22322
# HELP huatuo_bamai_memory_vmstat_nr_slab_unreclaimable /proc/vmstat nr_slab_unreclaimable
# TYPE huatuo_bamai_memory_vmstat_nr_slab_unreclaimable gauge
huatuo_bamai_memory_vmstat_nr_slab_unreclaimable{host="hostname",region="dev"} 24168
# HELP huatuo_bamai_memory_vmstat_nr_unevictable /proc/vmstat nr_unevictable
# TYPE huatuo_bamai_memory_vmstat_nr_unevictable gauge
huatuo_bamai_memory_vmstat_nr_unevictable{host="hostname",region="dev"} 6839
# HELP huatuo_bamai_memory_vmstat_nr_writeback /proc/vmstat nr_writeback
# TYPE huatuo_bamai_memory_vmstat_nr_writeback gauge
huatuo_bamai_memory_vmstat_nr_writeback{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_nr_writeback_temp /proc/vmstat nr_writeback_temp
# TYPE huatuo_bamai_memory_vmstat_nr_writeback_temp gauge
huatuo_bamai_memory_vmstat_nr_writeback_temp{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_numa_pages_migrated /proc/vmstat numa_pages_migrated
# TYPE huatuo_bamai_memory_vmstat_numa_pages_migrated gauge
huatuo_bamai_memory_vmstat_numa_pages_migrated{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgdeactivate /proc/vmstat pgdeactivate
# TYPE huatuo_bamai_memory_vmstat_pgdeactivate gauge
huatuo_bamai_memory_vmstat_pgdeactivate{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgrefill /proc/vmstat pgrefill
# TYPE huatuo_bamai_memory_vmstat_pgrefill gauge
huatuo_bamai_memory_vmstat_pgrefill{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgscan_direct /proc/vmstat pgscan_direct
# TYPE huatuo_bamai_memory_vmstat_pgscan_direct gauge
huatuo_bamai_memory_vmstat_pgscan_direct{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgscan_direct_throttle /proc/vmstat pgscan_direct_throttle
# TYPE huatuo_bamai_memory_vmstat_pgscan_direct_throttle gauge
huatuo_bamai_memory_vmstat_pgscan_direct_throttle{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgscan_kswapd /proc/vmstat pgscan_kswapd
# TYPE huatuo_bamai_memory_vmstat_pgscan_kswapd gauge
huatuo_bamai_memory_vmstat_pgscan_kswapd{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgsteal_direct /proc/vmstat pgsteal_direct
# TYPE huatuo_bamai_memory_vmstat_pgsteal_direct gauge
huatuo_bamai_memory_vmstat_pgsteal_direct{host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_vmstat_pgsteal_kswapd /proc/vmstat pgsteal_kswapd
# TYPE huatuo_bamai_memory_vmstat_pgsteal_kswapd gauge
huatuo_bamai_memory_vmstat_pgsteal_kswapd{host="hostname",region="dev"} 0
```

Standard kernel vmstat counters (see kernel documentation for full details):

- nr_free_pages: total free pages in buddy allocator
- nr_active_anon / nr_inactive_anon: active / inactive anonymous pages
- nr_active_file / nr_inactive_file: active / inactive file pages
- nr_dirty / nr_writeback: dirty / under writeback pages
- nr_dirty_threshold / nr_dirty_background_threshold: dirty page writeback thresholds
- pgscan_kswapd / pgsteal_kswapd / ... : reclaim & scanning statistics
- allocstall_*: stalls due to allocation failure in different zones
- numa_hit / numa_miss / numa_foreign / numa_local / numa_other: NUMA allocation statistics

Ref:
- https://docs.kernel.org/admin-guide/cgroup-v2.html
- https://docs.kernel.org/admin-guide/cgroup-v1/memory.html
- https://docs.kernel.org/admin-guide/mm/transhuge.html

### Events

From memory.events:

```bash
# HELP huatuo_bamai_memory_events_container_high memory events high
# TYPE huatuo_bamai_memory_events_container_high gauge
huatuo_bamai_memory_events_container_high{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_events_container_low memory events low
# TYPE huatuo_bamai_memory_events_container_low gauge
huatuo_bamai_memory_events_container_low{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_events_container_max memory events max
# TYPE huatuo_bamai_memory_events_container_max gauge
huatuo_bamai_memory_events_container_max{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_events_container_oom memory events oom
# TYPE huatuo_bamai_memory_events_container_oom gauge
huatuo_bamai_memory_events_container_oom{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_events_container_oom_group_kill memory events oom_group_kill
# TYPE huatuo_bamai_memory_events_container_oom_group_kill gauge
huatuo_bamai_memory_events_container_oom_group_kill{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_memory_events_container_oom_kill memory events oom_kill
# TYPE huatuo_bamai_memory_events_container_oom_kill gauge
huatuo_bamai_memory_events_container_oom_kill{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|memory_events_container_low|Pages reclaimed below memory.low due to system pressure|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_high|Times usage exceeded memory.high (throttling / direct reclaim triggered)|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_max|Times approaching or hitting memory.max|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom|Times OOM path entered due to memory.max|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom_kill|Number of processes killed by OOM killer in cgroup|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom_group_kill|Number of times entire cgroup killed by OOM|count|Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### Buddyinfo

Free page block distribution per node/zone/order (from /proc/buddyinfo):

```bash
# HELP huatuo_bamai_memory_buddyinfo_blocks buddy info
# TYPE huatuo_bamai_memory_buddyinfo_blocks gauge
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="0",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="0",region="dev",zone="DMA32"} 3
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="0",region="dev",zone="Normal"} 7
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="1",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="1",region="dev",zone="DMA32"} 1
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="1",region="dev",zone="Normal"} 36
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="10",region="dev",zone="DMA"} 2
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="10",region="dev",zone="DMA32"} 743
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="10",region="dev",zone="Normal"} 2265
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="2",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="2",region="dev",zone="DMA32"} 3
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="2",region="dev",zone="Normal"} 10
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="3",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="3",region="dev",zone="DMA32"} 2
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="3",region="dev",zone="Normal"} 224
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="4",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="4",region="dev",zone="DMA32"} 1
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="4",region="dev",zone="Normal"} 376
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="5",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="5",region="dev",zone="DMA32"} 1
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="5",region="dev",zone="Normal"} 165
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="6",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="6",region="dev",zone="DMA32"} 3
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="6",region="dev",zone="Normal"} 118
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="7",region="dev",zone="DMA"} 0
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="7",region="dev",zone="DMA32"} 4
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="7",region="dev",zone="Normal"} 172
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="8",region="dev",zone="DMA"} 1
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="8",region="dev",zone="DMA32"} 4
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="8",region="dev",zone="Normal"} 35
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="9",region="dev",zone="DMA"} 2
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="9",region="dev",zone="DMA32"} 4
huatuo_bamai_memory_buddyinfo_blocks{host="hostname",node="0",order="9",region="dev",zone="Normal"} 25
```

|Metric|Description|Unit|Target|Labels|
|---|---|---|---|---|
|memory_buddyinfo_blocks| Shows number of free blocks of each order (2^order pages) in each zone. |count|Host| procfs | host, node, order, region, zone |


## Network

### ARP

```bash
# HELP huatuo_bamai_arp_container_entries arp entries in container netns
# TYPE huatuo_bamai_arp_container_entries gauge
huatuo_bamai_arp_container_entries{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_arp_entries host init namespace
# TYPE huatuo_bamai_arp_entries gauge
huatuo_bamai_arp_entries{host="hostname",region="dev"} 5
# HELP huatuo_bamai_arp_total all entries in arp_cache for containers and host netns
# TYPE huatuo_bamai_arp_total gauge
huatuo_bamai_arp_total{host="hostname",region="dev"} 12
```

|Metric|Description|Unit|Scope| Labels |
|---|---|---|---|---|
|arp_entries| Number of ARP entries in the host's network namespace|count|Host namespace|host, region|
|arp_total| Total number of ARP entries across all network namespaces on the host|count|Host|host, region|
|arp_container_entries| Number of ARP entries in the container's network namespace |count|Container|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### Qdisc

Qdisc (Queueing Discipline) is a key module in the Linux kernel networking subsystem. Monitoring this module provides clear visibility into network packet processing and latency behavior.

```bash
# HELP huatuo_bamai_netdev_qdisc_backlog Number of bytes currently in queue to be sent.
# TYPE huatuo_bamai_netdev_qdisc_backlog gauge
huatuo_bamai_netdev_qdisc_backlog{device="ens2",host="hostname",kind="fq_codel",region="dev"} 0
# HELP huatuo_bamai_netdev_qdisc_bytes_total Number of bytes sent.
# TYPE huatuo_bamai_netdev_qdisc_bytes_total counter
huatuo_bamai_netdev_qdisc_bytes_total{device="ens2",host="hostname",kind="fq_codel",region="dev"} 2.578235443e+09
# HELP huatuo_bamai_netdev_qdisc_current_queue_length Number of packets currently in queue to be sent.
# TYPE huatuo_bamai_netdev_qdisc_current_queue_length gauge
huatuo_bamai_netdev_qdisc_current_queue_length{device="ens2",host="hostname",kind="fq_codel",region="dev"} 0
# HELP huatuo_bamai_netdev_qdisc_drops_total Number of packet drops.
# TYPE huatuo_bamai_netdev_qdisc_drops_total counter
huatuo_bamai_netdev_qdisc_drops_total{device="ens2",host="hostname",kind="fq_codel",region="dev"} 0
# HELP huatuo_bamai_netdev_qdisc_overlimits_total Number of packet overlimits.
# TYPE huatuo_bamai_netdev_qdisc_overlimits_total counter
huatuo_bamai_netdev_qdisc_overlimits_total{device="ens2",host="hostname",kind="fq_codel",region="dev"} 0
# HELP huatuo_bamai_netdev_qdisc_packets_total Number of packets sent.
# TYPE huatuo_bamai_netdev_qdisc_packets_total counter
huatuo_bamai_netdev_qdisc_packets_total{device="ens2",host="hostname",kind="fq_codel",region="dev"} 6.867714e+06
# HELP huatuo_bamai_netdev_qdisc_requeues_total Number of packets dequeued, not transmitted, and requeued.
# TYPE huatuo_bamai_netdev_qdisc_requeues_total counter
huatuo_bamai_netdev_qdisc_requeues_total{device="ens2",host="hostname",kind="fq_codel",region="dev"} 0
```

|Metric|Description|Unit|Scope| Labels |
|---|---|---|---|---|
|qdisc_backlog|Bytes of packets currently queued for transmission (backlog)|Bytes|Host| device, host, kind, region |
|qdisc_current_queue_length|Number of packets currently queued|count|Host| device, host, kind, region |
|qdisc_overlimits_total|Total number of times the queue limit was exceeded|count|Host| device, host, kind, region |
|qdisc_requeues_total|Number of times packets were requeued due to temporary inability of the NIC/driver to transmit|count|Host| device, host, kind, region |
|qdisc_drops_total|Total number of packets actively dropped|count|Host| device, host, kind, region |
|qdisc_bytes_total|Total bytes transmitted|Bytes|Host| device, host, kind, region |
|qdisc_packets_total|Total number of packets transmitted|count|Host| device, host, kind, region |

### Hardware

This metric tracks packets dropped by the network interface card (NIC) hardware in the receive (RX) path, typically due to buffer overflow, CRC errors, or other hardware-level issues.

```bash
# HELP huatuo_bamai_netdev_hw_rx_dropped count of packets dropped at hardware level
# TYPE huatuo_bamai_netdev_hw_rx_dropped gauge
huatuo_bamai_netdev_hw_rx_dropped{device="eth0",driver="mlx5_core",host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Scope| Labels |
|---|---|---|---|---|
|netdev_hw_rx_dropped|Number of packets dropped by NIC hardware in the receive direction|count|Host|eBPF| device, driver, host, region |


### Netdev

```bash
# HELP huatuo_bamai_netdev_container_receive_bytes_total Network device statistic receive_bytes.
# TYPE huatuo_bamai_netdev_container_receive_bytes_total counter
huatuo_bamai_netdev_container_receive_bytes_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 6.4400018e+07
# HELP huatuo_bamai_netdev_container_receive_compressed_total Network device statistic receive_compressed.
# TYPE huatuo_bamai_netdev_container_receive_compressed_total counter
huatuo_bamai_netdev_container_receive_compressed_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_dropped_total Network device statistic receive_dropped.
# TYPE huatuo_bamai_netdev_container_receive_dropped_total counter
huatuo_bamai_netdev_container_receive_dropped_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_errors_total Network device statistic receive_errors.
# TYPE huatuo_bamai_netdev_container_receive_errors_total counter
huatuo_bamai_netdev_container_receive_errors_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_fifo_total Network device statistic receive_fifo.
# TYPE huatuo_bamai_netdev_container_receive_fifo_total counter
huatuo_bamai_netdev_container_receive_fifo_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_frame_total Network device statistic receive_frame.
# TYPE huatuo_bamai_netdev_container_receive_frame_total counter
huatuo_bamai_netdev_container_receive_frame_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_multicast_total Network device statistic receive_multicast.
# TYPE huatuo_bamai_netdev_container_receive_multicast_total counter
huatuo_bamai_netdev_container_receive_multicast_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_receive_packets_total Network device statistic receive_packets.
# TYPE huatuo_bamai_netdev_container_receive_packets_total counter
huatuo_bamai_netdev_container_receive_packets_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 693155
# HELP huatuo_bamai_netdev_container_transmit_bytes_total Network device statistic transmit_bytes.
# TYPE huatuo_bamai_netdev_container_transmit_bytes_total counter
huatuo_bamai_netdev_container_transmit_bytes_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 6.2347911e+07
# HELP huatuo_bamai_netdev_container_transmit_carrier_total Network device statistic transmit_carrier.
# TYPE huatuo_bamai_netdev_container_transmit_carrier_total counter
huatuo_bamai_netdev_container_transmit_carrier_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_colls_total Network device statistic transmit_colls.
# TYPE huatuo_bamai_netdev_container_transmit_colls_total counter
huatuo_bamai_netdev_container_transmit_colls_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_compressed_total Network device statistic transmit_compressed.
# TYPE huatuo_bamai_netdev_container_transmit_compressed_total counter
huatuo_bamai_netdev_container_transmit_compressed_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_dropped_total Network device statistic transmit_dropped.
# TYPE huatuo_bamai_netdev_container_transmit_dropped_total counter
huatuo_bamai_netdev_container_transmit_dropped_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_errors_total Network device statistic transmit_errors.
# TYPE huatuo_bamai_netdev_container_transmit_errors_total counter
huatuo_bamai_netdev_container_transmit_errors_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_fifo_total Network device statistic transmit_fifo.
# TYPE huatuo_bamai_netdev_container_transmit_fifo_total counter
huatuo_bamai_netdev_container_transmit_fifo_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netdev_container_transmit_packets_total Network device statistic transmit_packets.
# TYPE huatuo_bamai_netdev_container_transmit_packets_total counter
huatuo_bamai_netdev_container_transmit_packets_total{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",device="eth0",host="hostname",region="dev"} 660218
```

|Metric|Description|Unit|Scope| Labels |
|---|---|---|---|---|
|netdev_receive_bytes_total|Total number of bytes successfully received|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_packets_total|Total number of packets successfully received|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_compressed_total|Number of compressed packets received|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_frame_total|Number of frame alignment errors on receive|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_errors_total|Total number of receive errors|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_dropped_total|Number of received packets dropped by kernel or driver (various reasons)|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_fifo_total|Number of receive FIFO/ring buffer overflow errors|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_bytes_total|Total number of bytes successfully transmitted|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_packets_total|Total number of packets successfully transmitted|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_errors_total|Total number of transmit errors|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_dropped_total|Number of packets dropped during transmission (queue full, policy, etc.)|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_fifo_total|Number of transmit FIFO/ring buffer errors|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_carrier_total|Number of carrier errors (link down or cable issues during transmission)|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_compressed_total|Number of compressed packets transmitted|count|Host, Container| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|


### Tcp Memory

From /proc/net/netstat

```bash
# HELP huatuo_bamai_tcp_memory_limit_pages tcp memory pages limit
# TYPE huatuo_bamai_tcp_memory_limit_pages gauge
huatuo_bamai_tcp_memory_limit_pages{host="hostname",region="dev"} 380526
# HELP huatuo_bamai_tcp_memory_usage_bytes tcp memory bytes usage
# TYPE huatuo_bamai_tcp_memory_usage_bytes gauge
huatuo_bamai_tcp_memory_usage_bytes{host="hostname",region="dev"} 0
# HELP huatuo_bamai_tcp_memory_usage_pages tcp memory pages usage
# TYPE huatuo_bamai_tcp_memory_usage_pages gauge
huatuo_bamai_tcp_memory_usage_pages{host="hostname",region="dev"} 0
# HELP huatuo_bamai_tcp_memory_usage_percent tcp memory usage percent
# TYPE huatuo_bamai_tcp_memory_usage_percent gauge
huatuo_bamai_tcp_memory_usage_percent{host="hostname",region="dev"} 0
```

### TcpExt

Linux-specific TCP extended statistics (see kernel Documentation/networking/snmp_counter.rst):

- TcpExtListenDrops / ListenOverflows: drops due to full listen queue
- TcpExtSyncookiesSent / Recv / Failed: SYN cookies handling
- TcpExtTCPRcvCoalesce: packets coalesced in receive path
- TcpExtTCPAutoCorking: packets corked automatically
- TcpExtTCPOrigDataSent: original data bytes sent (excluding retransmits)
- TcpExtTCPLossProbes / TCPLossProbeRecovery: tail loss probe statistics
- TcpExtTCPAbortOn*: various abort reasons
- ... (many more – refer to kernel snmp_counter documentation for complete list)

```bash
# HELP huatuo_bamai_netstat_container_TcpExt_ArpFilter statistic TcpExtArpFilter.
# TYPE huatuo_bamai_netstat_container_TcpExt_ArpFilter gauge
huatuo_bamai_netstat_container_TcpExt_ArpFilter{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_BusyPollRxPackets statistic TcpExtBusyPollRxPackets.
# TYPE huatuo_bamai_netstat_container_TcpExt_BusyPollRxPackets gauge
huatuo_bamai_netstat_container_TcpExt_BusyPollRxPackets{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_DelayedACKLocked statistic TcpExtDelayedACKLocked.
# TYPE huatuo_bamai_netstat_container_TcpExt_DelayedACKLocked gauge
huatuo_bamai_netstat_container_TcpExt_DelayedACKLocked{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_DelayedACKLost statistic TcpExtDelayedACKLost.
# TYPE huatuo_bamai_netstat_container_TcpExt_DelayedACKLost gauge
huatuo_bamai_netstat_container_TcpExt_DelayedACKLost{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_DelayedACKs statistic TcpExtDelayedACKs.
# TYPE huatuo_bamai_netstat_container_TcpExt_DelayedACKs gauge
huatuo_bamai_netstat_container_TcpExt_DelayedACKs{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 4650
# HELP huatuo_bamai_netstat_container_TcpExt_EmbryonicRsts statistic TcpExtEmbryonicRsts.
# TYPE huatuo_bamai_netstat_container_TcpExt_EmbryonicRsts gauge
huatuo_bamai_netstat_container_TcpExt_EmbryonicRsts{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_IPReversePathFilter statistic TcpExtIPReversePathFilter.
# TYPE huatuo_bamai_netstat_container_TcpExt_IPReversePathFilter gauge
huatuo_bamai_netstat_container_TcpExt_IPReversePathFilter{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_ListenDrops statistic TcpExtListenDrops.
# TYPE huatuo_bamai_netstat_container_TcpExt_ListenDrops gauge
huatuo_bamai_netstat_container_TcpExt_ListenDrops{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_ListenOverflows statistic TcpExtListenOverflows.
# TYPE huatuo_bamai_netstat_container_TcpExt_ListenOverflows gauge
huatuo_bamai_netstat_container_TcpExt_ListenOverflows{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_LockDroppedIcmps statistic TcpExtLockDroppedIcmps.
# TYPE huatuo_bamai_netstat_container_TcpExt_LockDroppedIcmps gauge
huatuo_bamai_netstat_container_TcpExt_LockDroppedIcmps{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_OfoPruned statistic TcpExtOfoPruned.
# TYPE huatuo_bamai_netstat_container_TcpExt_OfoPruned gauge
huatuo_bamai_netstat_container_TcpExt_OfoPruned{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_OutOfWindowIcmps statistic TcpExtOutOfWindowIcmps.
# TYPE huatuo_bamai_netstat_container_TcpExt_OutOfWindowIcmps gauge
huatuo_bamai_netstat_container_TcpExt_OutOfWindowIcmps{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_PAWSActive statistic TcpExtPAWSActive.
# TYPE huatuo_bamai_netstat_container_TcpExt_PAWSActive gauge
huatuo_bamai_netstat_container_TcpExt_PAWSActive{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_PAWSEstab statistic TcpExtPAWSEstab.
# TYPE huatuo_bamai_netstat_container_TcpExt_PAWSEstab gauge
huatuo_bamai_netstat_container_TcpExt_PAWSEstab{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_PFMemallocDrop statistic TcpExtPFMemallocDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_PFMemallocDrop gauge
huatuo_bamai_netstat_container_TcpExt_PFMemallocDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_PruneCalled statistic TcpExtPruneCalled.
# TYPE huatuo_bamai_netstat_container_TcpExt_PruneCalled gauge
huatuo_bamai_netstat_container_TcpExt_PruneCalled{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_RcvPruned statistic TcpExtRcvPruned.
# TYPE huatuo_bamai_netstat_container_TcpExt_RcvPruned gauge
huatuo_bamai_netstat_container_TcpExt_RcvPruned{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_SyncookiesFailed statistic TcpExtSyncookiesFailed.
# TYPE huatuo_bamai_netstat_container_TcpExt_SyncookiesFailed gauge
huatuo_bamai_netstat_container_TcpExt_SyncookiesFailed{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_SyncookiesRecv statistic TcpExtSyncookiesRecv.
# TYPE huatuo_bamai_netstat_container_TcpExt_SyncookiesRecv gauge
huatuo_bamai_netstat_container_TcpExt_SyncookiesRecv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_SyncookiesSent statistic TcpExtSyncookiesSent.
# TYPE huatuo_bamai_netstat_container_TcpExt_SyncookiesSent gauge
huatuo_bamai_netstat_container_TcpExt_SyncookiesSent{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedChallenge statistic TcpExtTCPACKSkippedChallenge.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedChallenge gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedChallenge{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedFinWait2 statistic TcpExtTCPACKSkippedFinWait2.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedFinWait2 gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedFinWait2{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedPAWS statistic TcpExtTCPACKSkippedPAWS.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedPAWS gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedPAWS{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSeq statistic TcpExtTCPACKSkippedSeq.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSeq gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSeq{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSynRecv statistic TcpExtTCPACKSkippedSynRecv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSynRecv gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedSynRecv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedTimeWait statistic TcpExtTCPACKSkippedTimeWait.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedTimeWait gauge
huatuo_bamai_netstat_container_TcpExt_TCPACKSkippedTimeWait{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAOBad statistic TcpExtTCPAOBad.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAOBad gauge
huatuo_bamai_netstat_container_TcpExt_TCPAOBad{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAODroppedIcmps statistic TcpExtTCPAODroppedIcmps.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAODroppedIcmps gauge
huatuo_bamai_netstat_container_TcpExt_TCPAODroppedIcmps{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAOGood statistic TcpExtTCPAOGood.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAOGood gauge
huatuo_bamai_netstat_container_TcpExt_TCPAOGood{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAOKeyNotFound statistic TcpExtTCPAOKeyNotFound.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAOKeyNotFound gauge
huatuo_bamai_netstat_container_TcpExt_TCPAOKeyNotFound{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAORequired statistic TcpExtTCPAORequired.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAORequired gauge
huatuo_bamai_netstat_container_TcpExt_TCPAORequired{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortFailed statistic TcpExtTCPAbortFailed.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortFailed gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortFailed{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortOnClose statistic TcpExtTCPAbortOnClose.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortOnClose gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortOnClose{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortOnData statistic TcpExtTCPAbortOnData.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortOnData gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortOnData{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortOnLinger statistic TcpExtTCPAbortOnLinger.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortOnLinger gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortOnLinger{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortOnMemory statistic TcpExtTCPAbortOnMemory.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortOnMemory gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortOnMemory{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAbortOnTimeout statistic TcpExtTCPAbortOnTimeout.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAbortOnTimeout gauge
huatuo_bamai_netstat_container_TcpExt_TCPAbortOnTimeout{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAckCompressed statistic TcpExtTCPAckCompressed.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAckCompressed gauge
huatuo_bamai_netstat_container_TcpExt_TCPAckCompressed{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPAutoCorking statistic TcpExtTCPAutoCorking.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPAutoCorking gauge
huatuo_bamai_netstat_container_TcpExt_TCPAutoCorking{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPBacklogCoalesce statistic TcpExtTCPBacklogCoalesce.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPBacklogCoalesce gauge
huatuo_bamai_netstat_container_TcpExt_TCPBacklogCoalesce{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 3
# HELP huatuo_bamai_netstat_container_TcpExt_TCPBacklogDrop statistic TcpExtTCPBacklogDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPBacklogDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPBacklogDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPChallengeACK statistic TcpExtTCPChallengeACK.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPChallengeACK gauge
huatuo_bamai_netstat_container_TcpExt_TCPChallengeACK{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredDubious statistic TcpExtTCPDSACKIgnoredDubious.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredDubious gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredDubious{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredNoUndo statistic TcpExtTCPDSACKIgnoredNoUndo.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredNoUndo gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredNoUndo{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredOld statistic TcpExtTCPDSACKIgnoredOld.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredOld gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKIgnoredOld{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoRecv statistic TcpExtTCPDSACKOfoRecv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoRecv gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoRecv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoSent statistic TcpExtTCPDSACKOfoSent.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoSent gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKOfoSent{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKOldSent statistic TcpExtTCPDSACKOldSent.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKOldSent gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKOldSent{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecv statistic TcpExtTCPDSACKRecv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecv gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecvSegs statistic TcpExtTCPDSACKRecvSegs.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecvSegs gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKRecvSegs{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDSACKUndo statistic TcpExtTCPDSACKUndo.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDSACKUndo gauge
huatuo_bamai_netstat_container_TcpExt_TCPDSACKUndo{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDeferAcceptDrop statistic TcpExtTCPDeferAcceptDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDeferAcceptDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPDeferAcceptDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDelivered statistic TcpExtTCPDelivered.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDelivered gauge
huatuo_bamai_netstat_container_TcpExt_TCPDelivered{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 3.28098e+06
# HELP huatuo_bamai_netstat_container_TcpExt_TCPDeliveredCE statistic TcpExtTCPDeliveredCE.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPDeliveredCE gauge
huatuo_bamai_netstat_container_TcpExt_TCPDeliveredCE{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActive statistic TcpExtTCPFastOpenActive.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActive gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActive{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActiveFail statistic TcpExtTCPFastOpenActiveFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActiveFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenActiveFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenBlackhole statistic TcpExtTCPFastOpenBlackhole.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenBlackhole gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenBlackhole{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenCookieReqd statistic TcpExtTCPFastOpenCookieReqd.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenCookieReqd gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenCookieReqd{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenListenOverflow statistic TcpExtTCPFastOpenListenOverflow.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenListenOverflow gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenListenOverflow{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassive statistic TcpExtTCPFastOpenPassive.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassive gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassive{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveAltKey statistic TcpExtTCPFastOpenPassiveAltKey.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveAltKey gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveAltKey{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveFail statistic TcpExtTCPFastOpenPassiveFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastOpenPassiveFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFastRetrans statistic TcpExtTCPFastRetrans.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFastRetrans gauge
huatuo_bamai_netstat_container_TcpExt_TCPFastRetrans{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFromZeroWindowAdv statistic TcpExtTCPFromZeroWindowAdv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFromZeroWindowAdv gauge
huatuo_bamai_netstat_container_TcpExt_TCPFromZeroWindowAdv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPFullUndo statistic TcpExtTCPFullUndo.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPFullUndo gauge
huatuo_bamai_netstat_container_TcpExt_TCPFullUndo{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHPAcks statistic TcpExtTCPHPAcks.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHPAcks gauge
huatuo_bamai_netstat_container_TcpExt_TCPHPAcks{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 616667
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHPHits statistic TcpExtTCPHPHits.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHPHits gauge
huatuo_bamai_netstat_container_TcpExt_TCPHPHits{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 9913
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayCwnd statistic TcpExtTCPHystartDelayCwnd.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayCwnd gauge
huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayCwnd{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayDetect statistic TcpExtTCPHystartDelayDetect.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayDetect gauge
huatuo_bamai_netstat_container_TcpExt_TCPHystartDelayDetect{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainCwnd statistic TcpExtTCPHystartTrainCwnd.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainCwnd gauge
huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainCwnd{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainDetect statistic TcpExtTCPHystartTrainDetect.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainDetect gauge
huatuo_bamai_netstat_container_TcpExt_TCPHystartTrainDetect{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPKeepAlive statistic TcpExtTCPKeepAlive.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPKeepAlive gauge
huatuo_bamai_netstat_container_TcpExt_TCPKeepAlive{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 20
# HELP huatuo_bamai_netstat_container_TcpExt_TCPLossFailures statistic TcpExtTCPLossFailures.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPLossFailures gauge
huatuo_bamai_netstat_container_TcpExt_TCPLossFailures{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPLossProbeRecovery statistic TcpExtTCPLossProbeRecovery.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPLossProbeRecovery gauge
huatuo_bamai_netstat_container_TcpExt_TCPLossProbeRecovery{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPLossProbes statistic TcpExtTCPLossProbes.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPLossProbes gauge
huatuo_bamai_netstat_container_TcpExt_TCPLossProbes{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_netstat_container_TcpExt_TCPLossUndo statistic TcpExtTCPLossUndo.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPLossUndo gauge
huatuo_bamai_netstat_container_TcpExt_TCPLossUndo{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPLostRetransmit statistic TcpExtTCPLostRetransmit.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPLostRetransmit gauge
huatuo_bamai_netstat_container_TcpExt_TCPLostRetransmit{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMD5Failure statistic TcpExtTCPMD5Failure.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMD5Failure gauge
huatuo_bamai_netstat_container_TcpExt_TCPMD5Failure{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMD5NotFound statistic TcpExtTCPMD5NotFound.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMD5NotFound gauge
huatuo_bamai_netstat_container_TcpExt_TCPMD5NotFound{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMD5Unexpected statistic TcpExtTCPMD5Unexpected.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMD5Unexpected gauge
huatuo_bamai_netstat_container_TcpExt_TCPMD5Unexpected{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMTUPFail statistic TcpExtTCPMTUPFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMTUPFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPMTUPFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMTUPSuccess statistic TcpExtTCPMTUPSuccess.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMTUPSuccess gauge
huatuo_bamai_netstat_container_TcpExt_TCPMTUPSuccess{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressures statistic TcpExtTCPMemoryPressures.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressures gauge
huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressures{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressuresChrono statistic TcpExtTCPMemoryPressuresChrono.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressuresChrono gauge
huatuo_bamai_netstat_container_TcpExt_TCPMemoryPressuresChrono{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqFailure statistic TcpExtTCPMigrateReqFailure.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqFailure gauge
huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqFailure{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqSuccess statistic TcpExtTCPMigrateReqSuccess.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqSuccess gauge
huatuo_bamai_netstat_container_TcpExt_TCPMigrateReqSuccess{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPMinTTLDrop statistic TcpExtTCPMinTTLDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPMinTTLDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPMinTTLDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPOFODrop statistic TcpExtTCPOFODrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPOFODrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPOFODrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPOFOMerge statistic TcpExtTCPOFOMerge.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPOFOMerge gauge
huatuo_bamai_netstat_container_TcpExt_TCPOFOMerge{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPOFOQueue statistic TcpExtTCPOFOQueue.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPOFOQueue gauge
huatuo_bamai_netstat_container_TcpExt_TCPOFOQueue{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPOrigDataSent statistic TcpExtTCPOrigDataSent.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPOrigDataSent gauge
huatuo_bamai_netstat_container_TcpExt_TCPOrigDataSent{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 2.675557e+06
# HELP huatuo_bamai_netstat_container_TcpExt_TCPPLBRehash statistic TcpExtTCPPLBRehash.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPPLBRehash gauge
huatuo_bamai_netstat_container_TcpExt_TCPPLBRehash{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPPartialUndo statistic TcpExtTCPPartialUndo.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPPartialUndo gauge
huatuo_bamai_netstat_container_TcpExt_TCPPartialUndo{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPPureAcks statistic TcpExtTCPPureAcks.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPPureAcks gauge
huatuo_bamai_netstat_container_TcpExt_TCPPureAcks{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 2.095262e+06
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRcvCoalesce statistic TcpExtTCPRcvCoalesce.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRcvCoalesce gauge
huatuo_bamai_netstat_container_TcpExt_TCPRcvCoalesce{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 3
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRcvCollapsed statistic TcpExtTCPRcvCollapsed.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRcvCollapsed gauge
huatuo_bamai_netstat_container_TcpExt_TCPRcvCollapsed{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRcvQDrop statistic TcpExtTCPRcvQDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRcvQDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPRcvQDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRenoFailures statistic TcpExtTCPRenoFailures.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRenoFailures gauge
huatuo_bamai_netstat_container_TcpExt_TCPRenoFailures{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRenoRecovery statistic TcpExtTCPRenoRecovery.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRenoRecovery gauge
huatuo_bamai_netstat_container_TcpExt_TCPRenoRecovery{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRenoRecoveryFail statistic TcpExtTCPRenoRecoveryFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRenoRecoveryFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPRenoRecoveryFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRenoReorder statistic TcpExtTCPRenoReorder.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRenoReorder gauge
huatuo_bamai_netstat_container_TcpExt_TCPRenoReorder{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDoCookies statistic TcpExtTCPReqQFullDoCookies.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDoCookies gauge
huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDoCookies{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDrop statistic TcpExtTCPReqQFullDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPReqQFullDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPRetransFail statistic TcpExtTCPRetransFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPRetransFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPRetransFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSACKDiscard statistic TcpExtTCPSACKDiscard.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSACKDiscard gauge
huatuo_bamai_netstat_container_TcpExt_TCPSACKDiscard{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSACKReneging statistic TcpExtTCPSACKReneging.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSACKReneging gauge
huatuo_bamai_netstat_container_TcpExt_TCPSACKReneging{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSACKReorder statistic TcpExtTCPSACKReorder.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSACKReorder gauge
huatuo_bamai_netstat_container_TcpExt_TCPSACKReorder{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSYNChallenge statistic TcpExtTCPSYNChallenge.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSYNChallenge gauge
huatuo_bamai_netstat_container_TcpExt_TCPSYNChallenge{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackFailures statistic TcpExtTCPSackFailures.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackFailures gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackFailures{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackMerged statistic TcpExtTCPSackMerged.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackMerged gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackMerged{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackRecovery statistic TcpExtTCPSackRecovery.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackRecovery gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackRecovery{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackRecoveryFail statistic TcpExtTCPSackRecoveryFail.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackRecoveryFail gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackRecoveryFail{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackShiftFallback statistic TcpExtTCPSackShiftFallback.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackShiftFallback gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackShiftFallback{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSackShifted statistic TcpExtTCPSackShifted.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSackShifted gauge
huatuo_bamai_netstat_container_TcpExt_TCPSackShifted{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSlowStartRetrans statistic TcpExtTCPSlowStartRetrans.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSlowStartRetrans gauge
huatuo_bamai_netstat_container_TcpExt_TCPSlowStartRetrans{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRTOs statistic TcpExtTCPSpuriousRTOs.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRTOs gauge
huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRTOs{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRtxHostQueues statistic TcpExtTCPSpuriousRtxHostQueues.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRtxHostQueues gauge
huatuo_bamai_netstat_container_TcpExt_TCPSpuriousRtxHostQueues{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPSynRetrans statistic TcpExtTCPSynRetrans.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPSynRetrans gauge
huatuo_bamai_netstat_container_TcpExt_TCPSynRetrans{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPTSReorder statistic TcpExtTCPTSReorder.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPTSReorder gauge
huatuo_bamai_netstat_container_TcpExt_TCPTSReorder{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPTimeWaitOverflow statistic TcpExtTCPTimeWaitOverflow.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPTimeWaitOverflow gauge
huatuo_bamai_netstat_container_TcpExt_TCPTimeWaitOverflow{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPTimeouts statistic TcpExtTCPTimeouts.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPTimeouts gauge
huatuo_bamai_netstat_container_TcpExt_TCPTimeouts{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPToZeroWindowAdv statistic TcpExtTCPToZeroWindowAdv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPToZeroWindowAdv gauge
huatuo_bamai_netstat_container_TcpExt_TCPToZeroWindowAdv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPWantZeroWindowAdv statistic TcpExtTCPWantZeroWindowAdv.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPWantZeroWindowAdv gauge
huatuo_bamai_netstat_container_TcpExt_TCPWantZeroWindowAdv{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPWinProbe statistic TcpExtTCPWinProbe.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPWinProbe gauge
huatuo_bamai_netstat_container_TcpExt_TCPWinProbe{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPWqueueTooBig statistic TcpExtTCPWqueueTooBig.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPWqueueTooBig gauge
huatuo_bamai_netstat_container_TcpExt_TCPWqueueTooBig{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TCPZeroWindowDrop statistic TcpExtTCPZeroWindowDrop.
# TYPE huatuo_bamai_netstat_container_TcpExt_TCPZeroWindowDrop gauge
huatuo_bamai_netstat_container_TcpExt_TCPZeroWindowDrop{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TW statistic TcpExtTW.
# TYPE huatuo_bamai_netstat_container_TcpExt_TW gauge
huatuo_bamai_netstat_container_TcpExt_TW{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 720624
# HELP huatuo_bamai_netstat_container_TcpExt_TWKilled statistic TcpExtTWKilled.
# TYPE huatuo_bamai_netstat_container_TcpExt_TWKilled gauge
huatuo_bamai_netstat_container_TcpExt_TWKilled{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TWRecycled statistic TcpExtTWRecycled.
# TYPE huatuo_bamai_netstat_container_TcpExt_TWRecycled gauge
huatuo_bamai_netstat_container_TcpExt_TWRecycled{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 2461
# HELP huatuo_bamai_netstat_container_TcpExt_TcpDuplicateDataRehash statistic TcpExtTcpDuplicateDataRehash.
# TYPE huatuo_bamai_netstat_container_TcpExt_TcpDuplicateDataRehash gauge
huatuo_bamai_netstat_container_TcpExt_TcpDuplicateDataRehash{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_netstat_container_TcpExt_TcpTimeoutRehash statistic TcpExtTcpTimeoutRehash.
# TYPE huatuo_bamai_netstat_container_TcpExt_TcpTimeoutRehash gauge
huatuo_bamai_netstat_container_TcpExt_TcpTimeoutRehash{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

Ref:
- https://www.kernel.org/doc/html/latest/networking/snmp_counter.html

### Socket

```bash
# HELP huatuo_bamai_sockstat_container_FRAG_inuse Number of FRAG sockets in state inuse.
# TYPE huatuo_bamai_sockstat_container_FRAG_inuse gauge
huatuo_bamai_sockstat_container_FRAG_inuse{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_FRAG_memory Number of FRAG sockets in state memory.
# TYPE huatuo_bamai_sockstat_container_FRAG_memory gauge
huatuo_bamai_sockstat_container_FRAG_memory{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_RAW_inuse Number of RAW sockets in state inuse.
# TYPE huatuo_bamai_sockstat_container_RAW_inuse gauge
huatuo_bamai_sockstat_container_RAW_inuse{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_TCP_alloc Number of TCP sockets in state alloc.
# TYPE huatuo_bamai_sockstat_container_TCP_alloc gauge
huatuo_bamai_sockstat_container_TCP_alloc{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 171
# HELP huatuo_bamai_sockstat_container_TCP_inuse Number of TCP sockets in state inuse.
# TYPE huatuo_bamai_sockstat_container_TCP_inuse gauge
huatuo_bamai_sockstat_container_TCP_inuse{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 1
# HELP huatuo_bamai_sockstat_container_TCP_orphan Number of TCP sockets in state orphan.
# TYPE huatuo_bamai_sockstat_container_TCP_orphan gauge
huatuo_bamai_sockstat_container_TCP_orphan{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_TCP_tw Number of TCP sockets in state tw.
# TYPE huatuo_bamai_sockstat_container_TCP_tw gauge
huatuo_bamai_sockstat_container_TCP_tw{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 75
# HELP huatuo_bamai_sockstat_container_UDPLITE_inuse Number of UDPLITE sockets in state inuse.
# TYPE huatuo_bamai_sockstat_container_UDPLITE_inuse gauge
huatuo_bamai_sockstat_container_UDPLITE_inuse{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_UDP_inuse Number of UDP sockets in state inuse.
# TYPE huatuo_bamai_sockstat_container_UDP_inuse gauge
huatuo_bamai_sockstat_container_UDP_inuse{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_sockstat_container_sockets_used Number of IPv4 sockets in use.
# TYPE huatuo_bamai_sockstat_container_sockets_used gauge
huatuo_bamai_sockstat_container_sockets_used{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 7
# HELP huatuo_bamai_sockstat_sockets_used Number of IPv4 sockets in use.
# TYPE huatuo_bamai_sockstat_sockets_used gauge
huatuo_bamai_sockstat_sockets_used{host="hostname",region="dev"} 409
```

|Metric|Description|Unit|Scope| Labels |
|---|---|---|---|---|
|sockstat_sockets_used|Total number of sockets currently in use on the system|count|Host||
|sockstat_TCP_inuse|Number of TCP sockets in active connection states|count|Host, Container||
|sockstat_TCP_orphan|Number of TCP sockets without an owning process|count|Host, Container||
|sockstat_TCP_tw|Number of TCP sockets currently in TIME_WAIT state|count|Host, Container||
|sockstat_TCP_alloc|Total number of allocated TCP socket objects|count|Host, Container||
|sockstat_TCP_mem|Number of memory pages currently used by TCP sockets|count|Host||

## IO

`iolatency` tracks disk I/O latency distribution. A simple way to read it is: break one disk request into stages, then count how many requests fall into each latency bucket.

- `q2c`: from entering the queue to completion, covering the full I/O lifecycle
- `d2c`: from driver dispatch to completion, closer to device-side latency
- `freeze`: number of disk freeze events

The current version exposes both host-level and container-level metrics.

### Queue

These metrics always include the common labels `host` and `region`. Container
metrics also always include `container_host`, `container_name`,
`container_type`, `container_level`, and `container_hostnamespace`.

```bash
# HELP huatuo_bamai_iolatency_disk_q2c disk q2c latency
# TYPE huatuo_bamai_iolatency_disk_q2c gauge
huatuo_bamai_iolatency_disk_q2c{disk="8:0",host="hostname",region="dev",zone="0"} 12
# HELP huatuo_bamai_iolatency_disk_d2c disk d2c latency
# TYPE huatuo_bamai_iolatency_disk_d2c gauge
huatuo_bamai_iolatency_disk_d2c{disk="8:0",host="hostname",region="dev",zone="1"} 3
# HELP huatuo_bamai_iolatency_container_q2c container q2c latency
# TYPE huatuo_bamai_iolatency_container_q2c gauge
huatuo_bamai_iolatency_container_q2c{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="0"} 7
# HELP huatuo_bamai_iolatency_container_d2c container d2c latency
# TYPE huatuo_bamai_iolatency_container_d2c gauge
huatuo_bamai_iolatency_container_d2c{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="1"} 2
```

|Metric|Description|Unit|Scope|Labels|
|---|---|---|---|---|
|iolatency_disk_q2c|Host disk latency statistics for the full I/O lifecycle, from queueing to completion. Buckets: zone0 20-30ms, zone1 30-50ms, zone2 50-100ms, zone3 100-200ms, zone4 200-400ms, zone5 400ms+|count|Host|host, region, disk, zone|
|iolatency_disk_d2c|Host disk latency statistics from driver dispatch to completion, closer to device processing time. Buckets: zone0 20-30ms, zone1 30-50ms, zone2 50-100ms, zone3 100-200ms, zone4 200-400ms, zone5 400ms+|count|Host|host, region, disk, zone|
|iolatency_container_q2c|Container-caused latency statistics for the full I/O lifecycle, from queueing to completion. Buckets: zone0 20-30ms, zone1 30-50ms, zone2 50-100ms, zone3 100-200ms, zone4 200-400ms, zone5 400ms+|count|Container|host, region, container_host, container_name, container_type, container_level, container_hostnamespace, zone|
|iolatency_container_d2c|Container-caused latency statistics from driver dispatch to completion. Buckets: zone0 20-30ms, zone1 30-50ms, zone2 50-100ms, zone3 100-200ms, zone4 200-400ms, zone5 400ms+|count|Container|host, region, container_host, container_name, container_type, container_level, container_hostnamespace, zone|

### Hardware

```bash
# HELP huatuo_bamai_iolatency_disk_freeze disk freeze count
# TYPE huatuo_bamai_iolatency_disk_freeze gauge
huatuo_bamai_iolatency_disk_freeze{disk="8:0",host="hostname",region="dev"} 4
```

|Metric|Description|Unit|Scope|Labels|
|---|---|---|---|---|
|iolatency_disk_freeze|Host disk freeze event count|count|Host|host, region, disk|

## General System

### Soft Lockup

```bash
# HELP huatuo_bamai_softlockup_total softlockup counter
# TYPE huatuo_bamai_softlockup_total counter
huatuo_bamai_softlockup_total{host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|softlockup_total|Count of soft lockup events|count|Host|BPF|

### HungTask
```bash
# HELP huatuo_bamai_hungtask_total hungtask counter
# TYPE huatuo_bamai_hungtask_total counter
huatuo_bamai_hungtask_total{host="hostname",region="dev"} 0
```

|Metric|Description|Unit|Target|Source|Labels|
|---|---|---|---|---|---|
|hungtask_total|Count of hung task events|count|Host|BPF|


## GPU

- MetaX

|Metric|Description|Unit|Target|Source|
|----|---|---|---|---|
|metax_gpu_sdk_info|GPU SDK info.|-|version|sml.GetSDKVersion|
|metax_gpu_driver_info|GPU driver info.|-|version|sml.GetGPUVersion with driver unit|
|metax_gpu_info|GPU info.|-|gpu, model, uuid, bios_version, bdf, mode, die_count|sml.GetGPUInfo|
|metax_gpu_board_power_watts|GPU board power.|W|gpu|sml.ListGPUBoardWayElectricInfos|
|metax_gpu_pcie_link_speed_gt_per_second|GPU PCIe current link speed.|GT/s|gpu|sml.GetGPUPcieLinkInfo|
|metax_gpu_pcie_link_width_lanes|GPU PCIe current link width.|lanes|gpu|sml.GetGPUPcieLinkInfo|
|metax_gpu_pcie_receive_bytes_per_second|GPU PCIe receive throughput.|B/s|gpu|sml.GetGPUPcieThroughputInfo|
|metax_gpu_pcie_transmit_bytes_per_second|GPU PCIe transmit throughput.|B/s|gpu|sml.GetGPUPcieThroughputInfo|
|metax_gpu_metaxlink_link_speed_gt_per_second|GPU MetaXLink current link speed.|GT/s|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|metax_gpu_metaxlink_link_width_lanes|GPU MetaXLink current link width.|lanes|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|metax_gpu_metaxlink_receive_bytes_per_second|GPU MetaXLink receive throughput.|B/s|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|metax_gpu_metaxlink_transmit_bytes_per_second|GPU MetaXLink transmit throughput.|B/s|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|metax_gpu_metaxlink_receive_bytes_total|GPU MetaXLink receive data size.|bytes|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|metax_gpu_metaxlink_transmit_bytes_total|GPU MetaXLink transmit data size.|bytes|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|metax_gpu_metaxlink_aer_errors_total|GPU MetaXLink AER errors count.|count|gpu, metaxlink, error_type|sml.ListGPUMetaXLinkAerErrorsInfos|
|metax_gpu_status|GPU status, 0 means normal, other values means abnormal. Check the documentation to see the exceptions corresponding to each value.|-|gpu, die|sml.GetDieStatus|
|metax_gpu_temperature_celsius|GPU temperature.|°C|gpu, die|sml.GetDieTemperature|
|metax_gpu_utilization_percent|GPU utilization, ranging from 0 to 100.|%|gpu, die, ip|sml.GetDieUtilization|
|metax_gpu_memory_total_bytes|Total vram.|bytes|gpu, die|sml.GetDieMemoryInfo|
|metax_gpu_memory_used_bytes|Used vram.|bytes|gpu, die|sml.GetDieMemoryInfo|
|metax_gpu_clock_mhz|GPU clock.|MHz|gpu, die, ip|sml.ListDieClocks|
|metax_gpu_clocks_throttling|Reason(s) for GPU clocks throttling.|-|gpu, die, reason|sml.GetDieClocksThrottleStatus|
|metax_gpu_dpm_performance_level|GPU DPM performance level.|-|gpu, die, ip|sml.GetDieDPMPerformanceLevel|
|metax_gpu_ecc_memory_errors_total|GPU ECC memory errors count.|count|gpu, die, memory_type, error_type|sml.GetDieECCMemoryInfo|
|metax_gpu_ecc_memory_retired_pages_total|GPU ECC memory retired pages count.|count|gpu, die|sml.GetDieECCMemoryInfo|
