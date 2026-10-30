---
title: Profiling API
type: docs
author: HUATUO Team
date: 2026-10-21
weight: 5
---

This reference covers profiling task requests, responses, and states. For a first collection, see the [Profiling Guide](/docs/best-practice/continuous-profiling_en.md#quick-start).

huatuo-apiserver creates, supervises, and stops profiling tasks through `/v1/profiling`. Each HTTP creation request is an independent execution. Profiling tasks are persisted as Jobs in huatuo-apiserver; the node keeps only in-memory operations and profiler processes.

Task control does not depend on Elasticsearch. Raw-result queries and dashboard links require it. huatuo-bamai and huatuo-apiserver must use the same index.

## Request Conventions

huatuo-apiserver listens on `:12740` by default. These examples set the address and Bearer token through environment variables:

```bash
API_BASE="http://127.0.0.1:12740"
API_TOKEN="REPLACE_WITH_RANDOM_HEX"
```

Every request must supply the configured Bearer token in `Authorization`:

```text
Authorization: Bearer REPLACE_WITH_RANDOM_HEX
```

Non-admin users need permissions for `/v1/profiling` and `/v1/profiling/**`. Permissions may include an HTTP method prefix, such as `GET /v1/profiling/**`. Successful responses contain only `data`:

```json
{
  "data": {}
}
```

Error responses contain a stable error code and a readable message:

```json
{
  "error": {
    "code": "result_unavailable",
    "message": "Job state does not provide a complete Profiling result"
  }
}
```

Clients should branch on `error.code`, not parse `error.message`.

## Query Capabilities

Before creating a task, query supported profiling types, languages, CPU and memory modes, and runtime parameters:

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/capabilities"
```

`data.items` is a static capability table shipped with each version. Entries contain:

| Field | Description |
| --- | --- |
| `type` | Profiling type: `cpu` or `memory` |
| `language` | Target language |
| `modes` | Allowed `mode` values for this type/language combination |
| `supports_binary_match` | Whether `binary_match_path` is allowed |
| `supported_scopes` | Allowed `scope` values |

CPU profiling for `c`, `c++`, and `go` supports `oncpu` and `offcpu`; `java` and `python` support only `oncpu`. Memory profiling supports:

| Language | `mode` | Description |
| --- | --- | --- |
| `c`, `c++`, `go` | `virtual_alloc` | Virtual address space allocations |
| `c`, `c++`, `go` | `physical_alloc` | Physical-page allocations |
| `c`, `c++`, `go` | `physical_usage` | Live bytes of physical pages tracked in this collection |
| `java` | `object_alloc` | JVM object allocations |
| `java` | `object_usage` | JVM live objects |
| `python` | `object_usage` | Allocations made after Memray attaches and still live at collection end; bytes, not RSS |

Python memory jobs use the installed Memray runtime, require `gdb` and ptrace
permission on the node, and upload one final retained profile. The API uses
Python-only stacks. To collect one, use the Java memory request below with
`"language": "python"`, keeping `"mode": "object_usage"` and
`"scope": "container"`. A job fails before attachment if its selected Python
processes exceed the node's positive `Profiling.MaxConcurrentProcesses` limit.
Concurrent memory jobs against the same Python process are rejected without
interrupting the active capture.

## Create a Task

JSON parameters for `POST /v1/profiling`:

| Parameter | Required | Description |
| --- | --- | --- |
| `type` | Yes | Profiling type: `cpu` or `memory` |
| `language` | Yes | Target process language; must match the profiling type |
| `mode` | Yes | Mode declared by the capability table for the type and language |
| `duration_seconds` | Yes | Collection duration in seconds |
| `hostname` | Yes | Hostname of the node running the target process, used for scheduling |
| `scope` | Yes | `host` or `container`, as allowed by the capability table |
| `container_id` | For container scope | Target container ID |
| `binary_match_path` | No | Executable-path match, when allowed by the capability table |

Every request creates a new task, even with identical content. Exceeding per-node or process-wide active task quotas returns HTTP 429.

Create a host Go CPU profiling task:

```bash
curl -sS -i \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "cpu",
    "language": "go",
    "mode": "oncpu",
    "scope": "host",
    "duration_seconds": 60,
    "hostname": "node-01"
  }' \
  "${API_BASE}/v1/profiling"
```

Create a Java live-object profiling task in a container:

```bash
curl -sS -i \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "memory",
    "language": "java",
    "mode": "object_usage",
    "scope": "container",
    "duration_seconds": 60,
    "container_id": "9f4c2f1a8b7d",
    "hostname": "node-01"
  }' \
  "${API_BASE}/v1/profiling"
```

Success returns `201 Created`, a `Location` header pointing to the new task, and the task ID needed for later queries:

```json
{
  "data": {
    "request_id": "<profile-job-id>",
    "hostname": "node-01",
    "duration_seconds": 60,
    "scope": "host",
    "type": "cpu",
    "language": "go",
    "mode": "oncpu",
    "status": "pending",
    "created_at": "2026-08-24T10:00:00Z",
    "updated_at": "2026-08-24T10:00:00Z"
  }
}
```

```bash
JOB_ID="<profile-job-id>"
```

## Query Tasks

**Task list**

`GET /v1/profiling` supports these query parameters:

| Parameter | Default | Description |
| --- | --- | --- |
| `limit` | `100` | Page size from 1 to 1000 |
| `offset` | `0` | Starting offset, at least 0 |

Query the 20 most recent profiling tasks:

```bash
curl -sS -G \
  -H "Authorization: Bearer ${API_TOKEN}" \
  --data-urlencode "limit=20" \
  --data-urlencode "offset=0" \
  "${API_BASE}/v1/profiling"
