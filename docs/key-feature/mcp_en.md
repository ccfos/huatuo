---
title: MCP Diagnostics
type: docs
description: Anomaly context evidence and metrics queries for AI agents
author: HUATUO Team
date: 2026-09-19
weight: 7
---

# MCP diagnostics

`huatuo-apiserver` exposes data and knowledge through the Model Context Protocol
(MCP). Any agent that supports MCP can connect.
The current capabilities are read-only: agents can retrieve typed anomaly
context evidence, hotspots and call paths from existing profiles, and bounded
metrics query results without connecting directly to storage.

The backends query existing event, profile, and time-series data; MCP does not
invoke collectors or trigger new collection. Concurrency quotas, principal and
backend rate limits, time/target filters, result limits, timeouts, and bounded
retries control query load. Caching, count-aware batching, and bounded regional
fan-out reduce query overhead and limit impact on production workloads and data
services. Investigation workflows and domain knowledge are maintained as text
and can be updated as needed.

## Architecture

The endpoint is integrated with the existing API server rather than running a
second listener:

```text
MCP client -> bearer auth -> /v1/mcp -> protocol server
                                      -> escompatible -> existing Elasticsearch/OpenSearch data
                                      -> prometheus   -> existing Prometheus-compatible time series
```

The transport is stateless. This avoids sticky-session requirements and
unbounded memory growth from abandoned sessions. `POST`, `GET`, and `DELETE`
are registered for client compatibility. All currently exposed operations are
read-only.

## Capabilities

Resources are auto-discovered from the embedded knowledge base and the configured
overlay directory, covering storage models, playbooks, subsystems, cross-domain
correlations, and automatic profiler guidance.

Prompts:

- `auto-diagnosis`: multi-subsystem diagnosis workflow requiring exact regions
  and a time range.
- `subsystem-diagnosis`: focused CPU, memory, I/O, network, or hardware workflow
  requiring exact regions and a time range.

Tools:

- `diagnostic_guide`: returns the server-maintained workflow for the
  `overview`, `subsystem`, `correlation`, or `profiler` stage.
- `discover_tracers`: lists tracers within the requested regions and time range.
  `has_profiler` indicates frame or pprof payload markers in `tracer_data.flamedata`,
  not that every payload is valid. Use `query_profiles` results and warnings to
  determine whether the data can actually be analyzed.
- `query_events`: queries events by tracer, region, target, and time range
  with count-aware batching and a document quota. Each event includes a native
  `source` as a caller-side locator.
- `query_profiles`: aggregates normalized automatic profile frames for
  one tracer into bounded self/total hotspots and call paths, and returns the
  native `source` of each parsed source document. It does not read manual
  continuous-profiling documents.
  To separate successive incidents, set `record_id` to a `source.record_id`
  from `query_events` and provide exactly one region. The same target, tracer,
  and time bounds still apply; the result analyzes only that source record.
  Frame arrays and valid pprof objects, including IRQ profiles, are supported;
  invalid payloads produce errors or warnings. Pprof results report
  `summary.sample_type` and `sample_unit`; incompatible sample types or units
  are not merged. Truncated input is reported in `summary.truncated` and
  `truncated_regions`. Hotspot percentages use only the analyzed input;
  `coverage` is the top_n functions' share of self weight, not query completeness
  or host CPU utilization.
- `discover_metrics`: fetches names from each selected backend within the
  requested time range without a series matcher, then filters the configured
  prefix locally. These fast catalog candidates have `availability: unknown`:
  a name in the backend catalog does not prove a series exists in that region.
  Optionally pass `metric_names` (up to five exact names) to check each name
  against a time- and region-scoped `/series` request with `limit=1`.
  Each result is `present`, `absent`, or `unknown` if verification fails.
  Only `present` names receive scoped label keys and backend HELP/TYPE/UNIT
  metadata. The metadata endpoint is not scoped by region or time range.
  Missing metadata is marked `metadata_status=unavailable`; incomplete or
  conflicting entries are marked `ambiguous`. Units are not inferred.
  Backend verification failures report partial results, not absence.
  `query_metrics` still enforces the exact region and target.
- `query_metrics`: validates and executes bounded range PromQL for one or more
  exact regions.

`discover_metrics` reports top-level `truncated` and per-region `query_status`
(`returned`, `truncated`, optional `warnings`). Regional quota truncation and
backend warnings produce partial results. Metric queries also report backend
warnings as partial, even for empty or scalar results. Total-time profile paths
include functions with zero self weight and their inclusive samples; self-time
paths require self samples.

`diagnostic_guide` is always registered. Event and metrics tools are registered
from valid backend configuration, independently of backend reachability at startup.

MCP returns source locators but does not construct Grafana, Prometheus, or
Elasticsearch UI URLs. A caller-side Skill can combine the returned region,
event `source`, metric name, labels, and query range with deployment-specific
URL templates.

Event and profile `source` locators contain `backend`, `record_id`, `region`,
and optional backend-native `locator` fields.

## Text knowledge base

Resources and prompts use Markdown, YAML, and other text files for storage models,
investigation workflows, tracer metadata, fault patterns, and profile analysis.
Set `MCP.KnowledgeOverlayDir` to override or extend the embedded `prompts/` and
`resources/` directory tree without changing Go code or rebuilding the service.

