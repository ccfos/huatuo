# Cross-subsystem automatic diagnosis

## Goal
Investigate all relevant subsystems for a target when the user's report is vague.

## Required guidance
- `huatuo://storage-model`: storage model, core principles, and metric label rules.
- `huatuo://profiling/methodology`: profiling data analysis.

## Prerequisites
- At least one target: hostname (host) or container_hostname (container).
- Exact region names supplied by the user.
- A user-specified time range.

## Target classification
- Only hostname: host target.
- Only container_hostname: container target.
- Both: investigate targets separately. Event filters across host/container fields
  are ANDed; metric target mode accepts exactly one scalar target.

## Workflow

### Check every Tool response
Check MCP `isError`, JSON `status`, and `errors`/`warnings` before interpreting data.
Follow "Tool results and error handling" in `huatuo://playbook/guide`.
For `invalid_argument`, correct the parameters using `message`.
Never repeat an unchanged request with `retryable=false`.
A failed query is neither an empty result nor proof of an ES/Prometheus cluster outage.
For `status=partial`, disclose coverage gaps. Mark failed sources as
"query failed / unverified", retaining the cause and limits on the conclusions.

### Phase 1: Load subsystem guidance and discover available sources
Resolve relative time ranges to absolute bounds once and reuse them across calls.

Read tracer metadata for each subsystem:
- `huatuo://subsystems/cpu/tracers`
- `huatuo://subsystems/memory/tracers`
- `huatuo://subsystems/io/tracers`
- `huatuo://subsystems/network/tracers`
- `huatuo://subsystems/hardware/tracers`

Use the investigation principles to select tracers and Prometheus metrics applicable
to the target type. Names can change between releases, so discover them dynamically:
- **First call `discover_event_types(region=[...], time_range=user-specified range)`**
  to obtain actual `tracer_name` values and `has_profiler` flags for that range.
- Call `discover_metrics(region=[...], time_range=user-specified range)` for
  prefix-filtered candidate names (`availability=unknown`). To verify regional
  availability and label keys, supply up to five exact `metric_names`.
  `unknown` does not mean absent. Read label values from `query_metrics` results.

Use naming conventions and the Metric label rules in `storage_model.md`:
- Names containing `container_` or `hostnamespace` generally identify container metrics.
- Names containing `host`, or explicitly documented as host metrics, identify host metrics.
- If uncertain, construct a bounded trial PromQL query using those label rules.

Compare documented tracers with discovered names:
- Exact match: use it directly.
- No exact match: verify a documented alternative; do not assume similar names
  have equivalent semantics. Cached discovery does not prove a tracer is absent.
- Undocumented tracer: it may extend the investigation.

For host targets, select tracers whose `target_type` includes host.
For container targets, select tracers whose `target_type` includes container.

### Phase 2: Scan ES events across subsystems
Call `query_events(scope=target)` with applicable `tracer_name` arrays (at most
100 names per call), the same regions, target, and absolute time bounds.
Choose a total `size` at least as large as the region count, within the configured
budget. Inspect returned samples by subsystem and disclose truncation; a small
sample does not establish subsystem totals or cover every tracer. Narrow or split
follow-up queries when coverage is insufficient.
If all tracers return no events, verify names and regions against the catalog
returned by this investigation's `discover_event_types` call.

### Phase 3: Investigate subsystems with abnormal events
Run the complete subsystem-diagnosis workflow for each affected subsystem.
For a tracer with `has_profiler=true`:
1. Call `query_event_profiles` to analyze profiling data, with exactly one
   `tracer_name`; regions and targets remain arrays. If several important events
   occurred within the window, analyze each separately using its `source.record_id`
   as `record_id`, then compare with the aggregate. Do not attribute hotspots from
   different events to a single event.
2. Read `huatuo://profiling/methodology`.
3. Read `huatuo://profiling/kernel-signatures` to interpret hotspot functions.
4. Read `huatuo://subsystems/{subsystem}/patterns` for candidate root causes.

### Phase 4: Validate with Prometheus
Query Prometheus regardless of whether ES returned events. Respect the configured
`MetricPrefix`. Build PromQL using discovered names and the Metric label rules.
Prefer HELP/TYPE/UNIT returned for `present` metrics by targeted `discover_metrics`.
If `metadata_status` is `unavailable` or `ambiguous_or_incomplete`, state that units/types are
unknown; do not infer them from names.

Every PromQL vector selector must contain the same exact target matcher and no region matcher: the server
injects an exact matcher for each requested region. Pass the region array, the
same `time_range`, and one scalar target to `query_metrics`:
- Host: an exact `host` matcher and a `host` argument with the same user-supplied value.
- Container: an exact `container_host` matcher and an identical `container_host` argument.

Read thresholds from `huatuo://subsystems/{subsystem}/tracers` and flag violations.
If no samples are returned, verify metric names, target labels, regions, and time
range. A verified regional series does not guarantee samples for the user's target.

### Phase 5: Correlate across subsystems
Read `huatuo://cross-domain` when several subsystems are abnormal. Examples:
- Memory anomalies + high system CPU time: reclaim may increase kernel CPU usage.
- I/O anomalies + CPU anomalies: I/O may increase system CPU time or I/O wait.
- Packet drops + softirq anomalies: softirq processing may be overloaded.
Treat these as hypotheses to validate, not established causal relationships.

### Phase 6: Produce a consolidated report
Record event `source`, profiler `sources`, and full Prometheus metric names used
for validation. If the environment provides link templates, let the caller's
Skill generate links from these locators. Never guess Grafana, Prometheus, or
Elasticsearch addresses.

## Automatic diagnosis report: {target}

**Scan summary**:

| Subsystem | ES events | Prometheus | Status |
|-----------|-----------|------------|--------|
| CPU | {returned samples / successful empty query / query failed / partial results} | {value or failure reason} | {normal / abnormal / unverified} |
| Memory | ... | ... | ... |
| I/O | ... | ... | ... |
| Network | ... | ... | ... |

**Detailed analysis**: {use the subsystem-diagnosis report format for affected
subsystems; include stack interpretation and complete original call stacks}

**Correlations**: {supported cross-subsystem causal chains and remaining hypotheses}

**Sources**:
- ES events: {tracer, event time, and key fields for verification}.
- Prometheus: {PromQL expressions and corresponding timestamps}.

**Recommendations**: {actions ordered by priority}.
