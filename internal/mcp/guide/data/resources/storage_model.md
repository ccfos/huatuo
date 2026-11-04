# Storage model

## Elasticsearch — anomaly events
- Anomaly context: each tracer records an event document when it detects an
  anomaly, including time, host/container, key fields, and important call stacks.
- Profiling analysis: `query_event_profiles` analyzes stored frame arrays or
  `{profile_type, profile}` pprof objects from event `tracer_data.flamedata`.
  The reader normalizes both representations; MCP does not trigger profiling collection.
  `has_profiler` indicates payload markers independently of `tracer_type`, not
  guaranteed valid profiles. Discovery is cached for 5 minutes and is not a live inventory.
- Tools: `query_events` (events), `query_event_profiles` (profiling data analysis),
  and `discover_event_types` (available event types identified by tracer names, and `has_profiler`).
- `query_events(scope=region)` groups across targets for LLM-directed discovery; it does not
  label faults. Require exactly one region, a time range of at most 7 days, and nonempty distinct `group_by`
  fields from hostname/container_hostname/container_host_namespace/tracer_name.
  Targets and tracers are optional. `size` defaults to 500, capped at 1000
  independently of the document limit; dimensions are bounded only by the whitelist
  and no-duplicates rule. Counts and first/last diagnostic timestamps
  are returned in key order, not top-count order. Truncated lists are incomplete;
  counts are event counts, not distinct hosts. Missing keys are null and multivalued
  fields may place one event in several buckets. Indexed, uniform keyword grouping
  is required; source scans and mixed-projection grouping are rejected. Narrow the
  time window and use returned targets in query_events(scope=target)/query_event_profiles.
  Remove group_by and preserve the resolved absolute query window when switching;
  a known target can skip statistics entirely.
- Each event returns its native `source`; `query_event_profiles.sources` identifies
  analyzed records and regions. Callers may pass these locators to their Skill
  or prompt to generate viewing links. MCP does not construct frontend URLs.
- `query_event_profiles(record_id=...)` analyzes the profile for one exact event.
  Without it, matching records in the time range are aggregated; aggregate
  hotspots must not be presented as hotspots from a single event.

## Prometheus — metric time series
- Each region has its own connection and `MetricPrefix`. A nonempty prefix
  restricts queries to matching metric names. An empty prefix imposes no name
  restriction; region filtering still applies, as do target filters in scope=target. There is no default
  connection or inherited regional configuration. Unmatched backend routing
  uses the first configured region; data queries still require exact region names.
- General system metrics cover CPU, networking, memory, and I/O.
- HuaTuo-specific metrics include qdisc, memory reclamation, and hardware metrics.
- Tools: `query_metrics` (target range evidence or regional instant statistics), `discover_metrics` (prefix-filtered
  candidates, optionally verifying exact `metric_names`, regional presence, and label keys).
- query_metrics defaults to scope=target for known-target range evidence. Explicit
  scope=region is a single-region instant statistics/candidate-discovery mode,
  like query_events(scope=region), not targeted fault diagnosis. Supply RFC3339 time and an
  aggregation query, without target parameters, time_range, or step. Every selector
  names a metric and is under aggregation; output overflow is rejected. Both
  statistics tools return purpose=statistics and usage_hint. Select targets from
  statistics before gathering target-level evidence; the caller interprets faults.
- Default discovery returns full metric names, such as `huatuo_*`, with
  `availability=unknown`: candidates do not prove regional series exist.
  Targeted discovery of up to five exact names returns `present`, `absent`, or
  `unknown` on verification failure. Only `present` results provide usable label
  keys. Read label values from query results. Callers generate viewing links.
- Targeted discovery returns HELP/TYPE/UNIT only when the backend provides them.
  `metadata_status=unavailable` means metadata was not supplied; never guess units
  from metric names. Incomplete or conflicting entries use `ambiguous_or_incomplete`.
- Prometheus stores time series, not tracer events or profiling payloads;
  use the ES event tools for those.

## Core principles
1. **ES first**: prioritize event queries for rich anomaly context.
2. **Prefix boundary**: never query outside a nonempty configured `MetricPrefix`.
   The server rejects such requests. Operators must change configuration to expand access.
3. **Exact regions**: use the user's exact region array without guessing,
   transforming, or inventing variants. Ask for regions if omitted. Do not add
   region matchers to PromQL; `query_metrics` injects them for each requested region.
4. **Causal consistency**: use the same time range for events, metrics, and
   profiles within one investigation. The agent maintains this consistency;
   the server does not enforce it across calls.
5. **Evidence**: report `source`, tracer, event time, key fields, full metric names,
   PromQL expressions, and timestamps. For stack-bearing events, include both
   interpretation and complete original call stacks for verification.

## Metric label rules

These are HuaTuo label conventions, not proof that a backend metric has these
labels. Verify actual label keys through discovery and values through queries.

| Metric scope | Labels | Target matcher |
|--------------|--------|----------------|
| Host | host, region | `host="{hostname}"` |
| Container | container_host, region, container_name, container_type, container_hostnamespace | `container_host="{container_hostname}"` |

Names containing `container_` usually identify container metrics.
Event and profiler queries support `container_hostnamespace` target arrays.
`query_metrics(scope=target)` requires one scalar `host` or `container_host`, not a namespace target.
