---
title: MCP Diagnostics
type: docs
description: Read-only event, profile, and metric queries for AI agents
author: HUATUO Team
date: 2026-09-19
weight: 7
---

# MCP diagnostics

`huatuo-apiserver` exposes event queries and profiling data analysis,
Prometheus-compatible metrics, and diagnostic guides through MCP. All operations are read-only: they do
not trigger collection, change configuration, or determine whether a target is faulty.

## Access

- Endpoint: `/v1/mcp`, stateless Streamable HTTP on the API server's port with bearer authentication.
- Disabled by default. Enable with `[MCP] Enabled=true`, `--enable-mcp`, or `ENABLE_MCP=true`.
- Grant non-admin users `POST /v1/mcp`, `GET /v1/mcp`, and `DELETE /v1/mcp` as needed by their client.
- Events use Elasticsearch/OpenSearch; metrics use Prometheus-compatible APIs.
  Connections are configured separately, never inherit application storage
  addresses or credentials, and are not probed at startup.

See [API server configuration](/docs/configuration/huatuo-apiserver-configuration_en.md#9-model-context-protocol)
for connections, authentication, and budgets. Use `tools/list` and its `inputSchema`
as the authority for available tools and arguments.
JSON examples below show only `tools/call` `arguments`, not full JSON-RPC
requests. Replace target, tracer, and metric names with actual values.

## Tools

| Tool | Purpose | Required arguments |
|------|---------|--------------------|
| `diagnostic_guide` | Diagnostic workflow; always registered | None; optional `stage`, `subsystem` |
| `discover_event_types` | Tracer names and profile payload markers | `region`, `time_range` |
| `query_events` | Target events or regional aggregation | See the two scopes below |
| `query_event_profiles` | Analyze profiling data | `region`, `time_range`, one `tracer_name`, targets |
| `discover_metrics` | Candidate metric names or exact-name verification | `region`, `time_range` |
| `query_metrics` | Target range queries or regional instant statistics | See the two scopes below |

Event and metric tools are registered only for configured backends, independently
of startup reachability. `diagnostic_guide.stage` accepts `overview` (default),
`subsystem`, `correlation`, or `profiler`. The subsystem stage also requires
`cpu`, `memory`, `io`, `network`, or `hardware` as `subsystem`.

### Common arguments

- Data tools require a nonempty array of exact `region` names; `all`, `_all`, and `*` are rejected.
- `time_range` accepts a Go duration (`1h`, `168h`, not `7d`), an RFC3339 start
  (ending now), or `start/end`. The end may also be `now`. The interval must be positive.
- The general span defaults to 30 days. `MCP.MaxQueryTimeRange=0` disables it,
  but not the 7-day regional event cap or 1-hour PromQL selector cap.
- Event/profile targets are `host`, `container_host`, and `container_hostnamespace`
  arrays, each allowing up to 100 values. Values within a field are ORed;
  different fields are ANDed. Investigate independent host/container targets in separate calls.

### Event queries

`query_events` defaults to `scope="target"` and requires targets and a `tracer_name`
array (up to 100 names). `size` defaults to 200, capped by the configured document
budget. It is a total across regions, not a per-tracer limit. Large multi-tracer
queries use count-aware batching; bounded samples need not cover every tracer.

```json
{"region":["us"],"time_range":"1h","host":["node-a"],"tracer_name":["cpusys"],"size":20}
```

Explicit `scope="region"` provides statistics and candidate discovery, not raw events:

- Exactly one region, `time_range`, and `group_by` are required; target/tracer filters are optional.
- Choose distinct `group_by` fields from `hostname`, `container_hostname`,
  `container_host_namespace`, and `tracer_name`.
- The window is at most 7 days, also bounded by a lower configured span.
- `size` defaults to 500, capped at 1000 independently of document budgets.
- Buckets contain `count`, `first_seen`, and `last_seen`, in key order,
  **not top-count order**. Missing keys are null. Multivalued fields can place
  one event in several buckets; counts are not machine counts.
- `count_exact` describes returned bucket counts, not complete enumeration.
  Overflow marks truncation; the tool does not page automatically.
- Complete, uniform keyword groupings are required. Mixed projections and source
  scans are rejected. Each search requests at most a 10-second backend timeout,
  further limited by the remaining Tool budget.

```json
{"scope":"region","region":["us"],"time_range":"1h","group_by":["hostname","tracer_name"],"size":100}
```

For follow-up evidence, build a fresh target request: remove `group_by`, map the
actual `hostname` to `host`, supply a `tracer_name` array, and use returned
`start_time/end_time` as an absolute range. `container_hostname` maps to
`container_host`; `container_host_namespace` maps to `container_hostnamespace`.
Null keys are not targets. Skip statistics when the target is already known.

### Event discovery and profiling analysis

`discover_event_types` returns `tracers`. `has_profiler` only indicates frame/pprof
payload markers under `tracer_data.flamedata`, not valid payloads. Results are
cached for 5 minutes by region and time-range string, so moving windows may reuse
older results. This is not a live, complete inventory; queries can name tracers
not returned by discovery.

`query_event_profiles` accepts one string `tracer_name` and analyzes
profiling data stored in events: frame arrays or valid pprof objects under
`tracer_data.flamedata`. It does not start or trigger profiling collection.
Matching records are aggregated by default. Supply `record_id=source.record_id`
to analyze one record: exactly one region and the target/time filters are still
required; multiple matching records are rejected.

| Argument | Default | Maximum |
|----------|--------:|--------:|
| `top_n` | 50 | 1000 |
| `max_call_paths` | 5 | 20 |
| `max_stack_depth` | 12 | 32 |

`analysis_type` is `self_time` (default) or `total_time`, ranking exclusive/inclusive
weights. Paths run from the hotspot toward callers. Total-time paths include
zero-self entries; self-time paths require self weight. Inspect `summary.sample_type`
and `sample_unit`: IRQ counts and other weights are not necessarily CPU time.
Incompatible types/units cannot be merged. `coverage` is returned hotspots' share
of self weight, not CPU utilization or query completeness. Input truncation and
unsupported payloads appear in `summary.truncated`, `unsupported_documents`, and `query_status`.

### Metric discovery

`discover_metrics` reads the backend name catalog and applies `MetricPrefix`
locally. Default results have `availability="unknown"`; a catalog name does not
prove a series exists in the requested region.

Pass up to 5 exact `metric_names` to verify by name, region, and time window:

- `present`: a series was found; scoped label keys and backend HELP/TYPE/UNIT are then requested.
- `absent`: verification succeeded but found no series.
- `unknown`: verification failed; results are partial, not proof of absence.

The metadata endpoint is not region/time scoped. `metadata_status` is `available`,
`unavailable`, or `ambiguous_or_incomplete`; missing/conflicting units are not
inferred. Read label values from query results.

### Metric queries

The two `query_metrics` scopes use different arguments:

| | `target` (default) | `region` |
|---|---|---|
| Purpose | Known-target range evidence | Single-region instant statistics |
| Time | Required `time_range`; no `time` | Required RFC3339 `time`; no `time_range` |
| Target | Exactly one string `host` or `container_host` | Target arguments rejected |
| `step` | Default `1m`, minimum `15s` | Rejected |
| PromQL | Same exact target matcher in every selector | Explicit metric names, every selector under aggregation |
| Output cap | Configured series budget; overflow truncates | Lower of 1000/configured budget; overflow fails |

Both require `query` and a `region` array. Omit region matchers; the server injects
them. A nonempty `MetricPrefix` restricts every selector's metric name. PromQL is
limited to 4096 bytes and matrix selectors to 1 hour; subqueries, `offset`, and `@`
are unsupported. Range queries allow at most 11000 timestamps per series.
Regional statistics additionally allow at most 4 selectors/100 AST nodes and no
vector-to-vector binary operations. Lookback is also bounded by a lower configured
span; queries/retries share at most 10 seconds.

Metric and target names below are illustrative; substitute actual discovered names and labels:

```json
{"region":["us"],"host":"node-a","time_range":"1h","query":"huatuo_cpu_usage{host=\"node-a\"}"}
```

```json
{"scope":"region","region":["us"],"time":"2026-10-27T08:00:00Z","query":"topk(20, avg by (host) (avg_over_time(huatuo_cpu_usage[5m])))"}
```

`[5m]` looks back from `time`; it does not scan a longer interval. To investigate a
candidate, remove `time`, add the target argument and matching selector, and build
a target request with the same absolute lookback window as `time_range`. Small
outputs do not guarantee cheap scans; backend resource limits remain necessary.
Both regional event/metric statistics return `purpose="statistics"` and `usage_hint`.
Statistics, empty results, and NaN do not establish target health or availability.

## Results and errors

Check MCP `isError` before interpreting JSON `status`:

| Result | Meaning and action |
|--------|--------------------|
| `isError=true`, `status="error"` | Failed query; inspect `error_class`, `message`, `errors`, `retryable`, `suggestion` |
| `status="partial"` | Use returned data only; disclose errors, warnings, and truncation gaps |
| `status="success"` | Successful call; empty data means no matches for this query, not a health verdict |

Parameter and backend errors are JSON text, also available as `structuredContent`. Invalid parameters
use `invalid_argument`; correct them instead of retrying unchanged. Data tools'
`query_status` fields vary; missing totals are unknown. Multi-region document,
series, and discovery budgets are divided across regions; a budget smaller than
the number of regions is rejected.

- Target event/profile queries and metric discovery provide regional `query_status`;
  metric queries retain backend responses and warnings.
- Truncated target metric responses contain `mcp_truncated=true` per region and
  top-level partial status. Regional metric statistics do not return truncated output.
- Count failure may fall back to bounded Search; returned events remain usable but totals are unknown.
- ES 404, shard failures, and malformed responses are not successful empty results.
  Prometheus missing/null `result`, unknown result types, and invalid scalar/string
  structures produce decoding errors.
- `backend_diagnostics` retains failed attempts. `recovered=true` means that
  operation eventually succeeded; historical errors alone do not imply missing
  data. At most 32 operations/16 failed attempts per operation are retained;
  omitted counts are reported separately.
- Event/profile `source` contains `backend`, `record_id`, `region`, and optional
  `locator`. MCP does not generate frontend URLs; callers can apply deployment templates.

### Event time and compatibility reads

Time filtering includes both endpoints and matches any of `uploaded_timestamp`,
`uploaded_time`, or `time`. The returned `timestamp` uses the first non-missing,
non-null field in that order and sorts newest first; it may fall outside the
filter interval. `observed_timestamp` is metadata only. Date strings and integer
`epoch_millis` as JSON numbers/numeric strings are supported; numeric timestamps
are always milliseconds. Large payload integers retain precision.

Unavailable indexed exact filters may fall back to bounded `_source` scans.
More than 500 candidates fails instead of returning apparently complete data.
Regional aggregation never uses this fallback. Reads preserve alias filters and
routing and create no Scroll cursors.

## Text guides and operations

- Resources cover storage models, subsystem tracers, patterns, and profile methods.
  Prompts are `auto-diagnosis` and `subsystem-diagnosis`. Both require host/container
  targets, exact regions, and time ranges; the latter also requires a subsystem.
  Prompt regions use comma-separated strings, unlike data tools' arrays.
- Embedded English guides live in `internal/mcp/guide/data`. `MCP.GuideOverlayDir`
  overrides/extends the `prompts/` and `resources/` hierarchy. Existing file edits
  apply on the next read; missing files fall back to embedded copies. New Resources
  require restart, embedded edits require rebuilding, and new prompt files do not
  automatically register new Prompts.
- `huatuo://data-manifest` reports file sizes and SHA256 hashes.
- `/metrics` reports MCP requests, concurrency, tools, backend attempts, retries,
  and rate limits, without query, target, region, tracer, or credential labels.
