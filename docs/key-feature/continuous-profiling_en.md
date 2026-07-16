---
title: Continuous Profiling
type: docs
description:
author: HUATUO Team
date: 2026-07-09
weight: 4
---

## Overview

**Continuous Profiling** performs long-running, continuous performance sampling of the operating system and applications, covering **CPU, memory, and lock** profiles. It produces standard pprof flame-graph data, persists samples to Elasticsearch, and supports aggregated viewing over arbitrary time windows in Grafana — providing a data foundation for capacity planning, performance regression analysis, and post-mortem diagnosis.

### Architecture

Continuous Profiling is built on three cooperating components:

| Component | Role | Description |
| --- | --- | --- |
| huatuo-apiserver | Control plane | Receives profiling jobs, dispatches them to target nodes, and exposes a Pyroscope-compatible flame-graph query API |
| huatuo-bamai | Data plane | Runs collection on the target node, sampling call stacks via eBPF (C/C++/Go) or third-party tools (Java/Python) |
| Grafana | Visualization | Connects directly to apiserver through the pyroscope datasource plugin to render flame graphs |

Supported languages and underlying implementations:

| Language | Profile types | Implementation |
| --- | --- | --- |
| C / C++ / Go | CPU / memory / lock | eBPF (perf_event / lock contention + stack maps) |
| Java | CPU / memory | async-profiler |
| Python | CPU / memory | py-spy / memray |

Profile type identifiers (used in Grafana queries):

| Type | profile_type |
| --- | --- |
| CPU | `process_cpu:cpu:nanoseconds:cpu:nanoseconds` |
| Memory | `memory:alloc_space:bytes:space:bytes` |
| Lock | `process_lock:lock:count:lock:count`<br>`process_lock:lock:nanoseconds:lock:nanoseconds` |

## Running

The simplest way is to bring up Elasticsearch, Prometheus, Grafana, huatuo-apiserver, and huatuo-bamai together with Docker Compose:

```bash
$ docker compose --project-directory ./build/docker up
```

Component addresses after startup:

| Component | Address |
| --- | --- |
| huatuo-apiserver | `http://127.0.0.1:12740` |
| huatuo-bamai metrics | `http://127.0.0.1:19704/metrics` |
| Grafana | `http://localhost:3000` (admin / admin) |
| Elasticsearch | `http://127.0.0.1:9200` |

Profiling-related configuration lives in the `[Profiling]` section of `huatuo-apiserver.conf`:

| Parameter | Default | Description |
| --- | --- | --- |
| `CPUProfilingInterval` | 10 | Single CPU sampling duration (seconds) |
| `MemoryProfilingInterval` | 10 | Single memory sampling duration (seconds) |
| `CPUSingleTraceTimeout` | 20 | Single CPU sampling timeout (seconds) |
| `MemorySingleTraceTimeout` | 20 | Single memory sampling timeout (seconds) |
| `ThirdPartyToolLimit` | 10 | Max concurrent third-party tools (async-profiler, etc.) |
| `FlameGraphBaseURL` | `http://localhost:8006/d` | Flame-graph dashboard base URL, used to build task result links |

> To make the `results.url` returned by a task point directly at Grafana, set `FlameGraphBaseURL` to the actual Grafana address (e.g. `http://localhost:3000/d`).

Apiserver API calls require an `Authorization` request header carrying the user ID (configured under `[[Auth.users]]` in `huatuo-apiserver.conf`).

> The default conf ships with no users configured, so the auth middleware is disabled and `Authorization` can be any non-empty value. In production, always configure real users under `[[Auth.users]]` and replace `<user-id>` with the actual ID.

## Collection: Host CPU Example

The following starts a CPU profile on a host. Host-level collection omits the `container` field; `target_process_language` is set to `go` (or `c`/`c++`) to trigger the eBPF native profiler:

```bash
$ curl -X POST http://127.0.0.1:12740/v1/profiles \
    -H "Content-Type: application/json" \
    -H "Authorization: <user-id>" \
    -d '{
        "type": "cpu",
        "target_process_language": "go",
        "hostname": "<target-host>",
        "duration": 600
    }'
```

Request fields:

| Field | Description |
| --- | --- |
| `type` | Profile type: `cpu` / `memory` / `lock` |
| `target_process_language` | Target language: `go`, `c`, `c++`, `java`, `python` |
| `hostname` | **Required**. Target host name; apiserver dispatches the job to the huatuo-bamai agent at `http://{hostname}:19704` (must match the hostname reported by the agent) |
| `duration` | Total profiling duration (seconds); the agent samples periodically at `CPUProfilingInterval` |
| `container` | Container hostname for container-level collection; leave empty for host-level |
| `target_exec_path` | Optional, filter target processes by executable path |
| `scope` | Collection dimension: `pid`, `tgid`, `cgroup`, `process-group`, or `all` |
| `pid` | PID/TGID target; with `process-group`, it can also be used to resolve the PGID |
| `cgroup_id` / `cgroup_path` | Cgroup target. Specify one; `container` also selects its cgroup |
| `process_group_id` | Process-group target |
| `labels` | Additional Prometheus-compatible labels (`[A-Za-z_][A-Za-z0-9_]*`) |

