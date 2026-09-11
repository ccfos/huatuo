---
title: 内核全景观测
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 1
---

当前版本支持的指标:

## CPU 系统

### 调度延迟

如下指标可以观测进程调度延迟状态，即一个进程从变得可运行的时刻（即被放进运行队列），到它真正开始在 CPU 上执行的这段时间。

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

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|runqlat_container_latency|进程调度延迟计数：<br>zone0, 0~10ms<br>zone1, 10-20ms <br>zone2, 20-50ms <br>zone3, 50+ms|计数|容器| eBPF |container_host, container_hostnamespace, container_level, container_name, container_type, host, region, zone |
|runqlat_latency|进程调度延迟计数：<br>zone0, 0~10ms<br>zone1, 10-20ms <br>zone2, 20-50ms <br>zone3, 50+ms |计数|物理机| eBPF | host, region, zone|

### 中断延迟

系统中各类软中断在不同CPU上的响应延迟指标（当前只采集了 NET_RX/NET_TX）。

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

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|softirq_latency|软中断响应延迟在不同 zone 的计数：<br>zone0, 0-10us<br>zone1, 10-100us<br>zone2, 100-1000us<br>zone3, 1+ms |计数|物理机| eBPF |cpuid, host, region, type, zone|


### 资源利用率

通过如下指标可以观测，物理机，容器的 CPU 资源使用情况，prometheus 指标格式：
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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|cpu_util_sys| CPU 内核态利用率|%| 物理机 | host, region |
|cpu_util_usr| CPU 用户态利用率|%| 物理机 | host, region |
|cpu_util_total| CPU 总利用率  |%| 物理机 | host, region |
|cpu_util_container_sys| CPU 内核态利用率|%|容器|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |
|cpu_util_container_usr| CPU 用户态利用率|%|容器|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |
|cpu_util_container_total| CPU 总利用率|%|容器|container_host,container_hostnamespace,container_level,container_name,container_type,host,region |

### 资源配置

通过如下指标可以了解容器 CPU 资源配置情况，prometheus 指标格式：
```bash
# HELP huatuo_bamai_cpu_util_container_cores cpu core number for the containers
# TYPE huatuo_bamai_cpu_util_container_cores gauge
huatuo_bamai_cpu_util_container_cores{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="Burstable",container_name="coredns",container_type="Normal",host="hostname",region="dev"} 6
```

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|cpu_util_container_cores| quota、有效 cpuset 和主机核数共同约束的 CPU 容量，可为小数|个| 容器 | container_host, container_hostnamespace, container_level, container_name, container_type, host, region |

容器核数表示可用容量，不表示独占物理核心数；两端保留原有利用率口径和指标名。

`cpu_util` 和 `memory_vmstat` 在采集期间分别保留主机与容器的可用数据：容器发现失败不阻断主机指标，主机读取失败不丢弃已采集的容器指标。失败会返回采集管理器，标记该采集器本次采集失败，同时输出可用的部分指标；不会将失败项伪造成零。此行为不改变采集器初始化依赖，也不改变单个容器读取失败时记录日志并跳过的既有策略。

### 资源争抢

