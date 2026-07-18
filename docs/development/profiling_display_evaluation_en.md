---
title: Continuous Profiling Display Evaluation
type: docs
description: Online and standalone Continuous Profiling displays, capability boundaries, and extension choices
author: HUATUO Team
date: 2026-07-16
weight: 6
---

## Scope and decision

This document evaluates Grafana, Pyroscope, Parca, and FlameGraph RS as requested
by [Issue #328](https://github.com/ccfos/huatuo/issues/328), and records the
boundaries of this implementation.

The implementation provides two independently testable display entry points:

1. **Option A: Grafana + huatuo-apiserver.** The Grafana Pyroscope data source
   calls the Pyroscope-compatible API exposed by the apiserver. The apiserver
   queries and merges the existing pprof data in Elasticsearch. This is the
   integrated, default online display.
2. **Option B: standalone pprof / interactive SVG.** With the same label selector
   and time range as Option A, a user can download a standard pprof profile or
   open a standalone interactive SVG flame graph. This path does not require
   Grafana or introduce another storage backend.

The native Pyroscope service and Parca are evaluated candidates only. They are
**not deployed or written to by this implementation and must not be presented as
display options delivered by this branch**.

## Comparison

| Option | Multidimensional filtering and aggregation | Views | Performance and scaling model | Operational cost | Decision |
| --- | --- | --- | --- | --- | --- |
| Grafana with a Pyroscope-compatible data source | Prometheus-style label selectors and grouped series are supported to the extent implemented by the backend Label, Series, Merge, and Diff APIs | Flame graph, Top table, and time series; the delivered comparison dashboard shows two time windows and the backend separately exposes Diff | Grafana queries and renders data; storage and aggregation costs remain in the apiserver and Elasticsearch. Time-range, series, and node limits bound responses | Reuses the existing Grafana, Elasticsearch, and apiserver deployment | **Option A, delivered** |
| Pyroscope OSS native UI | Tag Explorer and label selectors are designed for tag filtering; time series support group-by | Native flame graph, Top table, Sandwich, Comparison, and normalized Diff views | The v2 architecture independently scales read and write paths and uses object storage; single-node mode is intended for development and small evaluations | Requires another profile store, dual writes, persistence, upgrades, and an authenticating proxy | Evaluated, not delivered by this branch |
| Parca | A multidimensional label model and label-selector queries; standard pprof is an interchange format | Continuous Profiling flame graphs and comparisons across labels or time | A purpose-built profile store and query engine retain raw data for slicing and aggregation | Requires a Parca service, an ingestion adapter, and a separate lifecycle | Evaluated, not delivered by this branch |
| FlameGraph RS | It does not index labels for continuous data; filtering and aggregation must happen before SVG generation | Generates an interactive SVG for one-off or offline analysis; it does not provide a Continuous Profiling Top N view, time series, or tag browser | Local generation has few moving parts; inline symbol processing can significantly increase generation time. Its documentation also notes that proportional flame graphs are coarse-grained and difficult to diff directly | Lowest, but it is neither a Continuous Profiling backend nor a complete web UI | Its standalone SVG experience is a reference; no Rust dependency is added |

Official references:

- [Grafana flame graph panel](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/visualizations/flame-graph/)
- [Grafana Pyroscope query editor](https://grafana.com/docs/grafana/latest/datasources/pyroscope/query-profile-data/)
- [Pyroscope UI](https://grafana.com/docs/pyroscope/latest/view-and-analyze-profile-data/pyroscope-ui/)
- [Pyroscope server API](https://grafana.com/docs/pyroscope/latest/reference-server-api/)
- [Pyroscope v2 architecture](https://grafana.com/docs/pyroscope/latest/reference-pyroscope-v2-architecture/about-pyroscope-v2-architecture/)
- [Parca](https://github.com/parca-dev/parca)
- [FlameGraph RS](https://github.com/flamegraph-rs/flamegraph)
- [pprof tags and profile comparison](https://github.com/google/pprof/blob/main/doc/README.md)

There is no public, reproducible benchmark of all four projects using the same
dataset and hardware. This document therefore does not claim an absolute
performance ranking. The table only describes the published architectures and
boundaries that can be verified for this implementation.

## Implemented data paths

### Option A: online Grafana queries

```text
profiler -> standard pprof + labels -> Elasticsearch
                                      |
Grafana <- Pyroscope-compatible API <- huatuo-apiserver
```

The apiserver exposes Pyroscope-compatible ProfileTypes, LabelNames,
LabelValues, SelectSeries, SelectMergeStacktraces, and Diff APIs. Grafana uses
the same profile type, label selector, and time range for a timeline, a regular
flame graph, and a two-window comparison. Diff aggregates explicit baseline and
comparison selections on the server for compatible clients; it is not a proxy
to a Pyroscope service and does not require Pyroscope to be deployed.

The `Continuous Profiling Comparison` dashboard places two flame graph and Top
table panels side by side. The left panel uses the current range and the right
panel shifts the same range back by one hour. Both panels share profile type,
host/container, CPU, PID/TGID, cgroup, process-group, and other selectors. This
branch does not configure the `service_name` discovery required by Grafana
Profiles Drilldown, so its normalized red/green Diff UI is not claimed as a
verified feature; the backend Diff RPC can be verified at the protocol level.

### Option B: standalone pprof and SVG

```text
                         +-> pprof download -> go tool pprof / other pprof tools
Elasticsearch -> apiserver
                         +-> interactive SVG -> browser
```

The standalone entry points share query parsing, authorization, time-range, and
document-merge behavior with Option A. This avoids a situation where Grafana
and the download endpoint select different datasets. The pprof download
preserves the complete Profile structure, while SVG is a quick browser-based
view that does not require Grafana.

Option B does not provide a normalized red/green Pyroscope-style Diff SVG. Use
Option A for the built-in comparison view. Two downloaded pprof files can also
be processed by an external tool that supports pprof differences.

Both standalone endpoints take the same required query parameters:
`profile_type`, a Prometheus-format `selector`, and Unix millisecond `start`
and `end` timestamps:

```text
GET /v1/profiles/flamegraph/export/pprof?profile_type=...&selector=...&start=...&end=...
GET /v1/profiles/flamegraph/export/svg?profile_type=...&selector=...&start=...&end=...
```

The pprof response is a gzip-compressed standard protobuf accepted by
`go tool pprof`. The SVG response opens directly in a browser and supports
search and zoom. The selector and profile type must be URL encoded, and both
routes inherit the profiles API access controls.

## Multidimensional filtering and aggregation

The profile type selects CPU, memory, or lock data. Labels injected by #329
provide the following additional series/sample dimensions:

| Dimension | Label |
| --- | --- |
| Collection scope | `profiling_scope` |
| Logical CPU set | `cpu` |
| Thread / process | `pid`, `tgid` |
| cgroup | `cgroup_id`, `cgroup_path` |
| Process group | `process_group_id` |
| Container | `container_id` |
| Lock | `lock_type` |

Non-empty label conditions are combined with AND and matching profiles are
merged within the selected time range. Grafana variables in Option A and query
parameters in Option B are converted to the same label selector.

Current boundaries:

- The profile type selects CPU profiles. The `cpu` label is available only to
  C, C++, and Go CPU profiling through the native provider; Java and Python
  providers reject that option. It records the task's selected logical CPU set
  (for example, `0,1`), not the CPU on which each sample ran, so one profile
  cannot be split back into per-core samples through this label.
- Grafana variables read exact Elasticsearch keyword values and stay
  single-select to control cardinality. Selectors use escaped regular
  expressions and represent All with the standard `=~".*"`, without reserving
  API literals. The query API supports Prometheus `=`, `!=`, `=~`, and `!~`
  matchers. Non-exact matchers are applied after decoding at most 10,000
  candidate profiles; the request fails if that set exceeds the limit instead
  of returning an incomplete result.
- Custom pprof labels remain in the raw pprof and may be selected or grouped
  within that bounded candidate set. They do not automatically appear in
  Grafana variables or LabelValues and, unlike exposed collection dimensions,
  cannot be pushed down to Elasticsearch, so high-cardinality queries cost more.

## View capabilities and boundaries

| Capability | Option A: Grafana | Option B: pprof / SVG |
| --- | --- | --- |
| Flame graph | Interactive flame graph merged over a selected time range | Standalone interactive SVG; the pprof file opens in compatible tools |
| Top N | The Grafana flame graph panel Top table sorts by Self or Total; a grouped time-series panel limits results to the top 10 series for the selected dimension | Run `go tool pprof -top` on the downloaded pprof; the SVG itself is not a Top N table |
| Comparison / Diff | `Continuous Profiling Comparison` shows the current range beside the prior hour; the apiserver separately exposes a protocol-verifiable Diff RPC | No built-in normalized Diff SVG; baseline and comparison pprof files can be downloaded separately |
| Time series | Shows the time distribution of matching profiles and helps narrow the flame graph range | The SVG is a snapshot of one selected range and has no continuous timeline |

The `SelectSeries` timeline sums or averages profile sample values and may group
them by one exposed label. The existing profile-document-count panel only shows
sample availability. Neither is equivalent to system CPU utilization or a
replacement for a system CPU metric.

## Performance boundaries

The implementation leaves collection unchanged, but query behavior has explicit
bounds:

- Profiles are read from Elasticsearch and merged in the apiserver process.
  Memory and CPU costs grow with the number of profiles, samples, and distinct
  stacks in the selected range.
- Regular flame graph, standalone pprof/SVG, and Diff queries enforce a hard
  10,000-profile-document limit. If storage counts more matches, the request
  fails and asks the caller to narrow the time range or selector instead of
  silently returning only the newest 10,000 documents.
- Diff performs baseline and comparison aggregation and normally consumes more
  query time and memory than one flame graph. Production deployments should
  bound both time ranges.
- Label-value queries depend on Elasticsearch keyword indexes. High-cardinality
  PID, cgroup, or custom labels increase indexing and aggregation costs and
  should not be expanded into an unbounded variable list.
- A standalone SVG is an on-demand snapshot and adds no persistent cache or
  second storage copy. Repeated requests over large ranges still repeat merge
  and render work.

"No UI stalls at massive scale" cannot be proven by unit tests alone. Release
validation should fix a dataset size and record LabelValues, SelectSeries,
single-flame-graph, Diff, and SVG latency, response sizes, and peak apiserver
memory, then compare those measurements with the same-version baseline.

## Boundaries with #329 and #353

- [#329](https://github.com/ccfos/huatuo/issues/329) owns PID/TGID, cgroup, and
  process-group collection scopes and injection of those dimensions into
  profile labels. The multidimensional display in #328 depends on those labels,
  but lock/eBPF collection work must not be counted again as frontend work.
- [PR #353](https://github.com/ccfos/huatuo/pull/353) implements the #327
  AutoTracing standalone Pyroscope mode. It is not a dependency of #328, and
  this implementation does not merge that PR's Pyroscope store or Docker
  composition wholesale.
- A future Pyroscope or Parca online store should be a separate profile sink. It
  should send pprof protobuf data and promote every display dimension to series
  labels in parallel with the existing Elasticsearch write, without changing
  the eBPF or third-party profiler sampling logic.