```

The server returns only profiling tasks, ordered by `created_at` descending. `data.items` is the task array; `data.limit` and `data.offset` are the effective pagination parameters; `data.has_more` indicates another page. Non-admin users see only their own tasks.

**Task details**

`GET /v1/profiling/:request_id` returns one task:

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/${JOB_ID}"
```

Task information is in `data`:

| Field | Description |
| --- | --- |
| `request_id` | Profiling task ID used in all subsequent requests |
| `container_id` | Target container ID; omitted for host tasks |
| `hostname` | Target node hostname |
| `duration_seconds` | Requested profiling duration in seconds |
| `scope` | `host` or `container` |
| `type` | `cpu` or `memory` |
| `language` | Target process language |
| `mode` | Profiling mode selected from the capability table |
| `binary_match_path` | Executable match path; omitted when unused |
| `status` | Current task status |
| `terminal` | Terminal details `{outcome, reason, message}`; omitted before termination |
| `created_at` | Task creation time |
| `updated_at` | Last task status update time |
| `started_at` | Actual start time; omitted until execution is observed |
| `ended_at` | Time of entering a terminal state; omitted while running |
| `result_url` | Dashboard link returned by the single-task detail endpoint when complete results are available |

Task states:

| State | Description |
| --- | --- |
| `pending` | Created and waiting for agent execution |
| `running` | Agent is collecting profile data |
| `stopping` | Stop intent is persisted and being processed asynchronously |
| `terminal` | Task ended; see `terminal.outcome` |

`terminal.outcome` is `completed`, `failed`, `stopped`, or `unknown`. For `failed`, inspect `terminal.reason` and `terminal.message`. `stopped` means an explicit stop and possibly incomplete results. `unknown` means huatuo-apiserver cannot confirm the final operation state, although durably published results may still be available.

`operation_lost` and `execution_timed_out` are `terminal.reason` codes, not task states. Other reasons include startup or stop timeouts, unavailable nodes, execution-capacity limits, startup or runtime failure, `invalid_node_request` for an invalid node request constructed by huatuo-apiserver, and protocol errors.

## Get Results

`GET /v1/profiling/:request_id/raw` returns raw windows for tasks with `terminal.outcome` of `completed` or `unknown` only while the durable publication marker exists. A missing or expired marker returns `result_not_found`. Active states return `result_not_ready`; `failed` and `stopped` return `result_unavailable`.

Responses may be large; save them directly to a file:

```bash
curl -sS \
  -H "Authorization: Bearer ${API_TOKEN}" \
  -o profile-raw.json \
  "${API_BASE}/v1/profiling/${JOB_ID}/raw?limit=100&offset=0"
```

Windows are in `data.items`; `data.limit`, `data.offset`, and `data.has_more` describe pagination. Each record includes `uploaded_timestamp`, `started_timestamp`, `profile_type`, and pprof-compatible `profile` data. A durably published empty result succeeds with an empty `items` array. Explicit `limit=0` is invalid; only omission uses the default. `limit` defaults to 20 and is capped at 100. Encoded Profile data above 64 MiB returns `413 result_too_large`; reduce `limit` and retry.

Flame graphs read and merge batches of 100 windows, with a total limit of 100000 per request. At one window per second for an hour, all 3600 windows participate. Exceeding 100000 returns an error asking for a narrower time range or filters. Query or merge failures never return partial flame graphs. Raw Profile lists remain scoped to a task; their page limit does not cap aggregation for a complete flame graph.

## Stop a Task

Stopping is an asynchronous intent accepted for tasks in `pending`, `running`, or `stopping`:

```bash
curl -sS \
  -X POST \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "${API_BASE}/v1/profiling/${JOB_ID}/stop"
```

Success returns `200 OK` with the current task snapshot. Terminal tasks return `409 Conflict`. Because results may be incomplete, `stopped` tasks do not support result queries.

## Unified flame graph queries

Grafana uses the Pyroscope-compatible Protobuf endpoints under `/v1/profiling/flamegraph/`.
`SelectMergeStacktraces` selectors require `scope="host"` or `scope="container"`.
`region` is the Agent's configured region; `hostname` is only the collection node.
Container queries require `container_id` or one task `id`. Host queries cannot include container filters.
For example: `{region="example",hostname="node-01",scope="container",container_id="…",id="…"}`.

The shared dashboard UID is `continuous-profiling`; variables are ordered as Region, node,
scope, container, profile type, and task. The timeline and flame graph use the same task filter.
With All containers selected, the task still identifies one target. Physical usage always
requires one task and selects only the latest snapshot.
The task summary uses the Infinity data source in `pyroscope.yaml`, with the same API token
as the Pyroscope data source. `result_url` includes the stored windows' Region and actual
profile type. Results without windows do not provide a dashboard link.
