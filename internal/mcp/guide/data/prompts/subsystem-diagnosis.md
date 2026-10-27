# In-depth subsystem diagnosis

## Goal
Investigate anomalies in a kernel subsystem and produce a root-cause analysis.

## Required guidance
- `huatuo://storage-model`: storage model, principles, and metric label rules.
- `huatuo://subsystems/{subsystem}/tracers`: tracer metadata (`key_fields`,
  thresholds, `match_field`, `metric_keywords`, and `related_tracers`).
- `huatuo://profiling/methodology`: required for tracers with `has_profiler=true`.

## Prerequisites
- Subsystem: cpu | memory | io | network | hardware.
- Target: hostname (host) or container_hostname (container).
- Exact region names supplied by the user.
- A user-specified time range.

## Workflow
Check MCP `isError`, JSON `status`, `errors`, and `warnings` after every call.
Follow "Tool results and error handling" in `huatuo://playbook/guide`.
Correct `invalid_argument` using `message`; do not repeat unchanged requests with
`retryable=false`. Query failure is not an empty result or evidence of a cluster
outage. Disclose partial coverage, unverified evidence, and specific failure causes.

### Step 1: Load subsystem guidance
Resolve relative time ranges to absolute bounds once and reuse them across calls.
Investigate independent host/container targets separately: filters across fields are ANDed.

Read `huatuo://subsystems/{subsystem}/tracers` for applicable tracers, their
`target_type`, `match_field`, `has_profiler`, key fields, thresholds, and related tracers.

### Step 2: Discover tracers and metrics
Names may change between releases. Confirm what is currently available:
- **First call `discover_event_types(region=[...], time_range=user-specified range)`**
  for actual `tracer_name` values and `has_profiler` flags.
- Call `discover_metrics(region=[...], time_range=user-specified range)` for
  prefix-filtered candidate names (`availability=unknown`). Supply up to five
  exact `metric_names` to verify regional availability and label keys.
  A failed verification with `unknown` does not mean absent.
  Read actual label values from `query_metrics` results.

Compare documented tracers with discovered names. Use exact matches directly;
verify alternatives rather than assuming similar names have equivalent semantics.
Discovery is cached and incomplete; missing entries do not prove absence.

Use naming conventions and the Metric label rules in `storage_model.md`:
- `container_` generally identifies container metrics: use the exact user-supplied `container_host`.
- `host` or explicit host documentation identifies host metrics: use the exact user-supplied `host`.
- If uncertain, perform a bounded trial query using those label rules.

### Step 3: Fetch ES anomaly events
Call `query_events(scope=target)` with a bounded tracer batch:
- `tracer_name`: applicable actual names, at most 100 per call.
- `region`: the user's exact region array.
- `host`: the user's host target array.
- `container_host`: the user's container target array.
- `time_range`: the user-specified range.
- `size`: a total sample budget across regions, at least the region count and
  within the configured limit. It is not a per-tracer limit.

Check truncation and missing tracer coverage. Returned samples do not establish
event totals; narrow or split follow-up queries when coverage is insufficient.

If no events match, verify names and regions against the current discovery catalog.

### Step 4: Analyze profiling data
For each tracer with `has_profiler=true`, call `query_event_profiles`:
- `tracer_name`: one tracer from Step 3 per call.
- `region`: the same region array.
- `host` / `container_host`: the same target arrays, as applicable.
- `time_range`: the same range.
- `top_n`: 10.
- `analysis_type`: `self_time`.

Read `huatuo://profiling/methodology`, then
`huatuo://profiling/kernel-signatures` to interpret hotspot kernel functions.

### Step 5: Check Prometheus metrics
Query Prometheus even if ES has no events: trends may be visible below event thresholds.
Build PromQL using discovered names and `storage_model.md`:
- Select relevant discovered metrics using the tracer's `metric_keywords`.
- Include the same exact `host` or `container_host` matcher in every vector selector.
- Do not include a region matcher; the server injects one per requested region.
- Call `query_metrics` for each query, with the same region array, `time_range`,
  and one scalar target.

Compare results with `abnormal_threshold`.

### Step 6: Check related tracers
Map `related_tracers` to verified actual names. A documented exact tracer may be
queried even if absent from cached discovery. Do not invent names or label
unmapped tracers unavailable without verification.

### Step 7: Match candidate root causes
Read `huatuo://subsystems/{subsystem}/patterns` and compare profiler hotspots
with the patterns. For cross-subsystem correlations, read `huatuo://cross-domain`.

### Step 8: Report
Record event `source`, profiler `sources`, and full metric names used for validation.
The caller's Skill generates deployment-specific links. Never guess Grafana,
Prometheus, or Elasticsearch addresses.

## Diagnosis report: {target} — {subsystem} anomaly

**Current state**: {key metric values and anomaly assessments}.

**Root-cause assessment**: {matched pattern, or "unrecognized pattern"}.

**Evidence**:
- ES events: {returned samples per tracer, event times, key fields, and completeness limits}.
  **For events with call stacks, copy the entire original stack verbatim**, including
  function names, offsets, and filenames in every frame. Do not summarize or truncate it.
- Profiling: {top hotspots and percentages, with kernel-function explanations}.
- Prometheus: {metric values and assessments; PromQL expressions and timestamps}.
- Related tracers: {whether matching events were found}.

**Recommendations**: {typical causes and actions supported by the matched pattern}.