Registered files are read on each request, so external content edits take effect
on the next read. Files not overridden retain their embedded versions. Restart
after adding Resource files to refresh the catalog; embedded knowledge changes
require rebuilding. `huatuo://data-manifest` reports file sizes and SHA256 hashes
for version verification.

## Safety limits

### Errors visible to clients

Tool errors, including parameter validation, are returned with MCP `isError=true`.
Their text is a JSON error envelope, also available as `structuredContent`, with
`status=error`, `tool`, `error_class`, `message`, `retryable`, and `suggestion`.
Invalid arguments are labeled `invalid_argument` and include the validation reason
so clients can correct the request. Backend failures include an `errors` array
with region, backend, operation, cause, retryability, and a specific suggestion.
Data tools distinguish successful queries (`status=success`) from partial results
(`status=partial`); partial results retain detailed errors and warnings.
A failed query does not mean no matching data or prove the entire backend is down.
ES search/count/aggregation HTTP 404 errors remain errors with backend details.
Batch failures retain tracer names and nested `causes`; Count-to-Search fallback
keeps events but reports the Count error and an unknown total. Profile errors
preserve regional errors and `query_status` warnings even when analysis fails.
Prometheus proxy/plain-text/HTML error bodies and decoder causes are forwarded
with size limits and explicit truncation markers.

`backend_diagnostics` retains failed attempts, scoped by backend/operation/region.
`recovered=true` means that operation eventually succeeded, so its historical
errors alone do not imply missing data. Failed operations retain their attempt
history alongside the final error. At most 32 operations and 16 failed attempts
per operation are included, keeping the final entry and reporting omitted counts;
each attempt error is limited to 512 bytes plus a truncation marker.

### Bounds and backend behavior

MCP is disabled by default and uses existing API bearer authentication. Backend
connections are separate from application storage configuration: application
storage addresses and credentials are not inherited automatically. Startup does
not probe backends or create writers.

**Query scope and quotas**

- All five data Tools require `time_range` and a non-empty array of exact
  `region` names; `diagnostic_guide` does not require query scope. Time ranges
  accept a Go duration such as `168h`, an RFC3339 start time, or `start/end`.
  The maximum span defaults to 30 days; `MCP.MaxQueryTimeRange=0` disables it.
- Hard limits are 128 concurrent requests, 5 regions per call, 500 event/profile
  documents, 3000 metric series, and 800 discovered metrics. Operators may lower
  these quotas. Timeouts, response sizes, request rates, and retries are also
  bounded. Partial regional failures retain successful results from other regions.
- Event and profile queries require at least one `host`, `container_host`, or
  `container_hostnamespace` array, with at most 100 values per array. Values
  within a field are ORed; different fields are ANDed. Event queries accept up
  to 100 tracers; profile queries accept one tracer.
- Event/profile queries and metric discovery report regional completeness in
  `query_status`, with fields varying by Tool. Missing totals are unknown, and
  separate counts/searches do not imply a shared snapshot. Discovery caches are
  not complete inventories; queries may specify tracers not previously discovered.

**Events and profiles**

- Reads use existing Elasticsearch/OpenSearch data without changing mappings,
  creating collection tasks, or using Scroll cursors. Configured alias filters
  and routing are preserved.
- When indexed exact filtering is unavailable, reads attempt a bounded `_source`
  scan. Each scan examines at most 500 candidate documents and explicitly reports
  incomplete data on overflow. Narrow the time and target scope before retrying.
- Time filtering includes both endpoints and matches any of `uploaded_timestamp`,
  `uploaded_time`, or `time` within the range. The returned `timestamp` prefers
  `uploaded_timestamp`, then `uploaded_time`, then `time`, falling back only for
  missing or null fields. It can therefore fall outside the filter range. Results
  are ordered newest first by this clock; `observed_timestamp` is metadata only.
- Timestamps accept date strings and integer `epoch_millis` as JSON numbers or
  numeric strings. Numeric timestamps are always milliseconds. Large integers
  in event payloads retain precision during decoding and responses.
- Records are not excluded by `tracer_type`. Profiles must pass payload parsing
  and sample type/unit checks; discovery markers alone do not prove analyzability.
  For truncated input or invalid payloads, check partial status,
  `summary.truncated`, `truncated_regions`, and warnings.

**Metric queries**

- `query_metrics` requires exactly one scalar `host` or `container_host`.
  Every PromQL vector selector must include the same exact target matcher.
  Clients must omit region matchers; the server injects them for each region.
- With a nonempty `MetricPrefix`, every selector's metric name must use that
  prefix. An empty prefix removes this restriction. Matrix selectors are limited
  to one hour; subqueries, `offset`, and `@` modifiers are unsupported. PromQL
  length, step, and result points are also constrained.
- Truncation or backend warnings mark results partial. Truncated Prometheus
  responses also include `mcp_truncated: true`. Do not treat partial results as
  complete data.

`/metrics` reports MCP requests, concurrency, Tool calls, errors, latency,
response sizes, backend attempts, retries, and rate limits. Labels use fixed,
low-cardinality values and exclude queries, targets, regions, tracers, and credentials.

See the API server configuration guide for setup and permissions.