这些指标体现了容器争抢，被限制等状态，prometheus 指标格式：
```bash
# HELP huatuo_bamai_cpu_stat_container_nr_throttled throttle nr for the containers
# TYPE huatuo_bamai_cpu_stat_container_nr_throttled gauge
huatuo_bamai_cpu_stat_container_nr_throttled{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_throttled_time throttle time for the containers
# TYPE huatuo_bamai_cpu_stat_container_throttled_time gauge
huatuo_bamai_cpu_stat_container_throttled_time{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|cpu_stat_container_nr_throttled| 当前 cgroup 被 throttled 限制的次数|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|cpu_stat_container_throttled_time| 当前 cgroup 被 throttled 限制的总时间|纳秒|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

Ref:
- https://docs.kernel.org/scheduler/sched-bwc.html#statistics
- https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html#cpu-interface-files

内核导出 `wait_sum` 时提供以下争抢指标：
```bash
# HELP huatuo_bamai_cpu_stat_container_wait_sum_percent percentage of CFS cgroup schedulable time spent waiting on the parent runqueue (requires kernel.sched_schedstats=1)
# TYPE huatuo_bamai_cpu_stat_container_wait_sum_percent gauge
huatuo_bamai_cpu_stat_container_wait_sum_percent{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

### 资源突发

如下指标体现了容器出现资源突发使用状态：

```bash
# HELP huatuo_bamai_cpu_stat_container_nr_bursts burst nr for the containers
# TYPE huatuo_bamai_cpu_stat_container_nr_bursts gauge
huatuo_bamai_cpu_stat_container_nr_bursts{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
# HELP huatuo_bamai_cpu_stat_container_burst_time burst time for the containers
# TYPE huatuo_bamai_cpu_stat_container_burst_time gauge
huatuo_bamai_cpu_stat_container_burst_time{container_host="coredns-855c4dd65d-8v5kg",container_hostnamespace="kube-system",container_level="burstable",container_name="coredns",container_type="normal",host="hostname",region="dev"} 0
```

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|cpu_stat_container_burst_time| 所有在各个周期中超过 quota 部分所累计使用的真实墙钟时间|纳秒|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|cpu_stat_container_nr_bursts| 发生超额使用的周期数量|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |

### 资源负载

这些指标体现物理机、容器负载状态。
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

|指标|意义|单位|对象|标签|备注|
|---|---|---|---|---|---|
|loadavg_load1|系统过去 1 分钟的平均负载|计数|物理机| host, region ||
|loadavg_load5|系统过去 5 分钟的平均负载|计数|物理机| host, region ||
|loadavg_load15|系统过去 15 分钟的平均负载|计数|物理机| host, region ||
|loadavg_nr_running|主机当前正在运行或等待 CPU 的任务数|计数|物理机| host, region |读取 `/proc/stat` 的 `procs_running`|
|loadavg_nr_uninterruptible|主机参与负载计算的不可中断任务数|计数|物理机| host, region |共享 BPF task iterator|
|loadavg_container_nr_running|容器中运行或等待 CPU 的任务数量|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |支持 cgroup v1/v2|
|loadavg_container_nr_uninterruptible|容器中不可中断任务的数量|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |支持 cgroup v1/v2|
|loadavg_container_load1|容器 1 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|
|loadavg_container_load5|容器 5 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|
|loadavg_container_load15|容器 15 分钟 R+D 平均负载估算|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |cgroup v1/v2，可配置 EMA 采样间隔，默认 15 秒|

`nr_running` 是瞬时 Gauge，不是 `load1/5/15` 平均负载，也不是 CPU 利用率。主机包含容器任务，不能与容器值相加；容器采用非递归采样，不包含子 cgroup。主机数据依赖采集器可见的宿主机 procfs，不要求启用 cgroup；读取失败或字段缺失时不补零，且不影响已有 load average 和容器采集。

容器 R/D 与平均负载由 `loadavg` 生命周期按 `MetricCollector.Loadavg.Interval` 秒采样（默认 15 秒，0 使用默认值），独立于 Prometheus 抓取和 dload 自动追踪。v1 复用 netlink，v2 复用共享 BPF task iterator。平均负载按实际采样间隔计算 `L += (R+D-L) * (1-exp(-dt/τ))`，τ 为 60/300/900 秒；首次采样只建立基线，之后从零预热。它是用户态估算，不等同于主机内核 loadavg，也不按 CPU 配额归一化。容器消失、采样缺失或间隔超过三倍采样周期（默认 45 秒）时清理/重置该容器历史，不以零填充失败；超过三倍采样周期的缓存不导出。暂停或重启 loadavg 会重置平均负载，已有主机 loadavg 不变。Dload 仍使用独立的 `AutoTracing.Dload.Interval`（默认 10 秒）；iterator 仅合并 100ms 内符合条件的请求，不保证每次采样都复用。

主机 `loadavg_nr_uninterruptible` 随 `loadavg` 组件启用。它复用 dload 的共享 BPF task iterator，按 loadavg 配置的周期（默认 15 秒）统计全主机参与负载计算的 D 状态任务（排除 `TASK_NOLOAD`/`TASK_FROZEN`），包括非容器任务，不是简单相加容器指标，也不使用 `/proc/stat` 的 `procs_blocked`。在 cgroup v2 上在同一次遍历中汇总；主机统计不受 cgroup v1/v2 模式限制，但需要内核 BTF、BPF task iterator 和主机 PID namespace。不支持时省略该指标，不扫描 `/proc` 任务目录兜底；已有主机 loadavg 和 v1 容器指标不受影响。它是遍历期间的采样值，不是所有任务的原子快照。

cgroup v2 容器负载指标随 `loadavg` 组件启用，每次采样与主机 D 状态统计共用一次宿主机全部任务遍历。它依赖可读的内核 BTF 和 BPF `task` iterator，统计仅包含直接挂在目标 cgroup 下的任务，不递归包含子 cgroup。内核不支持时仍会输出基于 procfs 的物理机负载指标，省略依赖 iterator 的指标且只记录一次告警，不会将宿主机 procfs 采集判为失败；确定的不支持结果会被缓存，后续不会反复尝试加载 BPF 程序。

Kubernetes 部署必须为 Huatuo 设置 `hostPID: true`。无法访问宿主机 PID namespace 时，cgroup v2 容器指标会作为不支持而省略，不会输出有误导性的全零数据。

## 内存系统

### 资源回收

系统内存回收行为可能导致进程被阻塞。通过这些指标可以了解系统内存状态。
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

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|memory_free_allocpages_stall|全局直接回收累计耗时| 毫秒|物理机| eBPF | host, region|
|memory_free_compaction_stall|直接内存规整累计耗时| 毫秒|物理机| eBPF | host, region|
|memory_free_container_allocpages_stall|容器任务参与全局直接回收的累计耗时| 毫秒|容器| eBPF | container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_free_container_compaction_stall|容器任务参与直接内存规整的累计耗时| 毫秒|容器| eBPF | 同上|
|memory_reclaim_directstall|整台主机 memcg 限额回收累计次数| 计数| 物理机| eBPF | host, region|
|memory_reclaim_container_directstall|容器任务触发 memcg 限额回收累计次数| 计数| 容器| eBPF | container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

主机两项原本就导出毫秒，本次仅修正文档，不改名称、数值缩放或 Gauge 类型。容器两项在操作入口记录 memory CSS，复用现有 cgroup v1/v2 容器发现能力；表示任务承受的全局直接回收/规整耗时，不是为该容器回收了多少内存，也不是 memcg 限额回收次数 `memory_reclaim_container_directstall`。主机总量包含未归属容器的任务，不能和容器指标相加。

仅为存在 BPF 记录且能匹配已发现普通容器的 cgroup 导出，不为缺失容器补零；容器发现或容器 map 读取失败时仍导出主机值并报告错误。计时表与容器累计表各限制 10240 条 LRU 记录，压力下可能丢失计时或淘汰累计值，BPF 重载也会归零，分析增量需处理重置。除既有回收 tracepoint、规整 kprobe 外，容器采集使用 `cgroup_mkdir` raw tracepoint 清理复用的 CSS 地址。所有数值为采集期间累计值，并非机器启动以来的绝对累计。

启动时若完整 BPF 对象加载或挂载失败，释放原对象后仅重试一次主机模式：关闭容器 cgroup 读取与计数，移除 `cgroup_mkdir` 程序，保留原有两项主机耗时指标并记录降级告警；不导出容器零值，不调整默认过滤配置。主机模式仍要求可用的 BPF/BTF、回收 tracepoint 和规整 kprobe，不能绕过这些基础依赖；主机模式也失败则报告启动错误。

> **注意**：`memory_others_container_directstall_time`、`memory_others_container_asyncreclaim_time`、`memory_others_container_local_direct_reclaim_time` 指标读取的是厂商定制内核提供的 memory cgroup 扩展接口（`memory.directstall_stat`、`memory.asynreclaim_stat`、`memory.local_direct_reclaim_time`）。主线内核及常见发行版内核不提供这些接口，因此这些指标不会输出，属预期行为，无需额外加载内核模块。在标准内核上观测容器直接回收（direct reclaim）行为，请使用上表基于 eBPF 实现的 `memory_reclaim_container_directstall`。

`memory_reclaim_directstall` 与容器计数共用 `mm_vmscan_memcg_reclaim_begin`，均排除 kswapd；在容器 CSS 匹配前累加 per-CPU 主机计数，采集时求和。它覆盖整台主机上的 memcg 回收（包括未被发现为普通容器的 cgroup），不是容器指标求和，也不是全局直接回收次数或耗时；没有 memcg 回收时为零。主机计数不受容器删除或容器计数表容量影响，BPF 重载归零。沿用 Gauge 类型及现有挂点要求，不新增探针、不调整默认过滤配置；主机和容器采集失败相互隔离，失败的一侧不伪造零值。

### 资源状态

通过如下指标可以了解整体系统、容器的内存状态。

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|memory_vmstat_container_active_file|活跃的文件内存数|字节, Bytes | 容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_active_anon|活跃的匿名内存数|字节, Bytes | 容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_inactive_file|非活跃的文件内存数|字节, Bytes | 容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_inactive_anon|非活跃的匿名内存数|字节, Bytes | 容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_file_dirty|已修改且还未写入磁盘的文件内存大小|字节, Bytes |容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_file_writeback|已修改且正等待写入磁盘的文件内存大小|字节, Bytes |容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_dirty|已修改且还未写入磁盘的内存大小|字节, Bytes |容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_writeback|已修改且正等待写入磁盘的文件，匿名内存大小|字节, Bytes |容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_vmstat_container_pgdeactivate|将页面从 active LRU 移动到 inactive LRU 的数量|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_pgrefill|在 active LRU 链表上被扫描的页面总数|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_pgscan_direct|直接回收时，在 inactive LRU 上扫描过的页面总数|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_pgscan_kswapd|kswapd 在 inactive LRU 链表上扫描过的页面总数|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_pgsteal_direct|直接回收时，成功从 inactive LRU 回收的页面总数|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_pgsteal_kswapd|kswapd 成功从 inactive LRU 回收的页面总数|页数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |
|memory_vmstat_container_unevictable|不可回收的页面字节数|字节, Bytes|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region |


物理机内存资源指标：
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

- 页面状态与 LRU 分布, Page state & LRU

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|nr_free_pages|空闲页面总数（伙伴系统可直接分配）。|页面| 物理机|  host, region|
|nr_inactive_anon|非活跃匿名页面数|页面| 物理机| host, region|
|nr_inactive_file|活跃文件页面数|页面| 物理机| host, region|
|nr_active_anon|活跃匿名页面数|页面| 物理机| host, region|
|nr_active_file|活跃文件页面数|页面| 物理机| host, region|
|nr_unevictable|不可回收页面数（mlocked、hugetlbfs 等）|页面| 物理机| host, region|
|nr_mlock|被 mlock() 锁定的页面数|页面| 物理机| host, region|
|nr_shmem|tmpfs / shmem 使用的页面数|页面| 物理机| host, region|
|nr_slab_reclaimable|可回收的 slab 缓存对象|页面| 物理机| host, region|
|nr_slab_unreclaimable|不可回收的 slab 缓存对象|页面| 物理机| host, region|

- 脏页与写回控制, Dirty & writeback thresholds

|指标|意义|单位|对象|标签 |
|---|---|---|---|---|
|nr_dirty|当前脏页数|页面| 物理机| host, region|
|nr_writeback|正在写回的页面数|页面| 物理机| host, region|
|nr_dirty_threshold|脏页达到此阈值时开始强制写回（dirty_background_ratio / dirty_ratio 决定）|页面| 物理机| host, region|
|nr_dirty_background_threshold|后台写回开始的阈值|页面| 物理机| host, region|
|nr_dirty_background_threshold|后台写回开始的阈值|页面| 物理机| host, region|

- 页面错误与换页, Page fault & swapping

|指标|意义|单位|对象|标签 |
|---|---|---|---|---|
|pgfault|总缺页异常次数|计数| 物理机| host, region|
|pgmajfault|主缺页异常次数|计数| 物理机| host, region|
|pgpgin|从块设备读入的页面数|页面| 物理机| host, region|
|pgpgout|写出到块设备的页面数|页面| 物理机 | host, region|
|pswpin/pswpout|换入/换出的页面数（swap）|页面| 物理机| host, region|

- 回收与扫描, Reclaim & scanning

|指标|意义|单位|对象|标签 |
|---|---|---|---|---|
|pgscan_kswapd/direct/khugepaged|kswapd/直接回收/khugepaged 扫描的页面数|页面数| 物理机| host, region|
|pgsteal_kswapd/direct/khugepaged|回收成功的页面数|页面数| 物理机| host, region|

- 透明大页, THP

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|thp_fault_alloc|缺页时成功分配 THP 的次数|计数| 物理机| host, region|
|thp_fault_fallback|缺页时分配 THP 失败而回落普通页的次数|计数| 物理机| host, region|
|thp_collapse_alloc|khugepaged 折叠成 THP 的成功次数|计数| 物理机| host, region|
|thp_collapse_alloc_failed|khugepaged 折叠 THP 的失败次数|计数| 物理机| host, region|

- NUMA 相关统计, NUMA balancing & allocation

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|numa_hit|进程希望从某个节点分配内存，并且成功在该节点上分配到的页面总数。|计数| 物理机|  host, region|
|numa_miss|进程原本希望从其他节点分配，但由于目标节点内存不足等原因，最终在本节点分配成功的页面数。|计数| 物理机| host, region|
|numa_foreign|进程原本希望从本节点分配内存，但最终在其他节点分配成功的页面数。|计数| 物理机| host, region|
|numa_local|进程在本地节点上成功分配到的页面总数。|计数| 物理机| host, region|
|numa_other|进程在远程节点上分配到的页面总数。|计数| 物理机| host, region|
|numa_pages_migrated|由于自动 NUMA 平衡而成功迁移的页面总数|计数| 物理机| host, region|

Ref:
- https://docs.kernel.org/admin-guide/cgroup-v2.html
- https://docs.kernel.org/admin-guide/cgroup-v1/memory.html
- https://docs.kernel.org/admin-guide/mm/transhuge.html

### 资源事件

容器级别的内存事件指标。

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|memory_events_container_low|使用量低于 memory.low，但由于系统内存压力大，仍被主动回收的次数。说明 memory.low 被过度承诺。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_high|内存使用量超过 memory.high（软限制），导致进程被节流并强制走直接回收的次数。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_max|内存使用量达到或即将超过 memory.max（硬限制），触发内存分配失败检查的次数。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom|内存使用量达到 memory.max 限制，导致内存分配失败，进入 OOM 路径的次数。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom_kill|cgroup 内因达到内存限制而被 OOM killer 杀死的进程数。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|memory_events_container_oom_group_kill|整个 cgroup 被 OOM killer 杀死的次数。|计数|容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### Buddyinfo

展示 Buddy 分配器（内核页分配器核心算法）在每个 NUMA 节点（Node）和每个内存区域（Zone）中的空闲内存块分布情况。

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

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|---|
|memory_buddyinfo_blocks| buddy 内存页空闲情况。|内存页|物理机| procfs | host, node, order, region, zone |


## 网络系统

#### TCP 内存

如下指标描述 TCP 协议栈占用系统内存状态。

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

|指标|意义|单位|对象|标签 |
|---|---|---|---|---|
|tcp_memory_limit_pages| 系统可使用的 TCP 总内存大小|内存页|物理机| host, region |
|tcp_memory_usage_bytes| 系统已使用的 TCP 内存大小|字节|物理机| host, region |
|tcp_memory_usage_pages| 系统已使用的 TCP 内存大小|内存页|物理机| host, region |
|tcp_memory_usage_percent|系统已使用的 TCP 内存百分比（相对 TCP 内存总限制）|%|物理机| host, region |

### 邻居项

如下指标描述邻居项使用状态。

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|arp_entries| 宿主机网络命名空间 arp 条目数量|计数|宿主命名空间|host, region|
|arp_total| 物理机所有网络命名空间 arp 条目数量总和|计数|物理机|host, region|
|arp_container_entries| 容器网络命名空间 arp 条目数量|计数|容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### Qdisc

Qdisc 是内核网络子系统重要模块。通过观测该模块，可以清楚的看到网络报文处理，延迟情况。

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|qdisc_backlog|后备排队待发送的字节数|字节|物理机| device, host, kind, region |
|qdisc_current_queue_length|当前排队的包量|计数|物理机| device, host, kind, region |
|qdisc_overlimits_total|超限次数|计数|物理机| device, host, kind, region |
|qdisc_requeues_total|由于网卡/驱动暂时无法发送而被重新入队的次数|计数|物理机| device, host, kind, region |
|qdisc_drops_total|主动丢弃的包数（因队列满、限速策略等原因）|计数|物理机| device, host, kind, region |
|qdisc_bytes_total|已发送的包量|字节|物理机| device, host, kind, region |
|qdisc_packets_total|已发送的包数|计数|物理机| device, host, kind, region |

### 硬件丢包

网络设备硬件接收方向丢包数。

```bash
# HELP huatuo_bamai_netdev_hw_rx_dropped count of packets dropped at hardware level
# TYPE huatuo_bamai_netdev_hw_rx_dropped gauge
huatuo_bamai_netdev_hw_rx_dropped{device="eth0",driver="mlx5_core",host="hostname",region="dev"} 0
```

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|netdev_hw_rx_dropped|网卡硬件接收方向丢包|计数|物理机|eBPF| device, driver, host, region |


### 网络设备

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|netdev_receive_bytes_total|成功接收的总字节数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_packets_total|成功接收的数据包总数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_compressed_total|接收到的已压缩数据包数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_frame_total|接收帧错误数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_errors_total|接收错误总数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_dropped_total|由于各种原因被内核或驱动丢弃的接收包数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_receive_fifo_total|接收FIFO/环形缓冲区溢出错误数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_bytes_total|成功发送的总字节数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_packets_total|成功发送的数据包总数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_errors_total|发送错误总数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_dropped_total|发送过程中被丢弃的包数（队列满、策略丢弃等）|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_fifo_total|发送FIFO/环形缓冲区错误数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_carrier_total|载波错误次数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netdev_transmit_compressed_total|发送的已压缩数据包数|计数|物理机或者容器| container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

### TCP

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|netstat_TcpExt_ArpFilter|因 ARP 过滤规则而被丢弃的数据包数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_BusyPollRxPackets|通过 busy polling 机制接收到的数据包数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_DelayedACKLocked|由于用户态进程锁住了 socket，而无法发送 delayed ACK 的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_DelayedACKLost|延迟 ACK 丢失导致重传的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_DelayedACKs|尝试发送 delayed ACK 的次数，包括未成功发送的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_EmbryonicRsts|在 SYN_RECV 状态收到带 RST/SYN 标记的包个数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_ListenDrops|因全连接队列满丢弃的连接总数（含ListenOverflows）|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_ListenOverflows|表示在 TCP 监听队列中发生的溢出次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_OfoPruned|乱序队列因内存不足被修剪的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_OutOfWindowIcmps|收到的与当前 TCP 窗口无关的 ICMP 错误报文数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_PruneCalled|因内存不足触发缓存清理的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_RcvPruned|接收队列因内存不足被修剪（丢弃数据包）的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_SyncookiesFailed|验证失败的 SYN cookie 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_SyncookiesRecv|表示接收的 SYN cookie 的数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_SyncookiesSent|表示发送的 SYN cookie 的数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPACKSkippedChallenge|在处理 Challenge ACK 过程中跳过的其他 ACK 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPACKSkippedFinWait2|在 FIN-WAIT-2 状态下跳过的 ACK 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPACKSkippedPAWS|因 PAWS 检查失败而跳过的 ACK 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPACKSkippedSeq|因为序列号检查而跳过的 ACK 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPACKSkippedTimeWait|在 TIME-WAIT 状态下跳过的 ACK 数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPAbortOnClose|用户态程序在缓冲区内还有数据时关闭连接的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPAbortOnData|收到未知数据导致被关闭的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPAbortOnLinger|在LINGER状态下等待超时后中止连接的数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPAbortOnMemory|因内存问题关闭连接的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPAbortOnTimeout|因各种计时器的重传次数超过上限而关闭连接的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPLossFailures|丢失数据包而进行恢复失败的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPLossProbeRecovery|检测到丢失的数据包恢复的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPLossProbes|TCP 检测到丢失的数据包数量，通常用于检测网络拥塞或丢包|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPLossUndo|在恢复过程中检测到丢失而撤销的次数|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|
|netstat_TcpExt_TCPLostRetransmit|丢包重传的数量|计数|宿主，容器|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

备注：TcpExt 扩展指标非常多，可按需参考官方文档。

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

|指标|意义|单位|对象| 标签 |
|---|---|---|---|---|
|sockstat_sockets_used|系统层面当前正在使用的 socket 描述符总数|计数|系统||
|sockstat_TCP_inuse|当前处于 TCP 连接状态（如 ESTABLISHED、LISTEN 等，除 TIME_WAIT 外）的 socket 数量|计数|宿主，容器||
|sockstat_TCP_orphan|通常表示应用已关闭但 TCP 连接仍未结束|计数|宿主，容器||
|sockstat_TCP_tw|当前处于 TIME_WAIT 状态的 TCP socket 数量|计数|宿主，容器||
|sockstat_TCP_alloc|当前已分配的 TCP socket 对象总数|计数|宿主，容器||
|sockstat_TCP_mem|TCP 套接字当前占用的内核内存页数|内存页|系统||
|sockstat_UDP_inuse|当前已绑定了本地端口的 UDP socket 数量|计数|宿主，容器||

## IO

`iolatency` 用来统计磁盘 I/O 延迟分布。可以把它理解成“把一次磁盘请求拆成几个阶段，再分别看每个阶段耗时多久”。

- `q2c`：从请求进入队列到完成，反映整个 I/O 生命周期延迟
- `d2c`：从驱动层下发到完成，更接近磁盘和驱动本身的耗时
- `freeze`：磁盘冻结事件次数

### 队列

这些指标都会自动带上公共标签 `host` 和 `region`。其中容器维度指标还会固定带上
`container_host`、`container_name`、`container_type`、`container_level`、`container_hostnamespace` 标签。

```bash
# HELP huatuo_bamai_iolatency_blkdisk_d2c the disk d2c latency
# TYPE huatuo_bamai_iolatency_blkdisk_d2c gauge
huatuo_bamai_iolatency_blkdisk_d2c{disk="253:1",host="hostname",region="dev",zone="0"} 3
# HELP huatuo_bamai_iolatency_blkdisk_q2c the disk q2c latency
# TYPE huatuo_bamai_iolatency_blkdisk_q2c gauge
huatuo_bamai_iolatency_blkdisk_q2c{disk="253:1",host="hostname",region="dev",zone="0"} 3
# HELP huatuo_bamai_iolatency_container_blkdisk_d2c container blkio d2c latency
# TYPE huatuo_bamai_iolatency_container_blkdisk_d2c gauge
huatuo_bamai_iolatency_container_blkdisk_d2c{container_host="etcd-hostname",container_hostnamespace="kube-system",container_level="burstable",container_name="etcd",container_type="normal",disk="253:1",host="hostname",region="dev",zone="5"} 2
# HELP huatuo_bamai_iolatency_container_blkdisk_q2c container blkio q2c latency
# TYPE huatuo_bamai_iolatency_container_blkdisk_q2c gauge
huatuo_bamai_iolatency_container_blkdisk_q2c{container_host="etcd-hostname",container_hostnamespace="kube-system",container_level="burstable",container_name="etcd",container_type="normal",disk="253:1",host="hostname",region="dev",zone="5"} 2
```

|指标|意义|单位|对象|标签|
|---|---|---|---|---|
|iolatency_blkdisk_q2c|宿主机磁盘整体 I/O 生命周期延迟统计，从入队到完成。分桶为：zone0 20-30ms，zone1 30-50ms，zone2 50-100ms，zone3 100-200ms，zone4 200-400ms，zone5 400ms+|计数|宿主|host, region, disk, zone|
|iolatency_blkdisk_d2c|宿主机磁盘驱动到完成阶段的延迟统计，更接近设备处理耗时。分桶为：zone0 20-30ms，zone1 30-50ms，zone2 50-100ms，zone3 100-200ms，zone4 200-400ms，zone5 400ms+|计数|宿主|host, region, disk, zone|
|iolatency_container_blkdisk_q2c|容器触发的整体 I/O 生命周期延迟统计，从入队到完成。分桶为：zone0 20-30ms，zone1 30-50ms，zone2 50-100ms，zone3 100-200ms，zone4 200-400ms，zone5 400ms+|计数|容器|host, region, container_host, container_name, container_type, container_level, container_hostnamespace, zone|
|iolatency_container_blkdisk_d2c|容器触发的驱动到完成阶段延迟统计。分桶为：zone0 20-30ms，zone1 30-50ms，zone2 50-100ms，zone3 100-200ms，zone4 200-400ms，zone5 400ms+|计数|容器|host, region, container_host, container_name, container_type, container_level, container_hostnamespace, zone|

### 硬件

```bash
# HELP huatuo_bamai_iolatency_blkdisk_freeze the disk freeze event count
# TYPE huatuo_bamai_iolatency_blkdisk_freeze gauge
huatuo_bamai_iolatency_blkdisk_freeze{disk="253:1",host="hostname",region="dev"} 0
```

|指标|意义|单位|对象|标签|
|---|---|---|---|---|
|iolatency_blkdisk_freeze|宿主机磁盘 freeze 事件次数|计数|宿主|host, region, disk|

### 磁盘 IO 统计

`diskio` 通过读取 `/proc/diskstats` 和 `/proc/stat` 采集 per-device 磁盘 IO 指标和系统级 CPU iowait。与 `iolatency` 不同，`diskio` 基于 procfs 而非 eBPF，提供可通过 rate 计算延迟的累积计数器。

默认配置通过 `BlackList` 禁用该 collector。需要启用这些指标时，从
`BlackList` 中移除 `diskio`。

Counter 指标为累积值，需使用 Prometheus `rate()` 计算 per-second 值（IOPS、吞吐量）。Gauge 指标为瞬时值。平均延迟通过 PromQL 中的 I/O 时间 rate 除以请求 rate 计算。

```bash
# HELP huatuo_bamai_diskio_read_requests_total Total number of read requests completed successfully.
# TYPE huatuo_bamai_diskio_read_requests_total counter
huatuo_bamai_diskio_read_requests_total{device="sda",host="hostname",region="dev"} 1000
# HELP huatuo_bamai_diskio_write_requests_total Total number of write requests completed successfully.
# TYPE huatuo_bamai_diskio_write_requests_total counter
huatuo_bamai_diskio_write_requests_total{device="sda",host="hostname",region="dev"} 2000
# HELP huatuo_bamai_diskio_read_bytes_total Total number of bytes read from the device.
# TYPE huatuo_bamai_diskio_read_bytes_total counter
huatuo_bamai_diskio_read_bytes_total{device="sda",host="hostname",region="dev"} 2.56e+07
# HELP huatuo_bamai_diskio_written_bytes_total Total number of bytes written to the device.
# TYPE huatuo_bamai_diskio_written_bytes_total counter
huatuo_bamai_diskio_written_bytes_total{device="sda",host="hostname",region="dev"} 4.096e+07
# HELP huatuo_bamai_diskio_io_in_progress Number of I/O requests currently in flight (queue depth).
# TYPE huatuo_bamai_diskio_io_in_progress gauge
huatuo_bamai_diskio_io_in_progress{device="sda",host="hostname",region="dev"} 50
# HELP huatuo_bamai_diskio_read_time_seconds_total Total seconds spent by completed read requests.
# TYPE huatuo_bamai_diskio_read_time_seconds_total counter
huatuo_bamai_diskio_read_time_seconds_total{device="sda",host="hostname",region="dev"} 3
# HELP huatuo_bamai_diskio_write_time_seconds_total Total seconds spent by completed write requests.
# TYPE huatuo_bamai_diskio_write_time_seconds_total counter
huatuo_bamai_diskio_write_time_seconds_total{device="sda",host="hostname",region="dev"} 6
# HELP huatuo_bamai_diskio_disk_iowait_percent CPU time spent waiting for I/O during the collection interval.
# TYPE huatuo_bamai_diskio_disk_iowait_percent gauge
huatuo_bamai_diskio_disk_iowait_percent{host="hostname",region="dev"} 50
```

|指标|意义|单位|对象|标签|
|---|---|---|---|---|
|read_requests_total|累积读请求完成数（field 4），使用 `rate()` 计算读 IOPS|计数|宿主|host, region, device|
|write_requests_total|累积写请求完成数（field 8），使用 `rate()` 计算写 IOPS|计数|宿主|host, region, device|
|read_bytes_total|累积读取字节数（field 6 × 512），使用 `rate()` 计算读吞吐量|字节|宿主|host, region, device|
|written_bytes_total|累积写入字节数（field 10 × 512），使用 `rate()` 计算写吞吐量|字节|宿主|host, region, device|
|read_time_seconds_total|已完成读请求的累积耗时（field 7）|秒|宿主|host, region, device|
|write_time_seconds_total|已完成写请求的累积耗时（field 11）|秒|宿主|host, region, device|
|io_in_progress|当前正在进行的 I/O 请求数，即队列深度（field 12）|计数|宿主|host, region, device|
|disk_iowait_percent|采集区间内 CPU 等待 I/O 完成的时间占比|百分比|宿主|host, region|


## 通用系统

### Soft Lockup

```bash
# HELP huatuo_bamai_softlockup_total softlockup counter
# TYPE huatuo_bamai_softlockup_total counter
huatuo_bamai_softlockup_total{host="hostname",region="dev"} 0
```

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|softlockup_total|系统 softlockup 事件计数|计数|物理机|BPF|

### HungTask
```bash
# HELP huatuo_bamai_hungtask_total hungtask counter
# TYPE huatuo_bamai_hungtask_total counter
huatuo_bamai_hungtask_total{host="hostname",region="dev"} 0
```

|指标|意义|单位|对象|取值| 标签 |
|---|---|---|---|---|---|
|hungtask_total|系统 hungtask 事件计数（包含容器与未归属任务）|计数|物理机|BPF|host, region|
|hungtask_container_total|已归属容器的阻塞任务事件计数|计数|容器|BPF + cgroup 文件句柄|container_host, container_hostnamespace, container_level, container_name, container_type, host, region|

容器归属通过 `sched_process_hang` raw tracepoint，在事件时刻读取目标阻塞任务（不是 `khungtaskd`）的 cgroup 身份。cgroup v1/Hybrid 使用 CPU 层级，v2 使用统一层级；携带最近的最多 16 层非根 cgroup 完整 kernfs ID（含代次），与已发现普通容器的 cgroup 文件句柄匹配，优先归属最近容器，支持容器子 cgroup。复用现有 cgroup ID 读取路径，不再事后查询 `/proc/<tid>/cgroup`，避免任务退出、TID 复用和后续迁移改变归属。

容器元数据消失、文件句柄不可读、身份读取失败，或容器根超出 16 层范围且无法匹配时，只保留主机总计，缺失容器不补零。raw tracepoint 加载或挂载失败时，清理后重试原有普通 tracepoint，仅采集主机事件并告警；不回退到可能误归属的 TID 查询。主机路径本身仍需要原有 BPF/BTF 和 hungtask tracepoint 支持。事件身份更精确不代表容器元数据发现一定成功，诊断堆栈仍是主机范围。

两项均保留 Counter 语义，计数发生在追踪退避之前；追踪仍沿用主机级退避和全机栈快照，已匹配的记录附带 `ContainerID`，不表示栈已按容器过滤。容器指标使用生命周期内累计计数，成功发现容器退出后清理，进程重启归零。发现故障不丢主机指标，也不清空已累计的容器状态。主机与容器计数不能相加，重复上报同一阻塞线程仍按多次事件计数。


## GPU

当前版本支持的 GPU 平台:
- MetaX

|指标|描述|单位|统计纬度|指标来源|
|----|---|---|---|---|
|metax_gpu_sdk_info|GPU SDK 信息|-|version|sml.GetSDKVersion|
|metax_gpu_driver_info|GPU 驱动信息|-|version|sml.GetGPUVersion with driver unit|
|metax_gpu_info|GPU 基本信息|-|gpu|
|metax_gpu_board_power_watts|GPU 板级功耗|瓦特（W）|gpu|sml.ListGPUBoardWayElectricInfos|
|metax_gpu_pcie_link_speed_gt_per_second|GPU PCIe 当前链路速率|GT/s|gpu|sml.GetGPUPcieLinkInfo|
|metax_gpu_pcie_link_width_lanes|GPU PCIe 当前链路宽度|链路宽度（通道数）|gpu|sml.GetGPUPcieLinkInfo|
|metax_gpu_pcie_receive_bytes_per_second|GPU PCIe 接收吞吐率|Bps|gpu|sml.GetGPUPcieThroughputInfo|
|metax_gpu_pcie_transmit_bytes_per_second|GPU PCIe 发送吞吐率|Bps|gpu|sml.GetGPUPcieThroughputInfo|
|metax_gpu_metaxlink_link_speed_gt_per_second|GPU MetaXLink 当前链路速率|GT/s|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|metax_gpu_metaxlink_link_width_lanes|GPU MetaXLink 当前链路宽度|链路宽度（通道数）|gpu, metaxlink|sml.ListGPUMetaXLinkLinkInfos|
|metax_gpu_metaxlink_receive_bytes_per_second|GPU MetaXLink 接收吞吐率|Bps|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|metax_gpu_metaxlink_transmit_bytes_per_second|GPU MetaXLink 发送吞吐率|Bps|gpu, metaxlink|sml.ListGPUMetaXLinkThroughputInfos|
|metax_gpu_metaxlink_receive_bytes_total|GPU MetaXLink 接收数据总量|字节|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|metax_gpu_metaxlink_transmit_bytes_total|GPU MetaXLink 发送数据总量|字节|gpu, metaxlink|sml.ListGPUMetaXLinkTrafficStatInfos|
|metax_gpu_metaxlink_aer_errors_total|GPU MetaXLink AER 错误次数|计数|gpu, metaxlink, error_type|sml.ListGPUMetaXLinkAerErrorsInfos|
|metax_gpu_status|GPU 状态|-|gpu, die|sml.GetDieStatus|
|metax_gpu_temperature_celsius|GPU 温度|摄氏度|gpu, die|sml.GetDieTemperature|
|metax_gpu_utilization_percent|GPU 利用率（0–100）|%|gpu, die, ip|sml.GetDieUtilization|
|metax_gpu_memory_total_bytes|显存总容量|字节|gpu, die|sml.GetDieMemoryInfo|
|metax_gpu_memory_used_bytes|已使用显存容量|字节|gpu, die|sml.GetDieMemoryInfo|
|metax_gpu_clock_mhz|GPU 时钟频率|兆赫兹（MHz）|gpu, die, ip|sml.ListDieClocks|
|metax_gpu_clocks_throttling|GPU 时钟降频原因|-|gpu, die, reason|sml.GetDieClocksThrottleStatus|
|metax_gpu_dpm_performance_level|GPU DPM 性能等级|-|gpu, die, ip|sml.GetDieDPMPerformanceLevel|
|metax_gpu_ecc_memory_errors_total|GPU ECC 内存错误次数|计数|gpu, die, memory_type, error_type|sml.GetDieECCMemoryInfo|
|metax_gpu_ecc_memory_retired_pages_total|GPU ECC 内存退役页数|计数|gpu, die|sml.GetDieECCMemoryInfo|

- 摩尔线程 (MThreads)

摩尔线程 GPU 监控通过 MTML（摩尔线程管理库）实现。库文件在启动时通过 SONAME 搜索（`libmtml.so.2` 然后 `libmtml.so`）自动发现。静态设备信息会被缓存，仅在重新初始化时重建；动态指标在每次抓取时采集。

配置：从 `BlackList` 中移除 `mthreads_gpu` 以启用。指标组由 `[MetricCollector.Mthreads]` 开关控制（`EnableHealth`、`EnablePCIe`、`EnableMTLink`）。

|指标|描述|单位|标签|来源|
|----|---|---|---|---|
|huatuo_bamai_mthreads_gpu_library_info|MTML 库信息|-|version|mtml.LibraryVersion|
|huatuo_bamai_mthreads_gpu_device_info|GPU 信息|-|gpu, name, brand, serial|mtml.Device.Name/Brand/SerialNumber|
|huatuo_bamai_mthreads_gpu_device_pci_info|GPU PCI 信息|-|gpu, sbdf|mtml.Device.PciSbdf|
|huatuo_bamai_mthreads_gpu_device_spec|GPU 规格|-|gpu, cores|mtml.Device.GpuCores|
|huatuo_bamai_mthreads_gpu_device_bios_version|GPU BIOS 版本|-|gpu, bios|mtml.Device.BiosVersion|
|huatuo_bamai_mthreads_gpu_device_musa_capability|GPU MUSA 计算能力|-|gpu, musa_capability|mtml.Device.MusaComputeCapability|
|huatuo_bamai_mthreads_gpu_memory_info|GPU 内存信息|-|gpu, type, vendor, bus_width|mtml.Device.InitMemory|
|huatuo_bamai_mthreads_gpu_pcie_link_max_speed_gt_per_sec|GPU PCIe 最大链路速率（硬件能力）|GT/s|gpu|mtml.Device.PciInfo|
|huatuo_bamai_mthreads_gpu_pcie_link_max_width_lanes|GPU PCIe 最大链路宽度（硬件能力）|lanes|gpu|mtml.Device.PciInfo|
|huatuo_bamai_mthreads_gpu_device_power_watts|GPU 设备功耗|W|gpu|mtml.Device.PowerUsage|
|huatuo_bamai_mthreads_gpu_gpu_temperature_celsius|GPU 温度|°C|gpu|mtml.Gpu.Temperature|
|huatuo_bamai_mthreads_gpu_memory_temperature_celsius|GPU 内存温度|°C|gpu|mtml.Memory.Temperature|
|huatuo_bamai_mthreads_gpu_gpu_utilization_percent|GPU 利用率（0-100）|%|gpu|mtml.Gpu.Utilization|
|huatuo_bamai_mthreads_gpu_memory_utilization_percent|GPU 内存利用率（0-100）|%|gpu|mtml.Memory.Utilization|
|huatuo_bamai_mthreads_gpu_memory_total_bytes|GPU 总内存|bytes|gpu|mtml.Memory.Total|
|huatuo_bamai_mthreads_gpu_memory_used_bytes|GPU 已使用内存|bytes|gpu|mtml.Memory.Used|
|huatuo_bamai_mthreads_gpu_gpu_clock_mhz|GPU 时钟频率|MHz|gpu|mtml.Gpu.Clock|
|huatuo_bamai_mthreads_gpu_gpu_max_clock_mhz|GPU 最大时钟频率|MHz|gpu|mtml.Gpu.MaxClock|
|huatuo_bamai_mthreads_gpu_memory_clock_mhz|GPU 内存时钟频率|MHz|gpu|mtml.Memory.Clock|
|huatuo_bamai_mthreads_gpu_memory_max_clock_mhz|GPU 内存最大时钟频率|MHz|gpu|mtml.Memory.MaxClock|
|huatuo_bamai_mthreads_gpu_gpu_voltage_volts|GPU 电压|V|gpu|mtml.Gpu.Voltage|
|huatuo_bamai_mthreads_gpu_gpu_power_limit_watts|GPU 强制功耗限制|W|gpu|mtml.Gpu.EnforcedPowerLimit|
|huatuo_bamai_mthreads_gpu_gpu_power_default_limit_watts|GPU 默认功耗管理限制|W|gpu|mtml.Gpu.PowerManagementDefaultLimit|
|huatuo_bamai_mthreads_gpu_fan_rpm|GPU 风扇转速|RPM|gpu, fan|mtml.Device.FanRpm|
|huatuo_bamai_mthreads_gpu_fan_speed_percent|GPU 风扇速度百分比|%|gpu, fan|mtml.Device.FanSpeed|
|huatuo_bamai_mthreads_gpu_gpu_pstate|GPU 性能状态（0=P0）|-|gpu|mtml.Device.PerformanceState|
|huatuo_bamai_mthreads_gpu_vpu_utilization_percent|GPU VPU 利用率（0-100）|%|gpu|mtml.Vpu.Utilization|
|huatuo_bamai_mthreads_gpu_vpu_encoder_utilization_percent|GPU VPU 编码器利用率（0-100）|%|gpu|mtml.Vpu.Utilization|
|huatuo_bamai_mthreads_gpu_vpu_decoder_utilization_percent|GPU VPU 解码器利用率（0-100）|%|gpu|mtml.Vpu.Utilization|
|huatuo_bamai_mthreads_gpu_vpu_clock_mhz|GPU VPU 时钟频率|MHz|gpu|mtml.Vpu.Clock|
|huatuo_bamai_mthreads_gpu_pcie_link_speed_gt_per_sec|GPU PCIe 当前链路速率|GT/s|gpu|mtml.Device.PciInfo|
|huatuo_bamai_mthreads_gpu_pcie_link_width_lanes|GPU PCIe 当前链路宽度|lanes|gpu|mtml.Device.PciInfo|
|huatuo_bamai_mthreads_gpu_pcie_replay_total|GPU PCIe 重放计数器|count|gpu|mtml.Device.PcieReplayCounter|
|huatuo_bamai_mthreads_gpu_mtlink_state|GPU MtLink 状态（0=DOWN,1=UP,2=DOWNGRADE）|-|gpu, link|mtml.Device.MtLinkState|
|huatuo_bamai_mthreads_gpu_mtlink_link_bandwidth_gb_s|GPU MtLink 每链路最大带宽（设备静态规格，非实时吞吐）|GB/s|gpu|mtml.Device.MtLinkSpec|
|huatuo_bamai_mthreads_gpu_mtlink_link_count|GPU MtLink 最大支持链路数（设备静态规格）|links|gpu|mtml.Device.MtLinkSpec|
