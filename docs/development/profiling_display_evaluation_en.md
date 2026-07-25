---
title: Continuous Profiling Display Evaluation
type: docs
description: Display choices, delivered capabilities, and scaling boundaries
author: HUATUO Team
date: 2026-07-25
weight: 6
---

## Decision

This document evaluates the display choices requested by
[Issue #328](https://github.com/ccfos/huatuo/issues/328).

HUATUO delivers two independently usable display paths:

1. **Grafana with the huatuo-apiserver Pyroscope-compatible API** is the
   integrated online path. It reuses the existing Elasticsearch profile
   documents and provides time series, Top N, flame graph, and adjacent-window
   comparison dashboards.
2. **Standard pprof and standalone interactive SVG export** is the portable
   path. It uses the same selectors, time range, pagination, and merge logic as
   Grafana, but does not require Grafana.

The Grafana provisioning also keeps a separate direct-Pyroscope datasource for
deployments that enable the standalone Pyroscope backend from #327. That
optional backend is not required by the two paths above.

## Option comparison

| Option | Filtering and aggregation | Views | Scaling model | Operational cost | Result |
| --- | --- | --- | --- | --- | --- |
| Grafana and huatuo-apiserver | Exact indexed dimensions, grouped series, and bounded merge/diff queries | Time series, Top 10, flame graph Top table, and comparison | Elasticsearch selection followed by bounded apiserver aggregation | Reuses the existing stack | Delivered |
| Pyroscope OSS | Native series labels, tag exploration, and group-by | Native flame graph, Top table, comparison, and diff | Profile-native storage with independently scalable components | Adds another store and lifecycle | Optional through #327 |
| Parca | Profile-native labels and pprof ingestion | Flame graph and label/time comparison | Dedicated profile database and query engine | Requires a new service and ingest path | Evaluated, not deployed |
| FlameGraph RS | Filtering must occur before rendering | Standalone interactive SVG | Local, on-demand rendering | Low, but not a continuous-profile UI | Experience evaluated; no Rust dependency added |

There is no reproducible benchmark of all four options on the same HUATUO
dataset and hardware. The table records architecture and verified integration
boundaries, not an absolute performance ranking.

## Implemented data paths

```text
profiler -> pprof + managed labels -> Elasticsearch
                                        |
Grafana <- Pyroscope-compatible API <- huatuo-apiserver
                                        |
                                        +-> pprof download
                                        +-> interactive SVG
```

Grafana and the exports use the same profile type, exact label selector, and
time window. The pprof response remains a gzip-compressed standard protobuf
accepted by `go tool pprof`. The SVG supports search and zoom in a browser.

When #327 enables direct Pyroscope ingestion, Grafana can also use the
separately provisioned `huatuo-bamai-pyroscope` datasource. The authenticated
`huatuo-apiserver-pyroscope` datasource remains available at the same time.

## Multidimensional selection

The collector records managed labels in profile metadata and every pprof
sample:

| User dimension | Selector |
| --- | --- |
| Collection scope | `profiling_scope` |
| Logical CPU set | `cpu` |
| Exact process or thread | `pid` |
| Thread group / process group | `tgid` |
| Container / cgroup target | `container_id` |
| Host | `hostname` |

`container_id` is the stable public cgroup selector. HUATUO intentionally does
not expose host-specific cgroup paths. `tgid` is the common thread-group
selector and replaces a separate process-group option.

The host and container dashboards expose optional exact
`profiling_scope`/`cpu`/`pid`/`tgid` variables. Their Top 10 panel can group by
any managed dimension or tracer. The comparison dashboard forwards the same
dimensions to both adjacent time windows. An empty dashboard variable omits
that filter.

The query API accepts equality matchers only. This lets Elasticsearch push down
every managed dimension and avoids decoding a broad candidate set merely to
apply a regular expression.

## View coverage

| Capability | Grafana | pprof / SVG |
| --- | --- | --- |
| Flame graph | Merged interactive flame graph with Top table | Interactive SVG or any pprof-compatible viewer |
| Top N | Top 10 grouped series and the flame graph Top table | `go tool pprof -top` |
| Comparison | Current window versus the immediately preceding equal window | Download two pprof windows for an external diff |
| Time series | Sum or average profile values in time buckets | A selected-range snapshot |

The document-count panel reports ingestion availability. The profile-value
timeline reports accumulated profile values; it is not a system CPU-utilization
metric.

## Scaling boundaries

The online and export paths fail closed instead of silently truncating data:

- A selection may contain at most 10,000 profile documents.
- Documents are read in stable pages of 1,000.
- `SelectSeries` returns at most 100 series; dashboards request Top 10.
- Flame graphs default to 5,000 nodes and reject requests above 10,000 nodes.
- The comparison JSON adapter and standalone SVG each reject responses above
  8 MiB.
- Standalone SVG rendering rejects more than 10,000 distinct stacks.
- Each high-cardinality dashboard dimension lists at most 500 exact values.

Callers must narrow the time range or selector after a limit error. This keeps
the UI responsive but does not replace production load testing. Release
validation should record latency, response size, and apiserver memory against a
fixed large profile fixture.

## Acceptance verification

The repository tests use labeled pprof fixtures to verify that:

- a `cpu` and `profiling_scope` selector filters stored profiles;
- `SelectSeries` groups the result by `pid`;
- the same managed label selects a merged flame graph;
- adjacent-window comparison forwards every managed dimension;
- pprof and SVG exports use the bounded common loader; and
- dashboard contracts contain the dimensions, Top 10, timeline, flame graph,
  and comparison requests.

Real Grafana rendering still depends on the Grafana, Elasticsearch, and
huatuo-apiserver services being available in the deployment environment.
