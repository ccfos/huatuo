---
title: 指标参考
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

本文列出指标名称、单位、标签、来源及统计口径。功能与排查场景见[内核全景观测](/docs/key-feature/kernel-wide-insight_zh.md)。

## 阅读说明

本文按 CPU、内存、网络、I/O、系统事件和 GPU 分类说明常用指标，供查询和排障时参考。实际输出取决于内核接口、设备驱动、采集器开关和过滤配置，完整指标以节点的 `/metrics` 输出为准。启用方式见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)。

- **名称**：表格统一省略 `huatuo_bamai_` 前缀。例如，`cpu_util_total` 的完整名称为 `huatuo_bamai_cpu_util_total`。
- **标签**：所有指标带有 `host`、`region`；容器指标另带 `container_host`、`container_hostnamespace`、`container_level`、`container_name`、`container_type`。表格仅列附加标签，`—` 表示无附加标签。
- **计数与单位**：计数值表示事件或对象数量，不等于延迟值。累计 Counter 可用 `rate()` 或 `increase()` 分析变化；部分内核累计统计以 Gauge 导出，需结合字段语义判断，不能仅凭类型或名称推断。
- **示例**：下列数值仅用于说明输出格式，不代表诊断阈值。未输出指标不等于指标值为零。

```text
# TYPE huatuo_bamai_runqlat_latency counter
huatuo_bamai_runqlat_latency{host="hostname",region="dev",zone="0"} 35100
# TYPE huatuo_bamai_runqlat_container_latency counter
huatuo_bamai_runqlat_container_latency{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev",zone="0"} 226
```

此例分别展示宿主机和容器的调度延迟分桶计数，`zone` 的范围见下文。

## CPU

容器核数表示可用容量，不表示独占物理核心数；两端保留原有利用率口径和指标名。

`cpu_util` 和 `memory_vmstat` 在采集期间分别保留主机与容器的可用数据：容器发现失败不阻断主机指标，主机读取失败不丢弃已采集的容器指标。失败会返回采集管理器，标记该采集器本次采集失败，同时输出可用的部分指标；不会将失败项伪造成零。此行为不改变采集器初始化依赖，也不改变单个容器读取失败时记录日志并跳过的既有策略。

### 调度延迟

调度延迟指任务进入可运行状态到实际获得 CPU 的等待时间。以下指标为 eBPF 采集的累计 Counter，按 `zone` 分桶：`0` 为 [0, 10) ms，`1` 为 [10, 20) ms，`2` 为 [20, 50) ms，`3` 为 ≥50 ms。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `runqlat_container_latency` | 调度延迟分桶计数 | 计数 | 容器 | eBPF | zone |
| `runqlat_latency` | 调度延迟分桶计数 | 计数 | 宿主机 | eBPF | zone |

### 软中断延迟

统计各 CPU 上软中断从触发到开始处理的等待时间，当前仅输出 `NET_RX`、`NET_TX`。该指标为累计 Counter；`zone`：`0` 为 [0, 10) μs，`1` 为 [10, 100) μs，`2` 为 [100, 1000) μs，`3` 为 ≥1 ms。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `softirq_latency` | 软中断响应延迟分桶计数 | 计数 | 宿主机 | eBPF | cpuid, type, zone |

### 利用率

宿主机利用率按在线 CPU 核数归一化，容器利用率按可用 CPU 容量归一化，输出范围为 0–100%。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `cpu_util_sys` | CPU 内核态利用率 | % | 宿主机 | — |
| `cpu_util_usr` | CPU 用户态利用率 | % | 宿主机 | — |
| `cpu_util_total` | CPU 总利用率 | % | 宿主机 | — |
| `cpu_util_container_sys` | CPU 内核态利用率 | % | 容器 | — |
| `cpu_util_container_usr` | CPU 用户态利用率 | % | 容器 | — |
| `cpu_util_container_total` | CPU 总利用率 | % | 容器 | — |

### 可用核数

容器可用 CPU 容量取配额折算核数与有效 CPU 集合大小的较小值；未限制配额时使用有效 CPU 集合大小，无法获取有效集合时回退到宿主机在线核数。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `cpu_util_container_cores` | 可用 CPU 容量（可为小数） | 核 | 容器 | — |

### 限流与争抢

限流统计来自 cgroup CPU 接口，仅输出内核提供的字段。等待占比还要求内核导出 `wait_sum`，并启用 `kernel.sched_schedstats=1`。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `cpu_stat_container_nr_throttled` | cgroup 发生 CPU 限流的累计周期数 | 计数 | 容器 | — |
| `cpu_stat_container_throttled_time` | cgroup 被限流的累计时间 | 纳秒 | 容器 | — |
| `cpu_stat_container_wait_sum_percent` | 采集区间内 cgroup 在父运行队列的等待时间占可调度时间的比例 | % | 容器 | — |

等待占比按采集区间计算：`等待时间增量 /（等待时间增量 + CPU 使用时间增量）× 100%`。它描述可调度时间中的等待比例，不是限流时间占比。

参考：