### Multi-dimensional collection and labels

Native CPU, memory, and lock profilers share the same collection scopes. The
legacy CLI names `thread` and `thread-group` remain aliases for `pid` and
`tgid`, so existing profiling commands continue to work.

```bash
# Exact thread
profiler -t cpu -l go --scope pid --pid 4242 -d 30

# Entire thread group/process
profiler -t cpu -l go --scope tgid --pid 4242 -d 30

# Cgroup v2 path (its inode is the kernel cgroup ID)
profiler -t cpu -l go --scope cgroup \
  --cgroup-path /sys/fs/cgroup/system.slice/example.service -d 30

# Process group; omit --process-group-id to derive it from --pid
profiler -t cpu -l go --scope process-group --pid 4242 -d 30
```

Every uploaded profile receives `profiling_scope` plus the applicable `pid`,
`tgid`, `cgroup_id`, `cgroup_path`, `container_id`, or `process_group_id` label. Custom
`--label key=value` labels are also accepted. Huatuo writes these dimensions
both as Pyroscope series labels and as standard pprof sample labels, which
keeps the payload usable by Pyroscope and Parca. The Pyroscope-compatible
`LabelNames`, `LabelValues`, and stacktrace selector endpoints expose the same
dimension names for querying and aggregation.

### Kernel lock profiling

Kernel lock profiling is available for native C/C++/Go targets. It measures
contention wait time or contention count for mutexes, spinlocks, and read/write
locks:

```bash
profiler -t lock -l go --scope tgid --pid 4242 \
  --lock-types mutex,spinlock,rwlock \
  --lock-mode time --lock-min-wait 1us -d 30
```

Use `--lock-mode count` for the number of contentions that reach the wait
threshold. `--lock-min-wait` suppresses short waits. The API accepts the
equivalent `lock_types`, `lock_mode`, and `lock_min_wait` fields.

The collector follows the same contention-only model as `perf lock
contention`: on Linux 5.19 and later it uses the kernel's
`lock:contention_begin` / `lock:contention_end` tracepoints. Older kernels,
including Ubuntu 20.04's 5.4 kernel, use feature-detected mutex, queued
spinlock, and queued rwlock slowpaths. It never falls back to instrumenting the
high-frequency `_raw_*_lock` acquisition paths. Counts and wait times are
aggregated in BPF before userspace reads them, which keeps event volume bounded
during heavy contention. The `perf` executable and `CONFIG_LOCK_STAT` are not
required; when neither contention tracepoints nor safe slowpath symbols are
available, the requested lock type fails with an explicit unsupported error.

Response returns the task ID:

```json
{ "id": "<task-id>" }
```

Collection flow:

1. apiserver creates the job and dispatches it to the huatuo-bamai agent on the target host.
2. huatuo-bamai loads an eBPF program (`perf_event_sw_cpu_clock`) and samples kernel and user stacks at the default 99 Hz.
3. Samples are symbolized, converted to pprof format, and written to Elasticsearch (the index name is the `[ElasticSearch].Index` setting in `huatuo-apiserver.conf`, default `huatuo_bamai`).

Query job status and stop a job:

```bash
# Query job status
$ curl -H "Authorization: <user-id>" \
    http://127.0.0.1:12740/v1/profiles/<task-id>

# Stop a job
$ curl -X PATCH http://127.0.0.1:12740/v1/profiles/<task-id> \
    -H "Content-Type: application/json" \
    -H "Authorization: <user-id>" \
    -d '{"status":"stopped"}'
```

On completion, the `results.url` field in the status response carries a flame-graph link built from `FlameGraphBaseURL`.

## Viewing

Flame graphs are viewed through pre-provisioned Grafana dashboards that load automatically with Docker Compose:

| Dashboard | UID | Scope |
| --- | --- | --- |
| Continuous Profiling(host) | `continuous-profiling-host` | Host |
| Continuous Profiling(container) | `continuous-profiling-container` | Container |

Open `http://localhost:3000/d/continuous-profiling-host`, select `hostname` and `type` (profile_type) to view the aggregated flame graph. The time-series panel at the top shows the sample distribution, and the flame-graph panel below supports aggregated viewing over a selectable time range.

> **How it works**: Grafana forwards flame-graph requests to the apiserver's `/v1/profiles/flamegraph/` path via the `grafana-pyroscope-datasource` plugin. The apiserver implements the Pyroscope Querier protocol (`SelectMergeStacktraces`, etc.), retrieving pprof data from Elasticsearch, merging it, and returning the result.
