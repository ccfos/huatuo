---
title: AutoTracing Fields
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

This reference covers AutoTracing record fields, units, and examples. See [AutoTracing](/docs/key-feature/autotracing_en.md) for use cases and [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md) for settings. Thresholds below are built-in defaults that configuration files can override.

## Results

Automatic tracing events are written to Elasticsearch or local files according to enabled storage settings. Filter by `tracer_type=autotracing`, `tracer_name`, host or container identifiers, and time range. The JSON examples illustrate field structure, not measured results.

CPU examples use `perf`. With `profiler`, events retain trigger metrics; profiles are stored separately in Elasticsearch with the same `tracer_id`. See [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md#7-automatic-tracing) for storage requirements and viewing instructions.

### Common Fields

Records contain these common fields. Container fields are provided only when a container is associated.

| Field | Description |
| --- | --- |
| `hostname` | Node hostname |
| `region` | Node region |
| `uploaded_timestamp` | Data upload time |
| `container_id` | Container ID |
| `container_hostname` | Container hostname |
| `container_host_namespace` | Container's Kubernetes namespace |
| `container_type` | Container type |
| `container_qos` | Container QoS class |
| `tracer_name` | Event name, such as `cpusys` or `memburst` |
| `tracer_id` | ID of this trace |
| `started_timestamp` | Trace start time |
| `tracer_type` | Observation type; always `autotracing` for AutoTracing records |
| `tracer_data` | Event-specific data described below |

### Host System CPU

Tracer: `cpusys`. Select `perf` (default) or `profiler` with `AutoTracing.CPUSys.Tool`.

Periodically reads `/proc/stat` to calculate host system-mode CPU utilization and its increase between consecutive samples. Utilization above 45% and an increase above 20 percentage points trigger host-level CPU sampling to generate a CPU flame graph.

These are built-in thresholds. The default global cooldown is 30 minutes.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `system_percent` | Host system-mode CPU utilization at trigger time (%) |
| `system_percent_threshold` | System-mode utilization threshold (%) |
| `system_percent_delta` | Increase between consecutive samples (percentage points) |
| `system_percent_delta_threshold` | Increase threshold (percentage points) |
| `flamedata` | `perf`: flame graph frames, with fields listed below. `profiler`: `null`; query the separate profile using the event `tracer_id` |
| `flamedata.level` | Stack depth |
| `flamedata.value` | Sample count for the frame, including children |
| `flamedata.self` | Sample count for the frame alone, excluding children |
| `flamedata.label` | Function or process name |

### Container CPU

Tracer: `cpuidle`. Select `perf` (default) or `profiler` with `AutoTracing.CPUIdle.Tool`.

Periodically reads container cgroup CPU statistics. Container-level CPU sampling starts when user, system, or total utilization exceeds both its utilization threshold and its increase threshold between consecutive samples.

The default cooldown is 30 minutes per container. `AutoTracing.CPUIdle.Filter` controls target scope. Built-in thresholds:

| Metric | Utilization Threshold | Increase Threshold |
| --- | --- | --- |
| User | 75% | 45 percentage points |
| System | 45% | 20 percentage points |
| Total | 90% | 55 percentage points |

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `user_percent` / `user_percent_threshold` | Container user-mode CPU utilization at trigger time (%) and its threshold |
| `user_percent_delta` / `user_percent_delta_threshold` | User-mode utilization increase (percentage points) and its threshold |
| `system_percent` / `system_percent_threshold` | Container system-mode CPU utilization at trigger time (%) and its threshold |
| `system_percent_delta` / `system_percent_delta_threshold` | System-mode utilization increase (percentage points) and its threshold |
| `total_percent` / `total_percent_threshold` | Container total CPU utilization at trigger time (%) and its threshold |
| `total_percent_delta` / `total_percent_delta_threshold` | Total utilization increase (percentage points) and its threshold |
| `flamedata` | `perf`: container flame graph frames, with the same fields as `cpusys`. `profiler`: `null`; query the separate profile using the event `tracer_id` |

### Container D-State Load

Tracer: `dload`.

Reads container process states through netlink and cgroups to calculate an exponential moving average (EMA) of uninterruptible (D-state) process load. It triggers when the EMA exceeds the threshold (default: 5), with a default 30-minute cooldown per container.

Results include kernel stacks of container and host D-state processes. `AutoTracing.IssuesList` labels known issues.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `threshold` | D-state load EMA trigger threshold |
| `nr_sleeping` | Sleeping processes in the container |
| `nr_running` | Running processes in the container |
| `nr_stopped` | Stopped processes in the container |
| `nr_uninterruptible` | Uninterruptible (D-state) processes in the container |
| `nr_iowait` | Processes waiting for I/O in the container |
| `load_avg` | Container load average at trigger time |
| `dload_avg` | Container D-state load EMA at trigger time |
| `known_issue` | Matched known-issue description; empty if none matched |
| `stack` | Kernel stacks of D-state processes as multiline text |

### Disk I/O

Tracer: `iotracing`.

Reads `/proc/diskstats` every 5 seconds. A trigger condition must hold for two consecutive intervals. Non-NVMe devices check utilization; NVMe devices require both utilization and either read or write throughput to exceed thresholds. Each device also checks mean read and write wait times independently. Results include process and file I/O rates and I/O scheduling wait stacks.

When running `iotracing` directly, `--enable-pprof` (disabled by default) serves profiles at `http://127.0.0.1:6000/debug/pprof/`; if the address is already in use, the command fails to start. These profiles describe only the `iotracing` process's Go runtime, not `huatuo-bamai` or the application processes performing disk I/O. Automatic tracing does not enable this option.

Example with some common fields omitted:

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

| Field | Description |
| --- | --- |
| `reason_snapshot` | Trigger reason, device, and disk metrics at that time; `type` is `ioutil`, `read_bps`, `write_bps`, `read_await`, or `write_await` |
| `reason_snapshot.iostatus` | `*_bps`: bytes/s; `*_iops`: operations/s; `*_await`: milliseconds; `io_util`: percentage; `queue_size`: mean queue depth |
| `process_file_io_stats` | File I/O in the collection window grouped by process, including PID, process name, and container hostname |
| `total_fs_read_bps` / `total_fs_write_bps` | Filesystem read/write rates (bytes/s) |
| `total_disk_read_bps` / `total_disk_write_bps` | Disk read/write rates (bytes/s) |
| `total_files` / `total_file_count` | A subset of file details and the untruncated total file count for the window; `*_bps` in file details uses bytes/s, and `*_us` uses microseconds |
| `io_schedule_timeout_stacks` | I/O scheduling wait events with PID, TID, CPU, process name, and container hostname |
| `schedule_latency_us` / `stack` | I/O scheduling wait duration (microseconds) and kernel stack frames |

### Host Anonymous Memory

Tracer: `memburst`.

By default, samples host anonymous memory every 10 seconds and evaluates after filling a 60-sample sliding window. It triggers when current anonymous memory reaches twice the oldest window value and at least 70% of total host memory. The default cooldown is 30 minutes.

Results list the top N processes by descending RSS (default: 10) and attempt runtime snapshots serially for the first `min(N, 3)` processes. Go, HotSpot, and CPython use their respective providers. Unsupported runtimes report `unavailable` and retain process memory counters when available. A failed capture does not mean zero memory usage.

Each process has a two-second capture budget. Each snapshot retains at most 10 memory object entries and 512 KiB of JSON. Timeouts are cooperative and cannot interrupt synchronous system calls, so capture may take longer than two seconds.

The complete `tracer_data` is limited to 2 MiB, with at most 1,000 persisted ranking entries. When limits are exceeded, the lowest-ranked snapshots are removed first, then ranking tails if needed, and `snapshot_reason` is set. Cancellation and fallback records follow the same limits.

Example (some common fields omitted):

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

| Field | Description |
| --- | --- |
| `top_memory_usage` | Processes ranked by descending RSS |
| `top_memory_usage.PID` | Process PID |
| `top_memory_usage.ProcessName` | Process name |
| `top_memory_usage.MemSize` | Process RSS (bytes) |
| `process_snapshots` | Optional process snapshots in rank order; each contains `pid`, `process_name`, `language`, `snapshot`, and optional `process_memory` |
| `snapshot_reason` | Optional explanation of cancellation, a full queue, or whole-event truncation; per-process failures use `snapshot.status_reason` |

`snapshot` fields:

| Field | Description |
| --- | --- |
| `runtime_version` | Optional target runtime version |
| `status` / `status_reason` | Status and optional explanation; status is `complete`, `partial`, `unavailable`, or `failed` |
| `duration_ms` | Capture duration (milliseconds) |
| `output_truncated` | Optional flag indicating truncated snapshot output |
| `entries` | Optional ranked memory object entries |
| `entries.kind` / `entries.name` | Entry type and name; their meaning depends on the runtime |
| `entries.bytes` / `entries.objects` | Entry memory size (bytes) and object count |
| `entries.average_bytes` / `entries.stack` | Optional average object size (bytes) and call stack |

`process_memory` contains language-independent process memory counters. Its `status` and optional `status_reason` describe availability. All counters below are optional; missing values mean unavailable, not zero:

| Field | Meaning (bytes) |
| --- | --- |
| `virtual_bytes` / `rss_bytes` | Virtual memory / RSS |
| `rss_anon_bytes` / `rss_file_bytes` / `rss_shmem_bytes` | Anonymous / file-backed / shared memory in RSS |
| `swap_bytes` / `page_table_bytes` | Swap space / page table memory |

### collect cgroup v2 task states with a BPF iterator

Kubernetes deployments must run Huatuo with `hostPID: true`. Without host PID namespace visibility, cgroup v2 dload is reported as unsupported instead of returning misleading zero counts.

### enable v2 load stats for metrics and dload

**Description** Reads container process states through netlink on cgroup v1 and through a batched BPF task iterator on cgroup v2. It computes an exponential weighted moving average (EMA) of the load contribution from uninterruptible (D-state) processes per container. When the EMA exceeds the threshold (default 5), kernel call stacks are collected for all D-state processes inside the container and on the host. Known-issue filtering (`issues_list`) reduces false positives. A 30-minute per-container cooldown applies. The cgroup v2 path walks all host tasks per sample; it requires readable kernel BTF and the BPF `task` iterator. Counts are non-hierarchical and include only tasks directly attached to each target cgroup. When task iterator support is definitively unavailable, the first sample marks `dload` as unsupported and stops this detector without periodic reload attempts. Verifier and other ordinary loading errors are not cached as unsupported; subsequent sampling attempts retry and report the error.
