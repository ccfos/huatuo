---
title: 自动追踪字段
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

本文说明自动追踪记录的字段、单位和示例。适用场景见[自动追踪](/docs/key-feature/autotracing_zh.md)，配置节和参数见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)。下文阈值为内置默认值，配置文件可覆盖。

## 结果

自动追踪事件按已启用的存储配置写入 Elasticsearch 或本地文件。可通过 `tracer_type=autotracing`、`tracer_name`、主机或容器标识及时间范围筛选。下列 JSON 为说明字段结构的示例，并非实测结果。

CPU 示例使用 `perf`。使用 `profiler` 时，事件保留触发指标，profile 独立存入 Elasticsearch，并与事件共用 `tracer_id`。存储要求与查看方式见[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md#7-自动追踪)。

### 公共字段

记录包含以下公共字段；容器字段仅在关联到容器时提供。

| 字段 | 说明 |
| --- | --- |
| `hostname` | 节点主机名 |
| `region` | 节点所属区域 |
| `uploaded_timestamp` | 数据上传时间 |
| `container_id` | 容器 ID |
| `container_hostname` | 容器主机名 |
| `container_host_namespace` | 容器的 Kubernetes 命名空间 |
| `container_type` | 容器类型 |
| `container_qos` | 容器 QoS 级别 |
| `tracer_name` | 事件名称（如 `cpusys`、`memburst` 等） |
| `tracer_id` | 本次追踪 ID |
| `started_timestamp` | 开始追踪的时间 |
| `tracer_type` | 观测类型，自动追踪记录固定为 `autotracing` |
| `tracer_data` | 特定事件私有数据（详见各事件说明） |

### 宿主机系统态 CPU

追踪器：`cpusys`。通过 `AutoTracing.CPUSys.Tool` 选择 `perf`（默认）或 `profiler`。

周期性读取 `/proc/stat`，计算宿主机 CPU 系统态占用率及相邻两次采样的增量。占用率超过 45% 且增量超过 20 个百分点时，触发宿主机 CPU 采样，生成 CPU 火焰图。

以上为内置默认阈值，全局默认冷却时间为 30 分钟。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "cpusys",
    "tracer_data": {
        "system_percent": 52,
        "system_percent_threshold": 45,
        "system_percent_delta": 25,
        "system_percent_delta_threshold": 20,
        "flamedata": [
            {"level": 0, "value": 1000, "self": 0, "label": "all"},
            {"level": 1, "value": 350, "self": 350, "label": "do_syscall_64"}
        ]
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `system_percent` | 触发时宿主机 CPU 系统态占用率（%） |
| `system_percent_threshold` | 系统态占用率触发阈值（%） |
| `system_percent_delta` | 相邻两次采样的系统态占用率增量（百分点） |
| `system_percent_delta_threshold` | 系统态增量触发阈值（百分点） |
| `flamedata` | `perf`：火焰图帧列表，帧字段见下文；`profiler`：`null`，通过事件的 `tracer_id` 查询独立存储的 profile |
| `flamedata.level` | 调用栈层级深度 |
| `flamedata.value` | 该帧（含子帧）的采样计数 |
| `flamedata.self` | 该帧自身（不含子帧）的采样计数 |
| `flamedata.label` | 函数或进程名称标签 |

### 容器 CPU

追踪器：`cpuidle`。通过 `AutoTracing.CPUIdle.Tool` 选择 `perf`（默认）或 `profiler`。

周期性读取容器 cgroup CPU 统计。用户态、系统态或总占用率中的任一项同时超过占用率阈值和相邻采样增量阈值时，触发容器级 CPU 采样。

同一容器默认冷却 30 分钟，目标范围由 `AutoTracing.CPUIdle.Filter` 控制。内置默认阈值如下：

| 指标 | 占用率阈值 | 增量阈值 |
| --- | --- | --- |
| 用户态 | 75% | 45 个百分点 |
| 系统态 | 45% | 20 个百分点 |
| 总占用率 | 90% | 55 个百分点 |

示例（省略部分公共字段）：

```json
{
    "tracer_name": "cpuidle",
    "tracer_data": {
        "user_percent": 80,
        "user_percent_threshold": 75,
        "user_percent_delta": 48,
        "user_percent_delta_threshold": 45,
        "system_percent": 12,
        "system_percent_threshold": 45,
        "system_percent_delta": 5,
        "system_percent_delta_threshold": 20,
        "total_percent": 92,
        "total_percent_threshold": 90,
        "total_percent_delta": 53,
        "total_percent_delta_threshold": 55,
        "flamedata": [
            {"level": 0, "value": 1000, "self": 0, "label": "all"},
            {"level": 1, "value": 800, "self": 800, "label": "java/com.example.App.main"}
        ]
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `user_percent` / `user_percent_threshold` | 触发时容器 CPU 用户态占用率（%）及其阈值 |
| `user_percent_delta` / `user_percent_delta_threshold` | 用户态占用率增量（百分点）及其阈值 |
| `system_percent` / `system_percent_threshold` | 触发时容器 CPU 系统态占用率（%）及其阈值 |
| `system_percent_delta` / `system_percent_delta_threshold` | 系统态占用率增量（百分点）及其阈值 |
| `total_percent` / `total_percent_threshold` | 触发时容器 CPU 总占用率（%）及其阈值 |
| `total_percent_delta` / `total_percent_delta_threshold` | 总占用率增量（百分点）及其阈值 |
| `flamedata` | `perf`：容器火焰图帧列表，帧字段同 `cpusys`；`profiler`：`null`，通过事件的 `tracer_id` 查询独立存储的 profile |

### 容器 D 状态负载

追踪器：`dload`。

通过 netlink 及 cgroup 读取容器内进程状态，计算不可中断（D 状态）进程负载的指数加权移动平均值（EMA）。EMA 超过阈值（默认 5）时触发，同一容器默认冷却 30 分钟。

结果包含容器及宿主机 D 状态进程的内核栈，通过 `AutoTracing.IssuesList` 标记已知问题。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "dload",
    "tracer_data": {
        "threshold": 5,
        "nr_sleeping": 120,
        "nr_running": 4,
        "nr_stopped": 0,
        "nr_uninterruptible": 8,
        "nr_iowait": 3,
        "load_avg": 7.23,
        "dload_avg": 6.81,
        "known_issue": "",
        "stack": "task:java            state:D stack:    0 pid: 12345 tgid: 12345 ...\n  io_schedule+0x18/0x40\n  ext4_file_write_iter+0x..."
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `threshold` | D 状态负载 EMA 触发阈值 |
| `nr_sleeping` | 容器内睡眠状态进程数 |
| `nr_running` | 容器内运行状态进程数 |
| `nr_stopped` | 容器内停止状态进程数 |
| `nr_uninterruptible` | 容器内不可中断（D 状态）进程数 |
| `nr_iowait` | 容器内 I/O 等待状态进程数 |
| `load_avg` | 触发时容器负载均值 |
| `dload_avg` | 触发时容器 D 状态负载 EMA 值 |
| `known_issue` | 命中的已知问题描述（为空表示未命中） |
| `stack` | D 状态进程的内核调用栈（多进程多行文本） |

### 磁盘 I/O

追踪器：`iotracing`。

每 5 秒读取 `/proc/diskstats`。触发条件需连续两个采样周期成立：非 NVMe 设备检查利用率；NVMe 设备要求利用率和读、写吞吐率之一同时超阈值；各设备也会独立检查读、写平均等待时间。采集结果包含进程及文件 I/O 速率、I/O 调度等待栈。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "iotracing",
    "tracer_data": {
        "reason_snapshot": {
            "type": "ioutil",
            "device": "sda",
            "major_num": 8,
            "minor_num": 0,
            "iostatus": {
                "read_bps": 125829120,
                "read_iops": 450,
                "read_await": 12,
                "write_bps": 2202009600,
                "write_iops": 890,
                "write_await": 145,
                "io_util": 95,
                "queue_size": 32
            }
        },
        "process_file_io_stats": [
            {
                "pid": 12345,
                "comm": "java",
                "container_hostname": "app-pod-xxx",
                "total_fs_read_bps": 0,
                "total_fs_write_bps": 52428800,
                "total_disk_read_bps": 0,
                "total_disk_write_bps": 49152000,
                "total_files": [
                    {
                        "path": "/data/logs/app.log",
                        "dev_name": "sda",
                        "fs_write_bps": 52428800,
                        "disk_write_bps": 49152000
                    }
                ],
                "total_file_count": 1
            }
        ],
        "io_schedule_timeout_stacks": [
            {
                "pid": 12345,
                "tid": 12346,
                "cpu": 2,
                "comm": "java",
                "container_hostname": "app-pod-xxx",
                "schedule_latency_us": 250000,
                "stack": [
                    "io_schedule+0x18/0x40",
                    "ext4_file_write_iter+0x2a0/0x4c0"
                ]
            }
        ]
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `reason_snapshot` | 触发原因、设备及当时的磁盘指标；`type` 为 `ioutil`、`read_bps`、`write_bps`、`read_await` 或 `write_await` |
| `reason_snapshot.iostatus` | `*_bps` 为字节/秒，`*_iops` 为次/秒，`*_await` 为毫秒，`io_util` 为百分比，`queue_size` 为平均队列深度 |
| `process_file_io_stats` | 按进程汇总采集窗口内的文件 I/O，包含 PID、进程名和容器主机名 |
| `total_fs_read_bps` / `total_fs_write_bps` | 文件系统读、写速率（字节/秒） |
| `total_disk_read_bps` / `total_disk_write_bps` | 磁盘读、写速率（字节/秒） |
| `total_files` / `total_file_count` | 采集窗口内访问的文件明细子集及未截断的文件总数；文件明细中的 `*_bps` 为字节/秒，`*_us` 为微秒 |
| `io_schedule_timeout_stacks` | I/O 调度等待事件，包含 PID、TID、CPU、进程名和容器主机名 |
| `schedule_latency_us` / `stack` | I/O 调度等待时长（微秒）与内核栈帧列表 |

### 宿主机匿名内存

追踪器：`memburst`。

默认每 10 秒采样一次宿主机匿名内存，填满固定长度为 60 个采样点的滑动窗口后开始判断。当前匿名内存达到窗口最早值的 2 倍，且占宿主机总内存至少 70% 时触发；默认冷却时间为 30 分钟。

结果按 RSS 降序记录前 N 个进程（默认 10 个），并依次尝试为排名前 `min(N, 3)` 个进程采集运行时快照。Go、HotSpot 和 CPython 使用对应采集器；不支持的运行时返回 `unavailable`，并在可用时保留进程内存计数。采集失败不代表内存使用为零。

每个进程的采集预算为 2 秒，每份快照最多保留 10 条内存对象记录，JSON 上限为 512 KiB。超时为协作式，不能中断正在执行的同步系统调用，因此不保证在 2 秒内返回。

整个 `tracer_data` 上限为 2 MiB，落盘排名最多 1,000 条。超限时先移除排名靠后的快照，再按需裁剪基础排名尾部，并设置 `snapshot_reason`；取消或降级记录也遵循这些限制。

示例（省略部分公共字段）：

```json
{
    "tracer_name": "memburst",
    "tracer_data": {
        "top_memory_usage": [
            {
                "PID": 3456,
                "ProcessName": "go-service",
                "MemSize": 268435456
            }
        ],
        "process_snapshots": [
            {
                "pid": 3456,
                "process_name": "go-service",
                "language": "go",
                "snapshot": {
                    "runtime_version": "go1.24.0",
                    "status": "complete",
                    "duration_ms": 25,
                    "entries": [
                        {
                            "kind": "inuse_space_objects",
                            "name": "main.allocate",
                            "bytes": 1048576,
                            "objects": 16,
                            "stack": [
                                "main.allocate fixture.go:42"
                            ]
                        }
                    ]
                },
                "process_memory": {
                    "status": "complete",
                    "rss_bytes": 268435456,
                    "rss_anon_bytes": 260046848
                }
            }
        ]
    }
}
```

| 字段 | 说明 |
| --- | --- |
| `top_memory_usage` | 按 RSS 降序排列的进程列表 |
| `top_memory_usage.PID` | 进程 PID |
| `top_memory_usage.ProcessName` | 进程名称 |
| `top_memory_usage.MemSize` | 进程 RSS（字节） |
| `process_snapshots` | 可选，按排名顺序记录进程快照；每项包含 `pid`、`process_name`、`language`、`snapshot` 和可选的 `process_memory` |
| `snapshot_reason` | 可选，说明取消、队列满或整体事件裁剪；单进程失败原因见 `snapshot.status_reason` |

`snapshot` 字段：

| 字段 | 说明 |
| --- | --- |
| `runtime_version` | 可选，目标运行时版本 |
| `status` / `status_reason` | 状态及可选的原因说明；状态为 `complete`、`partial`、`unavailable` 或 `failed` |
| `duration_ms` | 采集耗时（毫秒） |
| `output_truncated` | 可选，表示快照输出被裁剪 |
| `entries` | 可选，内存对象排序条目列表 |
| `entries.kind` / `entries.name` | 条目类型和名称，含义取决于运行时 |
| `entries.bytes` / `entries.objects` | 条目内存量（字节）和对象数 |
| `entries.average_bytes` / `entries.stack` | 可选，平均对象大小（字节）和调用栈 |

`process_memory` 是与语言无关的进程内存计数，使用 `status` 和可选的 `status_reason` 表示可用性。以下字段均为可选，缺失表示不可用，不表示零：

| 字段 | 含义（单位：字节） |
| --- | --- |
| `virtual_bytes` / `rss_bytes` | 虚拟内存 / RSS |
| `rss_anon_bytes` / `rss_file_bytes` / `rss_shmem_bytes` | RSS 中的匿名页 / 文件页 / 共享内存 |
| `swap_bytes` / `page_table_bytes` | 交换空间 / 页表内存 |