- [CFS 带宽统计](https://docs.kernel.org/scheduler/sched-bwc.html#statistics)
- [cgroup v2 CPU 接口](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html#cpu-interface-files)

### 突发使用

统计 cgroup 使用突发 CPU 容量的周期数和时间，仅在内核提供对应字段时输出。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `cpu_stat_container_burst_time` | 使用突发 CPU 容量的累计时间 | 纳秒 | 容器 | — |
| `cpu_stat_container_nr_bursts` | 使用突发 CPU 容量的累计周期数 | 计数 | 容器 | — |

### 负载

宿主机输出 1、5、15 分钟平均负载；容器输出当前可运行和不可中断任务数，两者不是相同时间窗口的统计。

| 指标 | 说明 | 单位 | 范围 | 附加标签 | 备注 |
| --- | --- | --- | --- | --- | --- |
| `loadavg_load1` | 系统过去 1 分钟的平均负载 | 任务数 | 宿主机 | — | — |
| `loadavg_load5` | 系统过去 5 分钟的平均负载 | 任务数 | 宿主机 | — | — |
| `loadavg_load15` | 系统过去 15 分钟的平均负载 | 任务数 | 宿主机 | — | — |
| `loadavg_container_nr_running` | 当前可运行任务数（含正在运行和等待 CPU 的任务） | 计数 | 容器 | — | 支持 cgroup v1/v2；v2 要求 BPF task iterator 支持 |
| `loadavg_container_nr_uninterruptible` | 容器中不可中断任务的数量 | 计数 | 容器 | — | 支持 cgroup v1/v2；v2 要求 BPF task iterator 支持 |

### add the host runnable task count

|指标|意义|单位|对象|标签|备注|
|---|---|---|---|---|---|
|loadavg_nr_running|主机当前正在运行或等待 CPU 的任务数|计数|物理机| host, region |读取 `/proc/stat` 的 `procs_running`|

`nr_running` 是瞬时 Gauge，不是 `load1/5/15` 平均负载，也不是 CPU 利用率。主机包含容器任务，不能与容器值相加；容器沿用 taskstats 的非递归采样，不包含子 cgroup。主机数据依赖采集器可见的宿主机 procfs，不要求启用 cgroup；读取失败或字段缺失时不补零，且不影响已有 load average 和容器采集。

主机 `nr_uninterruptible` 使用下文的共享 task iterator 采集。[`/proc/stat` 的 `procs_blocked`](https://www.kernel.org/doc/html/latest/filesystems/proc.html) 表示等待 IO 的任务数，不能替代该 D 状态统计。

### collect cgroup v2 task states with a BPF iterator

Kubernetes 部署必须为 Huatuo 设置 `hostPID: true`。无法访问宿主机 PID namespace 时，cgroup v2 容器指标会作为不支持而省略，不会输出有误导性的全零数据。

### enable v2 load stats for metrics and dload

`nr_running` 是瞬时 Gauge，不是 `load1/5/15` 平均负载，也不是 CPU 利用率。主机包含容器任务，不能与容器值相加；容器采用非递归采样，不包含子 cgroup。主机数据依赖采集器可见的宿主机 procfs，不要求启用 cgroup；读取失败或字段缺失时不补零，且不影响已有 load average 和容器采集。
cgroup v2 容器负载指标随 `loadavg` 组件自动启用，没有独立开关。后台按 `MetricCollector.Loadavg.Interval` 采样（默认 15 秒），Prometheus 抓取读取缓存，不发起任务遍历。每次采样与主机 D 状态统计共用一次全主机遍历，只统计直接挂在目标 cgroup 下的任务。该路径需要可读的内核 BTF、BPF `task` iterator 和宿主机 PID namespace；不支持时省略依赖 iterator 的指标，保留 procfs 主机负载和 cgroup v1 netlink 采集。每个消费者只记录一次不支持告警，确定的不支持结果会被缓存。Dload 保持独立采样周期，可复用 100ms 内符合条件的快照。

### estimate container load averages on cgroup v1 and v2

|指标|意义|单位|对象|标签|备注|
|---|---|---|---|---|---|
|loadavg_container_load1|容器 1 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|
|loadavg_container_load5|容器 5 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|
|loadavg_container_load15|容器 15 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|

`nr_running` 是瞬时 Gauge，不是 `load1/5/15` 平均负载，也不是 CPU 利用率。主机包含容器任务，不能与容器值相加；容器采用非递归采样，不包含子 cgroup。主机数据依赖采集器可见的宿主机 procfs，不要求启用 cgroup；读取失败或字段缺失时不补零，且不影响已有 load average 和容器采集。

容器 R/D 与平均负载由 `loadavg` 生命周期按 `MetricCollector.Loadavg.Interval` 秒采样（默认 15 秒，0 使用默认值），独立于 Prometheus 抓取和 dload 自动追踪。v1 复用 netlink，v2 复用共享 BPF task iterator。平均负载按实际采样间隔计算 `L += (R+D-L) * (1-exp(-dt/τ))`，τ 为 60/300/900 秒；首次采样只建立基线，之后从零预热。它是用户态估算，不等同于主机内核 loadavg，也不按 CPU 配额归一化。容器消失、采样缺失或间隔超过三倍采样周期（默认 45 秒）时清理/重置该容器历史，不以零填充失败；超过三倍采样周期的缓存不导出。暂停或重启 loadavg 会重置平均负载，已有主机 loadavg 不变。Dload 仍使用独立的 `AutoTracing.Dload.Interval`（默认 10 秒）；iterator 仅合并 100ms 内符合条件的请求，不保证每次采样都复用。

### add host D-state counts using the shared task iterator

|指标|意义|单位|对象|标签|备注|
|---|---|---|---|---|---|
|loadavg_nr_uninterruptible|主机参与负载计算的不可中断任务数|计数|物理机| host, region |共享 BPF task iterator|

主机 `loadavg_nr_uninterruptible` 随 `loadavg` 组件启用。它复用 dload 的共享 BPF task iterator，按 loadavg 配置的周期（默认 15 秒）统计全主机参与负载计算的 D 状态任务（排除 `TASK_NOLOAD`/`TASK_FROZEN`），包括非容器任务，不是简单相加容器指标，也不使用 `/proc/stat` 的 `procs_blocked`。在 cgroup v2 上在同一次遍历中汇总；主机统计不受 cgroup v1/v2 模式限制，但需要内核 BTF、BPF task iterator 和主机 PID namespace。不支持时省略该指标，不扫描 `/proc` 任务目录兜底；已有主机 loadavg 和 v1 容器指标不受影响。它是遍历期间的采样值，不是所有任务的原子快照。

## 内存

### 内存回收

内存回收和规整可能阻塞任务。以下指标分别记录宿主机累计耗时和容器直接回收事件数。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `memory_free_allocpages_stall` | 宿主机直接回收累计耗时 | 毫秒 | 宿主机 | eBPF | — |
| `memory_free_compaction_stall` | 宿主机内存规整累计耗时 | 毫秒 | 宿主机 | eBPF | — |
| `memory_reclaim_container_directstall` | 容器直接回收事件次数 | 计数 | 容器 | eBPF | — |
| `memory_reclaim_events_unresolved_suppressed_total` | 一秒输出冷却期间抑制的未归属 reclaim 事件（Counter） | 计数 | 宿主机 | 事件采集器 | — |

`memory_others_container_directstall_time`、`memory_others_container_asyncreclaim_time`、`memory_others_container_local_direct_reclaim_time` 依赖滴滴定制内核的 `memory.directstall_stat`、`memory.asynreclaim_stat`、`memory.local_direct_reclaim_time` 接口。

主线及常见发行版内核缺少这些接口时不会输出对应指标，无需额外加载模块。标准内核上的容器直接回收次数可使用 `memory_reclaim_container_directstall` 观测。

### 内存状态

容器字段来自 `memory.stat`，宿主机字段来自 `/proc/vmstat`，均受 `MetricCollector.Vmstat` 过滤规则影响。字段是否存在及其含义取决于内核和 cgroup 版本。

**容器内存**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_container_active_file` | 活跃文件内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_active_anon` | 活跃匿名内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_inactive_file` | 非活跃文件内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_inactive_anon` | 非活跃匿名内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_file_dirty` | 已修改且还未写入磁盘的文件内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_file_writeback` | 正在写回的文件内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_dirty` | 已修改且还未写入磁盘的内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_writeback` | 正在写回的内存大小 | 字节 | 容器 | — |
| `memory_vmstat_container_pgdeactivate` | 将页面从 active LRU 移动到 inactive LRU 的数量 | 页 | 容器 | — |
| `memory_vmstat_container_pgrefill` | 在 active LRU 链表上被扫描的页面总数 | 页 | 容器 | — |
| `memory_vmstat_container_pgscan_direct` | 直接回收时，在 inactive LRU 上扫描过的页面总数 | 页 | 容器 | — |
| `memory_vmstat_container_pgscan_kswapd` | kswapd 在 inactive LRU 链表上扫描过的页面总数 | 页 | 容器 | — |
| `memory_vmstat_container_pgsteal_direct` | 直接回收时，成功从 inactive LRU 回收的页面总数 | 页 | 容器 | — |
| `memory_vmstat_container_pgsteal_kswapd` | kswapd 成功从 inactive LRU 回收的页面总数 | 页 | 容器 | — |
| `memory_vmstat_container_unevictable` | 不可回收的页面字节数 | 字节 | 容器 | — |

**宿主机内存**

以下名称已包含 `memory_vmstat_` 前缀；底层字段值原样导出，不统一换算为字节。`pgpgin`、`pgpgout` 按内核的 KiB 口径读取，见 [Linux vmstat 实现](https://github.com/torvalds/linux/blob/v6.6/mm/vmstat.c#L1714-L1717)。

**页面状态与 LRU**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_nr_free_pages` | 空闲页面总数（伙伴系统可直接分配）。 | 页 | 宿主机 | — |
| `memory_vmstat_nr_inactive_anon` | 非活跃匿名页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_inactive_file` | 非活跃文件页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_active_anon` | 活跃匿名页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_active_file` | 活跃文件页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_unevictable` | 不可回收页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_mlock` | 被 mlock() 锁定的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_shmem` | tmpfs / shmem 使用的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_slab_reclaimable` | 可回收 Slab 占用的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_slab_unreclaimable` | 不可回收 Slab 占用的页面数 | 页 | 宿主机 | — |

**脏页与写回**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_nr_dirty` | 当前脏页数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_writeback` | 正在写回的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_nr_dirty_threshold` | 触发脏页限流的阈值 | 页 | 宿主机 | — |
| `memory_vmstat_nr_dirty_background_threshold` | 后台写回开始的阈值 | 页 | 宿主机 | — |

**缺页与换页**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_pgfault` | 总缺页异常次数 | 计数 | 宿主机 | — |
| `memory_vmstat_pgmajfault` | 主缺页异常次数 | 计数 | 宿主机 | — |
| `memory_vmstat_pgpgin` | 从块设备读入的数据量 | KiB | 宿主机 | — |
| `memory_vmstat_pgpgout` | 写出到块设备的数据量 | KiB | 宿主机 | — |
| `memory_vmstat_pswpin` | 从 Swap 换入的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pswpout` | 换出到 Swap 的页面数 | 页 | 宿主机 | — |

**回收与扫描**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_pgscan_kswapd` | kswapd 扫描的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pgscan_direct` | 直接回收扫描的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pgscan_khugepaged` | khugepaged 扫描的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pgsteal_kswapd` | kswapd 成功回收的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pgsteal_direct` | 直接回收成功回收的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_pgsteal_khugepaged` | khugepaged 成功回收的页面数 | 页 | 宿主机 | — |

**透明大页（THP）**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_thp_fault_alloc` | 缺页时成功分配 THP 的次数 | 计数 | 宿主机 | — |
| `memory_vmstat_thp_fault_fallback` | 缺页时分配 THP 失败而回落普通页的次数 | 计数 | 宿主机 | — |
| `memory_vmstat_thp_collapse_alloc` | khugepaged 折叠成 THP 的成功次数 | 计数 | 宿主机 | — |
| `memory_vmstat_thp_collapse_alloc_failed` | khugepaged 折叠 THP 的失败次数 | 计数 | 宿主机 | — |

**NUMA**

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_vmstat_numa_hit` | 在首选 NUMA 节点分配成功的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_numa_miss` | 首选其他节点但在本节点分配的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_numa_foreign` | 首选本节点但在其他节点分配的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_numa_local` | 在当前 CPU 所属 NUMA 节点分配的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_numa_other` | 在当前 CPU 所属节点以外分配的页面数 | 页 | 宿主机 | — |
| `memory_vmstat_numa_pages_migrated` | 由于自动 NUMA 平衡而成功迁移的页面总数 | 页 | 宿主机 | — |

参考：

- [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)
- [cgroup v1 内存控制器](https://docs.kernel.org/admin-guide/cgroup-v1/memory.html)
- [透明大页](https://docs.kernel.org/admin-guide/mm/transhuge.html)

### 内存事件

以下字段来自 `memory.events`，记录回收、内存限制和 OOM 事件。主线内核在 cgroup v2 中提供该接口；滴滴定制内核也可在 cgroup v1 中提供。接口缺失时不输出。各字段的统计条件不同，不能相加作为独立事件总数。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `memory_events_container_low` | 低于 memory.low 仍因压力发生回收的次数，通常表示低水位保护过量配置 | 计数 | 容器 | — |
| `memory_events_container_high` | 超过 memory.high 后被节流并进入直接回收的次数 | 计数 | 容器 | — |
| `memory_events_container_max` | 内存使用量即将超过 memory.max 的次数 | 计数 | 容器 | — |
| `memory_events_container_oom` | 内存达到限制、分配即将失败并考虑 OOM 处理的次数 | 计数 | 容器 | — |
| `memory_events_container_oom_kill` | 属于该 cgroup、被任意 OOM killer 杀死的进程数 | 计数 | 容器 | — |
| `memory_events_container_oom_group_kill` | 发生组 OOM kill 的次数 | 计数 | 容器 | — |

### 空闲内存块

展示每个 NUMA 节点、内存区域和阶数的空闲内存块数量。`order=n` 表示每块包含 `2^n` 个连续页面；指标值是块数，不是页面数。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `memory_buddyinfo_blocks` | 指定 NUMA 节点、内存区域和阶数的空闲块数 | 块 | 宿主机 | procfs | node, order, zone |

### add container reclaim and compaction stall times

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|memory_free_container_allocpages_stall|容器任务参与全局直接回收的累计耗时| 毫秒|容器| eBPF | container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_free_container_compaction_stall|容器任务参与直接内存规整的累计耗时| 毫秒|容器| eBPF | 同上|

主机两项原本就导出毫秒，本次仅修正文档，不改名称、数值缩放或 Gauge 类型。容器两项在操作入口记录任务实际生效的 memory cgroup 的完整 kernfs ID，与现有 cgroup v1/v2 容器发现结果匹配。该 ID 包含 cgroup 代际信息，避免 CSS 地址复用后将旧值归属到其他 cgroup；仅替换 memory CSS 而不重建 cgroup 目录时，保留同一 cgroup 的身份和累计值。这两项表示任务承受的全局直接回收/规整耗时，不是为该容器回收了多少内存，也不是 memcg 限额回收次数 `memory_reclaim_container_directstall`。主机总量包含未归属容器的任务，不能和容器指标相加。

仅为存在 BPF 记录且能匹配已发现普通容器的 cgroup 导出，不为缺失容器补零；容器发现或容器 map 读取失败时仍导出主机值并报告错误。ID 解析时忽略采集中容器消失产生的 ENOENT；其他解析错误会上报，同时保留主机值与成功匹配的容器值。计时表与容器累计表各限制 10240 条 LRU 记录，压力下可能丢失计时或淘汰累计值，BPF 重载也会归零，分析增量需处理重置。容器采集仅使用既有回收 tracepoint 和规整 kprobe；kernfs ID 匹配不需要 `cgroup_mkdir` 清理探针。所有数值为采集期间累计值，并非机器启动以来的绝对累计。

启动时若完整 BPF 对象加载或挂载失败，释放原对象后仅重试一次主机模式：将 BPF 常量 `enable_container_stalls` 设为 0，关闭容器 cgroup 读取与计数，沿用同一组探针，保留原有两项主机耗时指标并记录降级告警；不导出容器零值，不调整默认过滤配置。主机模式仍要求可用的 BPF/BTF、回收 tracepoint 和规整 kprobe，不能绕过这些基础依赖；主机模式也失败则报告启动错误。

> **注意**：`memory_others_container_directstall_time`、`memory_others_container_asyncreclaim_time`、`memory_others_container_local_direct_reclaim_time` 指标读取的是厂商定制内核提供的 memory cgroup 扩展接口（`memory.directstall_stat`、`memory.asynreclaim_stat`、`memory.local_direct_reclaim_time`）。主线内核及常见发行版内核不提供这些接口，因此这些指标不会输出，属预期行为，无需额外加载内核模块。在标准内核上观测容器直接回收（direct reclaim）行为，请使用上表基于 eBPF 实现的 `memory_reclaim_container_directstall`。

### add a host-wide memcg reclaim count

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|memory_reclaim_directstall|整台主机 memcg 限额回收累计次数| 计数| 物理机| eBPF | host, region|

`memory_reclaim_directstall` 与容器计数共用 `mm_vmscan_memcg_reclaim_begin`，均排除 kswapd；在容器 CSS 匹配前累加 per-CPU 主机计数，采集时求和。它覆盖整台主机上的 memcg 回收（包括未被发现为普通容器的 cgroup），不是容器指标求和，也不是全局直接回收次数或耗时；没有 memcg 回收时为零。主机计数不受容器删除或容器计数表容量影响，BPF 重载归零。沿用 Gauge 类型及现有挂点要求，不新增探针、不调整默认过滤配置；主机和容器采集失败相互隔离，失败的一侧不伪造零值。

## 网络

### TCP 内存

记录 TCP 内存使用量及其相对限制的占比。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `tcp_memory_limit_pages` | 系统可使用的 TCP 总内存大小 | 页 | 宿主机 | — |
| `tcp_memory_usage_bytes` | 系统已使用的 TCP 内存大小 | 字节 | 宿主机 | — |
| `tcp_memory_usage_pages` | 系统已使用的 TCP 内存大小 | 页 | 宿主机 | — |
| `tcp_memory_usage_percent` | 系统已使用的 TCP 内存百分比（相对 TCP 内存总限制） | % | 宿主机 | — |

### 连接跟踪

宿主机 nf_conntrack 表的占用与容量，分别来自
`/proc/sys/net/netfilter/nf_conntrack_count` 与
`/proc/sys/net/netfilter/nf_conntrack_max`。仓库配置默认通过 `BlackList` 禁用 `net_conntrack`。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `net_conntrack_entry_count` | 当前 nf_conntrack 表中的条目数 | 个 | 宿主机 | — |
| `net_conntrack_entry_max` | nf_conntrack 表可容纳的最大条目数 | 个 | 宿主机 | — |

### ARP 邻居项

按宿主机和容器网络命名空间统计 ARP 邻居项。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `arp_entries` | 宿主机网络命名空间 ARP 条目数量 | 计数 | 宿主命名空间 | — |
| `arp_total` | 宿主机所有网络命名空间 ARP 条目数量总和 | 计数 | 宿主机 | — |
| `arp_container_entries` | 容器网络命名空间 ARP 条目数量 | 计数 | 容器 | — |

### 发送队列

Qdisc 指标描述发送队列积压、发送量、丢包和重入队次数，不直接提供排队延迟。仅采集匹配设备的根 Qdisc，跳过 `noqueue`。仓库配置默认通过 `BlackList` 禁用 `netdev_qdisc`。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `netdev_qdisc_backlog` | 排队待发送的字节数 | 字节 | 宿主机 | device, kind |
| `netdev_qdisc_current_queue_length` | 当前排队报文数 | 计数 | 宿主机 | device, kind |
| `netdev_qdisc_overlimits_total` | 超限次数 | 计数 | 宿主机 | device, kind |
| `netdev_qdisc_requeues_total` | 由于网卡/驱动暂时无法发送而被重新入队的次数 | 计数 | 宿主机 | device, kind |
| `netdev_qdisc_drops_total` | 主动丢弃的包数（因队列满、限速策略等原因） | 计数 | 宿主机 | device, kind |
| `netdev_qdisc_bytes_total` | 累计发送字节数 | 字节 | 宿主机 | device, kind |
| `netdev_qdisc_packets_total` | 已发送的包数 | 计数 | 宿主机 | device, kind |

### 硬件丢包

统计支持的网卡驱动在接收方向的硬件丢包。优先使用 `rx_missed_errors`，否则尝试从 `rx_dropped` 中扣除 eBPF 统计的软件丢包。仓库配置默认通过 `BlackList` 禁用 `netdev_hw`。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `netdev_hw_rx_dropped_total` | 接收方向硬件丢包累计数 | 计数 | 宿主机 | sysfs、eBPF | device, driver |

### 网卡统计

通过 proc 或 netlink 采集网卡统计，公共指标遵循 [Linux `/proc/net/dev`](https://github.com/torvalds/linux/blob/v6.6/net/core/net-procfs.c#L72-L92) 的统计口径：

- `receive_dropped`：`rx_dropped + rx_missed_errors`。
- `receive_frame_errors`：`rx_length_errors + rx_over_errors + rx_crc_errors + rx_frame_errors`。
- `transmit_carrier_errors`：`tx_carrier_errors + tx_aborted_errors + tx_window_errors + tx_heartbeat_errors`。

netlink 还提供细分错误指标和 `receive_nohandler`，细分错误可能已包含在上述聚合值中，不能与聚合值重复相加。表中列宿主机名称；容器名称在 `netdev_` 后增加 `container_`。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `netdev_receive_bytes_total` | 成功接收的总字节数 | 字节 | 宿主机、容器 | device |
| `netdev_receive_packets_total` | 成功接收的数据包总数 | 计数 | 宿主机、容器 | device |
| `netdev_receive_multicast_total` | 接收的组播包数 | 计数 | 宿主机、容器 | device |
| `netdev_receive_compressed_total` | 接收到的已压缩数据包数 | 计数 | 宿主机、容器 | device |
| `netdev_receive_frame_errors_total` | 接收长度、溢出、CRC 和帧对齐错误总数 | 计数 | 宿主机、容器 | device |
| `netdev_receive_errors_total` | 接收错误总数 | 计数 | 宿主机、容器 | device |
| `netdev_receive_dropped_total` | 接收丢包数，包含 missed errors | 计数 | 宿主机、容器 | device |
| `netdev_receive_fifo_errors_total` | 接收 FIFO/环形缓冲区溢出错误数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_bytes_total` | 成功发送的总字节数 | 字节 | 宿主机、容器 | device |
| `netdev_transmit_packets_total` | 成功发送的数据包总数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_collisions_total` | 发送冲突次数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_errors_total` | 发送错误总数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_dropped_total` | 发送过程中被丢弃的包数（队列满、策略丢弃等） | 计数 | 宿主机、容器 | device |
| `netdev_transmit_fifo_errors_total` | 发送 FIFO/环形缓冲区错误数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_carrier_errors_total` | 发送载波、中止、窗口和心跳错误总数 | 计数 | 宿主机、容器 | device |
| `netdev_transmit_compressed_total` | 发送的已压缩数据包数 | 计数 | 宿主机、容器 | device |

### TCP 统计

读取网络命名空间的 `/proc/<pid>/net/netstat` 和 `snmp`，受 `MetricCollector.Netstat` 过滤规则影响。表中列宿主机名称；容器名称在 `netstat_` 后增加 `container_`。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `netstat_TcpExt_ArpFilter` | 因 ARP 过滤规则而被丢弃的数据包数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_BusyPollRxPackets` | 通过 busy polling 机制接收到的数据包数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_DelayedACKLocked` | 延迟 ACK 定时器到期，但 Socket 被用户态锁定而推迟发送的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_DelayedACKLost` | 收到已确认报文的次数，可能由 ACK 丢失或网络重复报文导致 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_DelayedACKs` | 延迟 ACK 定时器到期并发送 ACK 的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_EmbryonicRsts` | 在 SYN_RECV 状态收到带 RST/SYN 标记的包个数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_ListenDrops` | LISTEN 状态 Socket 丢弃报文的次数，包括接受队列溢出、内存分配失败等情况 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_ListenOverflows` | TCP 接受队列溢出次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_OfoPruned` | 乱序队列因内存不足被修剪的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_OutOfWindowIcmps` | 收到的与当前 TCP 窗口无关的 ICMP 错误报文数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_PruneCalled` | 因内存不足触发缓存清理的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_RcvPruned` | 接收队列因内存不足被修剪（丢弃数据包）的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_SyncookiesFailed` | 验证失败的 SYN cookie 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_SyncookiesRecv` | 接收的 SYN cookie 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_SyncookiesSent` | 发送的 SYN cookie 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPACKSkippedChallenge` | 在处理 Challenge ACK 过程中跳过的其他 ACK 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPACKSkippedFinWait2` | 在 FIN-WAIT-2 状态下跳过的 ACK 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPACKSkippedPAWS` | 因 PAWS 检查失败而跳过的 ACK 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPACKSkippedSeq` | 因为序列号检查而跳过的 ACK 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPACKSkippedTimeWait` | 在 TIME-WAIT 状态下跳过的 ACK 数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPAbortOnClose` | 应用关闭连接时仍有未读取数据而中止连接的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPAbortOnData` | 仍有在途数据时中止连接并发送 RST 的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPAbortOnLinger` | 在 LINGER 状态下等待超时后中止连接的数量 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPAbortOnMemory` | 因内存问题关闭连接的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPAbortOnTimeout` | 因各种计时器的重传次数超过上限而关闭连接的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPLossFailures` | 丢失数据包而进行恢复失败的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPLossProbeRecovery` | 通过 TLP 检测并恢复丢包的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPLossProbes` | 发送尾部丢包探测（TLP）报文的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPLossUndo` | 在恢复过程中检测到丢失而撤销的次数 | 计数 | 宿主机、容器 | — |
| `netstat_TcpExt_TCPLostRetransmit` | 丢包重传的数量 | 计数 | 宿主机、容器 | — |

表中仅列部分 TcpExt 字段；其余字段及版本差异见 Linux 内核文档。

参考：

- [TCP/SNMP 计数器](https://docs.kernel.org/networking/snmp_counter.html)

### Socket 使用量

读取网络命名空间的 `/proc/<pid>/net/sockstat`。表中列宿主机名称；容器名称在 `sockstat_` 后增加 `container_`。`mem`、`mem_bytes` 仅在宿主机输出。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `sockstat_sockets_used` | 当前使用的 Socket 数 | 计数 | 宿主机、容器 | — |
| `sockstat_TCP_inuse` | 当前处于 TCP 连接状态（如 ESTABLISHED、LISTEN 等，除 TIME_WAIT 外）的 socket 数量 | 计数 | 宿主机、容器 | — |
| `sockstat_TCP_orphan` | 不再关联用户态文件句柄的 TCP Socket 数 | 计数 | 宿主机、容器 | — |
| `sockstat_TCP_tw` | 当前处于 TIME_WAIT 状态的 TCP Socket 数量 | 计数 | 宿主机、容器 | — |
| `sockstat_TCP_alloc` | 当前已分配的 TCP Socket 对象总数 | 计数 | 宿主机、容器 | — |
| `sockstat_TCP_mem` | TCP 套接字当前占用的内核内存页数 | 页 | 宿主机 | — |
| `sockstat_UDP_inuse` | 当前已绑定了本地端口的 UDP Socket 数量 | 计数 | 宿主机、容器 | — |

## I/O

### 配置

`iotracing`、`iolatency`、`blk_throtl`、`iocost` 和 `io_health` 默认启用。
向全局 `BlackList` 添加对应名称并重启 Huatuo 可关闭该功能。
关闭 `iotracing` 会同时停止磁盘指标和自动诊断；关闭 `io_health` 会停止
健康事件、取证及其指标，不影响 `iotracing`。

### 磁盘性能

通过以下指标观测磁盘性能。指标使用 `huatuo_bamai_iotracing_` 前缀，
均为区间 Gauge，带 `disk` 标签。

|指标后缀|含义|
|---|---|
|`read_bps`、`write_bps`|读写吞吐量，单位字节/秒|
|`read_iops`、`write_iops`|每秒读写操作数|
|`read_await_ms`、`write_await_ms`|读写平均完成时间，单位毫秒|
|`io_util_percent`|磁盘忙碌时间占比|
|`average_queue_size`|平均 IO 队列长度|

指标覆盖整盘和支持的逻辑设备，不包含分区及 loop、ram、zram、fd 等伪设备。

### 延时与 IO 大小分布

`iolatency` 统计 SCSI/SAS、NVMe、virtio 磁盘及容器读写 IO 的延时和大小分布。
仅采集启动时已开启 `iostats` 和请求时间统计的磁盘，运行中不支持关闭这些配置。
启动时若软件 blk-crypto fallback 已初始化，整个 `iolatency` 不启动。

SCSI 下发大小沿用内核的 16 位扇区计数，达到 32 MiB 会回绕。
跨 blkcg 合并 request 时，容器下发大小为近似归属，宿主机统计不受影响。

块设备增删或重建时，`iolatency`、`blk_throtl` 和 `iocost` 重新开始统计，
过渡期间的所有磁盘数据可能缺失。

桶值为采集区间内小于等于 `le` 上界的 IO 数量，`+Inf` 为总数；延时边界
单位为秒，大小边界单位为字节。指标为区间 Gauge。

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

|指标|意义|单位|对象|
|---|---|---|---|
|iolatency_q2d_seconds_bucket|入队到最后一次下发的延时分布|计数|宿主|
|iolatency_d2c_seconds_bucket|最后一次下发到完成的延时分布|计数|宿主|
|iolatency_q2g_seconds_bucket|入队到 request 起始时间的延时分布|计数|宿主|
|iolatency_container_q2d_seconds_bucket|入队到最后一次下发的延时分布|计数|容器|
|iolatency_container_d2c_seconds_bucket|最后一次下发到完成的延时分布|计数|容器|
|iolatency_container_q2g_seconds_bucket|入队到 request 起始时间的延时分布|计数|容器|
|iolatency_queued_io_size_bytes_bucket|入队 bio 大小分布|计数|宿主|
|iolatency_issued_io_size_bytes_bucket|request 大小分布；SCSI 取最后一次下发的完整大小，NVMe/virtio 取完成字节数|计数|宿主|
|iolatency_container_queued_io_size_bytes_bucket|入队 bio 大小分布|计数|容器|
|iolatency_container_issued_io_size_bytes_bucket|request 大小分布；SCSI 取最后一次下发的完整大小，NVMe/virtio 取完成字节数|计数|容器|
|iolatency_interval_start_timestamp_seconds|本区间开始时间|Unix 秒|宿主|
|iolatency_interval_end_timestamp_seconds|本区间结束时间|Unix 秒|宿主|

17 个延时桶界（秒）为 `0.00005`、`0.0001`、`0.00025`、`0.0005`、
`0.001`、`0.002`、`0.004`、`0.008`、`0.016`、`0.032`、`0.064`、`0.128`、
`0.256`、`0.512`、`1`、`2`、`+Inf`。

#### Grafana

IO 总览展示各采集区间的延时分布和 IO 数量，点击柱状图可查看该时段的
延时与大小直方图。面板使用 Business Charts（`volkovlabs-echarts-panel`）。

#### 旧指标变更指南

升级后需更新自定义面板和告警，以下省略 `huatuo_bamai_` 前缀：

|旧指标|替换方式|
|---|---|
|`iolatency_blkdisk_d2c`|`iolatency_d2c_seconds_bucket`|
|`iolatency_container_blkdisk_d2c`|`iolatency_container_d2c_seconds_bucket`|
|`iolatency_blkdisk_q2c`、`iolatency_container_blkdisk_q2c`|无直接等价指标，按所需阶段选择 Q2G、Q2D 或 D2C|
|`iolatency_blkdisk_freeze`|`io_health_blkdisk_freeze`，仍为 Counter|

`disk` 从 `8:0` 等设备号改为 `sda` 等磁盘名，新增 `operation` 区分读写。
旧 `zone` 编号改为以秒为单位的 `le` 累计上界；桶界见上文，不能按编号直接替换。
统计 `(a, b]` 范围的数量时，用 `le=b` 的值减去 `le=a` 的值。

旧延时指标仅统计 20 ms 及以上的 IO；新指标覆盖支持盘的全部读写 IO，
D2C 从最后一次下发计时。旧告警需按新的阶段和桶界调整，不能只改指标名称。

新延时桶由累积 Counter 改为区间 Gauge，不能继续使用 `rate()` 或 `increase()`。
单个采集区间直接查询；例如，统计最近五分钟某盘读 IO 的 D2C 样本总数：

```promql
sum_over_time(huatuo_bamai_iolatency_d2c_seconds_bucket{disk="sda",operation="read",le="+Inf"}[5m])
```

### 硬件

```bash
# HELP huatuo_bamai_io_health_blkdisk_freeze the disk freeze event count
# TYPE huatuo_bamai_io_health_blkdisk_freeze counter
huatuo_bamai_io_health_blkdisk_freeze{disk="sda",host="hostname",region="dev"} 0
```

|指标|意义|单位|对象|标签|
|---|---|---|---|---|
|io_health_blkdisk_freeze|宿主机磁盘 freeze 次数，换盘清零|计数|宿主|host, region, disk|

可在宿主机仪表盘的 `Overview → Selected Metric` 中选择此指标。

### blk-throttle 排队等待

`blk_throtl` 统计 IO 因 blk-throttle 限速而排队的数量和平均等待时间。
每个 IO 结束排队后计数一次，等待时间从首次排队算起。

|指标|含义|
|---|---|
|`blk_throtl_delayed_io_count`|采集区间内完成等待的 IO 数量|
|`blk_throtl_average_wait_ms`|这些 IO 的平均排队时间，单位毫秒|
|`blk_throtl_container_delayed_io_count`|容器采集区间内完成等待的 IO 数量|
|`blk_throtl_container_average_wait_ms`|容器 IO 的平均排队时间，单位毫秒|

指标按 `disk` 和 `operation`（`read` 或 `write`）区分，`scope=host`
表示宿主机总量，`scope=other` 表示其中无法归属容器的部分。
容器指标使用容器标签，不带 `scope`。四项均为区间 Gauge。

容器发现可选，宿主机采集不依赖 kubelet。容器查询失败或标签无效时，本轮数据
归入 `other`；关联恢复后不补报历史数据。临时采集失败后，首次成功采集仅重建
基线，下一次成功采集恢复输出，不补报失败区间。若错误导致采集停止，需排障后
重启 Huatuo。

cgroup 实际释放后，尚未采集的尾部等待增量归入 Host 和 Other，不再关联容器。
两次成功采集之间，同一统计项在单 CPU 上完成至少 67,108,864 次等待，或累计
等待约 274.9 万秒时，计数回绕可能导致少计。

### IOCOST 等待统计

`iocost` 统计预算不足造成的等待。等待时间从 IO 进入等待队列算到最终被唤醒，
计入等待结束所在的采集区间。

|指标|含义|
|---|---|
|`iocost_waitq_io_count`|本轮完成等待的读写 IO 数量|
|`iocost_average_wait_ms`|这些 IO 的平均等待时间，单位毫秒|
|`iocost_container_waitq_io_count`|容器本轮完成等待的读写 IO 数量|
|`iocost_container_average_wait_ms`|容器 IO 的平均等待时间，单位毫秒|

这些区间 Gauge 按 `disk`（如 `sda`、`nvme0n1`）和 `operation`
（`read`、`write`）分组。`scope=host` 表示设备总量，包含所有
容器；`scope=other` 表示其中未归属容器的部分。

容器发现可选，宿主机采集不依赖 kubelet。容器查询失败或标签无效时，本轮数据
归入 `other`；关联恢复后不补报历史数据。临时采集失败后，首次成功采集仅重建
基线，下一次成功采集恢复输出，不补报失败区间。若错误导致采集停止，需排障后
重启 Huatuo。

cgroup 实际释放后，尚未采集的尾部等待增量归入 Host 和 Other，不再关联容器。
两次成功采集之间，同一统计项在单 CPU 上完成至少 67,108,864 次等待，或累计
等待约 274.9 万秒时，计数回绕可能导致少计。例如，同一统计项的 10,240 次等待
各持续 270 秒、在同一区间由同一 CPU 结算，会超出等待时间容量。

采集需要支持 IOCOST 的非 PREEMPT_RT Linux x86_64 内核，具备 BPF/BTF 和
kprobe 能力。

### 设备健康

`io_health` 记录以下事件：

|事件类型|触发条件|
|---|---|
|`block_error`|普通 block 请求完成时返回错误|
|`nvme_timeout`|NVMe 请求超时|
|`nvme_reset`|NVMe 控制器已接纳重置请求，不代表重置完成；仅记录事件，不触发取证|
|`nvme_state_change`|NVMe controller 状态发生变化|
|`scsi_timeout`|SCSI command 超时|
|`scsi_dispatch_error`|SCSI command 派发失败|
|`md_sync_action`|MD 同步动作发生变化|
|`md_degraded`|MD 不可用成员数发生变化|
|`md_member_state`|MD 成员状态发生变化或成员消失|

NVMe 状态名不可用时，`new_state` 为 `unknown`，`new_state_raw` 保留状态码。

block 错误、NVMe 超时、SCSI 异常及 MD 成员进入 `faulty`、`blocked`、
`write_error` 或 `removed` 状态会触发只读健康取证：NVMe 使用 `nvme-cli`，
SCSI 使用 `smartctl`，结果随事件保存。无法唯一确定目标时仅保留事件，不扫描
其他设备。MD 监测不需要 `mdadm`。
事件中的 `device` 可保留 MD 成员的分区名；取证目标归整为唯一物理磁盘或
NVMe controller。
同设备取证执行中及结束后的 60 秒冷却期内不重复取证，原始事件仍独立保存。

`huatuo_bamai_io_health_` Counter 记录进程启动后观察到的事件：

|指标后缀|标签|含义|
|---|---|---|
|`block_errors_total`|disk、operation、status|block 层错误次数|
|`nvme_timeouts_total`|disk|NVMe timeout 次数|
|`nvme_resets_total`|disk|NVMe 已接纳的重置请求次数|
|`scsi_timeouts_total`|disk|SCSI timeout 次数|
|`scsi_dispatch_errors_total`|disk、status|SCSI 派发失败次数|
|`collection_errors_total`|disk、reason|健康取证失败次数|
|`event_persistence_failures_total`|reason|事件保存返回错误的次数|

MD 和 NVMe controller 状态变化以事件保存，不提供当前状态 Gauge。
Counter 先于事件保存更新；保存失败计入
`event_persistence_failures_total{reason="save_error"}`。

健康事件单独保存为文档，不可获得或不适用的可选字段省略：

|字段|含义|
|---|---|
|`type`|事件类型|
|`device`|事件设备名；MD 成员事件可保留分区名|
|`array`、`member`|MD 阵列和成员名|
|`old_state`、`new_state`|MD 状态变化；NVMe 仅提供新状态|
|`new_state_raw`|NVMe controller 状态码|
|`operation`、`io_error_status`、`sector`|block 操作、错误及起始扇区|
|`collection_status`|`ok`、`partial`、`unsupported`、`timeout` 或 `error`|
|`nvme`、`scsi`|事件触发后采集的协议健康证据|

NVMe 证据包括 `critical_warning`、`media_errors_total`，以及最多八条包含
状态码、NSID 和 LBA 的错误日志。SCSI 证据包括整体健康状态、ASC/ASCQ、温度、
缺陷数量、读/写/校验错误计数，以及待处理缺陷数量和最多八个示例 LBA。

### 磁盘统计

`diskio` 读取 `/proc/diskstats` 和 `/proc/stat`，提供设备级请求数、吞吐量、累计耗时和宿主机 CPU iowait。

磁盘名标签由 `device` 改为 `disk`，自定义查询的筛选和图例需同步更新；
指标名称和类型不变。

仓库配置默认通过 `BlackList` 禁用 `diskio`。从黑名单移除并重启 `huatuo-bamai` 服务后启用。

请求数、字节数和 I/O 耗时为累计 Counter，可用 `rate()` 计算 IOPS、吞吐量和耗时增长速率。平均请求耗时为耗时增长速率除以请求速率，计算时需排除请求速率为零的区间。`io_in_progress` 和 `disk_iowait_percent` 为 Gauge。

| 指标 | 说明 | 单位 | 范围 | 附加标签 |
| --- | --- | --- | --- | --- |
| `diskio_read_requests_total` | 累积读请求完成数，使用 `rate()` 计算读 IOPS | 计数 | 宿主机 | disk |
| `diskio_write_requests_total` | 累积写请求完成数，使用 `rate()` 计算写 IOPS | 计数 | 宿主机 | disk |
| `diskio_read_bytes_total` | 累积读取字节数，使用 `rate()` 计算读吞吐量 | 字节 | 宿主机 | disk |
| `diskio_written_bytes_total` | 累积写入字节数，使用 `rate()` 计算写吞吐量 | 字节 | 宿主机 | disk |
| `diskio_read_time_seconds_total` | 已完成读请求的累积耗时 | 秒 | 宿主机 | disk |
| `diskio_write_time_seconds_total` | 已完成写请求的累积耗时 | 秒 | 宿主机 | disk |
| `diskio_io_in_progress` | 当前正在进行的 I/O 请求数，即队列深度 | 计数 | 宿主机 | disk |
| `diskio_disk_iowait_percent` | 采集区间内 CPU 等待 I/O 完成的时间占比 | % | 宿主机 | — |

### XFS

`xfs` 从 sysfs 采集宿主机各设备的统计，附加标签为 `device`。`xfs_log_free_bytes` 为 Gauge，名称以 `_total` 结尾的指标为累计 Counter。

| 指标 | 说明 | 单位 |
| --- | --- | --- |
| `xfs_log_free_bytes` | 日志剩余可用空间 | 字节 |
| `xfs_log_space_sleep_total` | 等待日志空间的累计次数 | 计数 |
| `xfs_alloc_blocks_total` | 累计分配块数 | 块 |
| `xfs_alloc_extents_total` | 累计分配 extent 数 | 计数 |
| `xfs_inode_attempts_total` | inode 缓存累计请求数 | 计数 |
| `xfs_inode_missed_total` | inode 缓存累计未命中次数 | 计数 |
| `xfs_buf_busy_locked_total` | 缓冲区忙锁事件累计次数 | 计数 |
| `xfs_buf_locked_waited_total` | 等待缓冲区锁的累计次数 | 计数 |

无法获取日志可用空间时不输出 `xfs_log_free_bytes`。新增 XFS 挂载后需重启 `huatuo-bamai` 服务。

## 系统事件

### 软锁死

记录内核检测到的 soft lockup 事件。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `softlockup_total` | 系统 softlockup 事件计数 | 计数 | 宿主机 | eBPF | — |

### 任务阻塞

记录内核检测到的 hung task 事件。

| 指标 | 说明 | 单位 | 范围 | 来源 | 附加标签 |
| --- | --- | --- | --- | --- | --- |
| `hungtask_total` | 系统 hungtask 事件计数 | 计数 | 宿主机 | eBPF | — |

### attribute hungtask events and counts to containers

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|hungtask_container_total|已归属容器的阻塞任务事件计数|计数|容器|BPF + cgroup 文件句柄|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

容器归属通过 `sched_process_hang` raw tracepoint，在事件时刻读取目标阻塞任务（不是 `khungtaskd`）的 cgroup 身份。cgroup v1/Hybrid 使用 CPU 层级，v2 使用统一层级；携带最近的最多 16 层非根 cgroup 完整 kernfs ID（含代次），与已发现普通容器的 cgroup 文件句柄匹配，优先归属最近容器，支持容器子 cgroup。复用现有 cgroup ID 读取路径，不再事后查询 `/proc/<tid>/cgroup`，避免任务退出、TID 复用和后续迁移改变归属。

容器元数据消失、文件句柄不可读、身份读取失败，或容器根超出 16 层范围且无法匹配时，只保留主机总计，缺失容器不补零。raw tracepoint 加载或挂载失败时，清理后重试原有普通 tracepoint，仅采集主机事件并告警；不回退到可能误归属的 TID 查询。主机路径本身仍需要原有 BPF/BTF 和 hungtask tracepoint 支持。事件身份更精确不代表容器元数据发现一定成功，诊断堆栈仍是主机范围。

两项均保留 Counter 语义，计数发生在追踪退避之前；追踪仍沿用主机级退避和全机栈快照，已匹配的记录附带 `ContainerID`，不表示栈已按容器过滤。容器指标使用生命周期内累计计数，成功发现容器退出后清理，进程重启归零。发现故障不丢主机指标，也不清空已累计的容器状态。主机与容器计数不能相加，重复上报同一阻塞线程仍按多次事件计数。

## GPU

### MetaX

仓库配置默认通过 `BlackList` 禁用 `metax_gpu`，启用前需满足对应设备和 SDK 依赖。

| 指标 | 说明 | 单位 | 附加标签 |
| --- | --- | --- | --- |
| `metax_gpu_sdk_info` | GPU SDK 信息 | — | version |
| `metax_gpu_driver_info` | GPU 驱动信息 | — | version |
| `metax_gpu_info` | GPU 基本信息 | — | gpu, model, uuid, bios_version, bdf, mode, die_count |
| `metax_gpu_board_power_watts` | GPU 板级功耗 | W | gpu |
| `metax_gpu_pcie_link_speed_gt_per_second` | GPU PCIe 当前链路速率 | GT/s | gpu |
| `metax_gpu_pcie_link_width_lanes` | GPU PCIe 当前链路宽度 | 通道数 | gpu |
| `metax_gpu_pcie_receive_bytes_per_second` | GPU PCIe 接收吞吐率 | B/s | gpu |
| `metax_gpu_pcie_transmit_bytes_per_second` | GPU PCIe 发送吞吐率 | B/s | gpu |
| `metax_gpu_metaxlink_link_speed_gt_per_second` | GPU MetaXLink 当前链路速率 | GT/s | gpu, metaxlink |
| `metax_gpu_metaxlink_link_width_lanes` | GPU MetaXLink 当前链路宽度 | 通道数 | gpu, metaxlink |
| `metax_gpu_metaxlink_receive_bytes_per_second` | GPU MetaXLink 接收吞吐率 | B/s | gpu, metaxlink |
| `metax_gpu_metaxlink_transmit_bytes_per_second` | GPU MetaXLink 发送吞吐率 | B/s | gpu, metaxlink |
| `metax_gpu_metaxlink_receive_bytes_total` | GPU MetaXLink 接收数据总量 | 字节 | gpu, metaxlink |
| `metax_gpu_metaxlink_transmit_bytes_total` | GPU MetaXLink 发送数据总量 | 字节 | gpu, metaxlink |
| `metax_gpu_metaxlink_aer_errors_total` | GPU MetaXLink AER 错误次数 | 计数 | gpu, metaxlink, error_type |
| `metax_gpu_status` | GPU 状态 | — | gpu, die |
| `metax_gpu_temperature_celsius` | GPU 温度 | °C | gpu, die |
| `metax_gpu_utilization_percent` | GPU 利用率（0–100） | % | gpu, die, ip |
| `metax_gpu_memory_total_bytes` | 显存总容量 | 字节 | gpu, die |
| `metax_gpu_memory_used_bytes` | 已使用显存容量 | 字节 | gpu, die |
| `metax_gpu_clock_mhz` | GPU 时钟频率 | MHz | gpu, die, ip |
| `metax_gpu_clocks_throttling` | GPU 时钟降频原因 | — | gpu, die, reason |
| `metax_gpu_dpm_performance_level` | GPU DPM 性能等级 | — | gpu, die, ip |
| `metax_gpu_ecc_memory_errors_total` | GPU ECC 内存错误次数 | 计数 | gpu, die, memory_type, error_type |
| `metax_gpu_ecc_memory_retired_pages_total` | GPU ECC 内存退役页数 | 计数 | gpu, die |

### MThreads

摩尔线程 GPU 采集依赖 MTML 管理库，启动时依次查找 `libmtml.so.2`、`libmtml.so`。

从 `BlackList` 移除 `mthreads_gpu` 并重启 `huatuo-bamai` 服务后启用。`MetricCollector.Mthreads` 中的 `EnableHealth`、`EnablePCIe`、`EnableMTLink` 控制对应指标组。

| 指标 | 说明 | 单位 | 附加标签 |
| --- | --- | --- | --- |
| `mthreads_gpu_library_info` | MTML 库信息 | — | version |
| `mthreads_gpu_device_info` | GPU 信息 | — | gpu, name, brand, serial |
| `mthreads_gpu_device_pci_info` | GPU PCI 信息 | — | gpu, sbdf |
| `mthreads_gpu_device_spec` | GPU 规格 | — | gpu, cores |
| `mthreads_gpu_device_bios_version` | GPU BIOS 版本 | — | gpu, bios |
| `mthreads_gpu_device_musa_capability` | GPU MUSA 计算能力 | — | gpu, musa_capability |
| `mthreads_gpu_memory_info` | GPU 内存信息 | — | gpu, type, vendor, bus_width |
| `mthreads_gpu_pcie_link_max_speed_gt_per_sec` | GPU PCIe 最大链路速率（硬件能力） | GT/s | gpu |
| `mthreads_gpu_pcie_link_max_width_lanes` | GPU PCIe 最大链路宽度（硬件能力） | 通道数 | gpu |
| `mthreads_gpu_device_power_watts` | GPU 设备功耗 | W | gpu |
| `mthreads_gpu_gpu_temperature_celsius` | GPU 温度 | °C | gpu |
| `mthreads_gpu_memory_temperature_celsius` | GPU 内存温度 | °C | gpu |
| `mthreads_gpu_gpu_utilization_percent` | GPU 利用率（0-100） | % | gpu |
| `mthreads_gpu_memory_utilization_percent` | GPU 内存利用率（0-100） | % | gpu |
| `mthreads_gpu_memory_total_bytes` | GPU 总内存 | 字节 | gpu |
| `mthreads_gpu_memory_used_bytes` | GPU 已使用内存 | 字节 | gpu |
| `mthreads_gpu_gpu_clock_mhz` | GPU 时钟频率 | MHz | gpu |
| `mthreads_gpu_gpu_max_clock_mhz` | GPU 最大时钟频率 | MHz | gpu |
| `mthreads_gpu_memory_clock_mhz` | GPU 内存时钟频率 | MHz | gpu |
| `mthreads_gpu_memory_max_clock_mhz` | GPU 内存最大时钟频率 | MHz | gpu |
| `mthreads_gpu_gpu_voltage_volts` | GPU 电压 | V | gpu |
| `mthreads_gpu_gpu_power_limit_watts` | GPU 强制功耗限制 | W | gpu |
| `mthreads_gpu_gpu_power_default_limit_watts` | GPU 默认功耗管理限制 | W | gpu |
| `mthreads_gpu_fan_rpm` | GPU 风扇转速 | RPM | gpu, fan |
| `mthreads_gpu_fan_speed_percent` | GPU 风扇速度百分比 | % | gpu, fan |
| `mthreads_gpu_gpu_pstate` | GPU 性能状态（0=P0） | — | gpu |
| `mthreads_gpu_vpu_utilization_percent` | GPU VPU 利用率（0-100） | % | gpu |
| `mthreads_gpu_vpu_encoder_utilization_percent` | GPU VPU 编码器利用率（0-100） | % | gpu |
| `mthreads_gpu_vpu_decoder_utilization_percent` | GPU VPU 解码器利用率（0-100） | % | gpu |
| `mthreads_gpu_vpu_clock_mhz` | GPU VPU 时钟频率 | MHz | gpu |
| `mthreads_gpu_pcie_link_speed_gt_per_sec` | GPU PCIe 当前链路速率 | GT/s | gpu |
| `mthreads_gpu_pcie_link_width_lanes` | GPU PCIe 当前链路宽度 | 通道数 | gpu |
| `mthreads_gpu_pcie_replay_total` | GPU PCIe 重放计数器 | 计数 | gpu |
| `mthreads_gpu_mtlink_state` | GPU MtLink 状态（0=DOWN,1=UP,2=DOWNGRADE） | — | gpu, link |
| `mthreads_gpu_mtlink_link_bandwidth_gb_s` | GPU MtLink 每链路最大带宽（设备静态规格，非实时吞吐） | GB/s | gpu |
| `mthreads_gpu_mtlink_link_count` | GPU MtLink 最大支持链路数（设备静态规格） | 链路数 | gpu |
