# MCP tool usage guide

## Choose a tool

| Scenario | Tool |
|----------|------|
| Get diagnostic workflows, subsystem capabilities, and correlations | `diagnostic_guide` |
| Discover event types and profiling payload markers | `discover_event_types` |
| Query target events / regional event statistics | `query_events`: `target` / `region` |
| Analyze profiling hotspots and call paths | `query_event_profiles` |
| Find metric names and verify metrics, labels, and units | `discover_metrics` |
| Query target metric trends / regional metric statistics | `query_metrics`: `target` / `region` |

Use `tools/list` and `inputSchema` for available tools and arguments. JSON examples
below contain only tool `arguments`; replace illustrative names with actual values.

## Common arguments and time ranges

- `region`: required by data tools; an array of exact names such as `["us"]`, not `all`, `_all`, or `*`.
- `scope`: only for event/metric queries; defaults to `target`. Explicitly use `region`
  only for regional statistics, considering backend query cost.
- `time_range`: use fixed UTC bounds such as
  `"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z"` for 07:00–08:00 UTC; end must follow start.
  Required by all data tools except regional metric statistics, which use `time`.

## 1. diagnostic_guide: get guidance

Returns guidance, not data; no region, target, or time is needed.
`stage` accepts `overview` (default), `subsystem`, `correlation`, or `profiler`.
The subsystem stage also requires `subsystem`: `cpu`, `memory`, `io`, `network`, or `hardware`.

```json
{"stage":"subsystem","subsystem":"memory"}
```

## 2. discover_event_types: discover event types

Requires `region` and `time_range`, without targets. Returns tracer names and `has_profiler`.

```json
{"region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z"}
```

Discovery is cached for 5 minutes, not a live, complete inventory. Known exact tracer
names can be queried directly. `has_profiler` indicates payload markers, not guaranteed analyzability.

## 3. query_events: event details or statistics

### target (default): query host or container events

Requires `region`, `time_range`, a `tracer_name` **array**, and at least one target
**array**: `host`, `container_host`, or `container_hostnamespace`.
Optional `size` defaults to 200, capped by the deployment document budget. Do not supply `group_by`.

```json
{"region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z","host":["node-a"],"tracer_name":["cpusys"],"size":20}
```

Targets within a field are ORed; different fields are ANDed. `size` is a total
across regions, not per tracer; the effective budget must cover the region count.
Returns events, `source`, and completeness information; bounded samples are not totals.

### region: group events by target or event type

Requires a single-region array, `time_range`, and a distinct `group_by` array chosen
from `hostname`, `container_hostname`, `container_host_namespace`, and `tracer_name`.
Target/tracer filters are optional. The window is at most 7 days, also subject to
deployment limits. `size` defaults to 500, capped at 1000 independently of document budgets.

```json
{"scope":"region","region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z","group_by":["hostname","tracer_name"],"size":100}
```

Returns bucket counts and first/last timestamps, not raw events. Buckets are
key-ordered, not count-ranked Top-N. Check `truncated` before treating a distribution as complete.
For follow-up target queries, remove `group_by`, use actual returned targets and
tracers, and retain the returned `start_time/end_time` window.

## 4. query_event_profiles: analyze profiling data

Requires `region`, `time_range`, one **string** `tracer_name`, and the same target
arrays used by event queries.

| Optional argument | Usage |
|-------------------|-------|
| `record_id` | Event `source.record_id` to analyze one record; requires exactly one region and still needs target/time filters |
| `analysis_type` | `self_time` (default): exclusive weight; `total_time`: weight including child calls |
| `top_n` | Hotspots; default 50, maximum 1000 |
| `max_call_paths` | Paths per hotspot; default 5, maximum 20 |
| `max_stack_depth` | Returned path depth; default 12, maximum 32 |

```json
{"region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z","host":["node-a"],"tracer_name":"cpusys","top_n":10}
```

Analyzes frame/pprof data already stored in events; it does not trigger collection.
Without `record_id`, matching records are aggregated. Inspect `top_functions`,
`sources`, and `summary`; interpret weights by sample type/unit, not always as CPU time.

## 5. discover_metrics: find and verify metrics

Requires `region` and `time_range`. Omit `metric_names` for prefix-filtered candidate
names; supply up to 5 exact names to verify regional series presence and retrieve
available label keys and metadata.

```json
{"region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z","metric_names":["huatuo_cpu_usage"]}
```

`availability` is `present`, `absent`, or `unknown`. Candidates and verification
failures may both have `availability=unknown`, which does not mean absence.
Read label values from metric query results; do not guess missing units.

## 6. query_metrics: metric trends or statistics

| Argument | target (default): target trends | region: regional statistics |
|----------|---------------------------------|-----------------------------|
| `region` | Array of exact names | Array with exactly one region |
| Time | Required `time_range`; omit `time` | Required RFC3339 `time`; omit `time_range` |
| Target | Exactly one of `host` or `container_host`, as a **string** | Omit target arguments |
| `query` | Every selector contains the same exact target matcher | Every selector names a metric explicitly and occurs under aggregation |
| `step` | Optional; default `1m`, minimum `15s` | Omit |

Target trends:

```json
{"region":["us"],"time_range":"2026-10-27T07:00:00Z/2026-10-27T08:00:00Z","host":"node-a","query":"huatuo_cpu_usage{host=\"node-a\"}","step":"1m"}
```

Regional statistics:

```json
{"scope":"region","region":["us"],"time":"2026-10-27T08:00:00Z","query":"topk(20, avg by (host) (avg_over_time(huatuo_cpu_usage[5m])))"}
```

Regional mode is an instant query at `time`; `[5m]` looks back 5 minutes, not a
range query. Follow up with target arguments and the same lookback window as fixed `time_range`.
Both modes inject region matchers server-side; do not write them yourself.
`MetricPrefix` applies; subqueries, `offset`, and `@` are unsupported.
Target output may be truncated; regional output overflow fails. Small outputs do not guarantee cheap scans.

## Check results

- Check MCP `isError` and JSON `status` first. Failure is not empty data;
  correct `invalid_argument` using the schema and error detail. Do not retry unchanged requests with `retryable=false`.
- For `status=partial`, inspect errors, warnings, truncation, and `query_status`, and disclose coverage gaps.
- Statistics, profiling aggregates, or missing data alone do not establish target health or faults.
