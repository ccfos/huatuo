---
title: Continuous Profiling Display Evaluation
type: docs
description: Display choices and integration boundaries
author: HUATUO Team
date: 2026-07-27
weight: 6
---

## Scope

This document evaluates the display choices requested by
[Issue #328](https://github.com/ccfos/huatuo/issues/328). It separates the
display layer from profile collection and storage so that a deployment can
change its user interface without replacing the existing collection pipeline.

The evaluation covers Grafana, Pyroscope, Parca, and FlameGraph RS. It does not
claim a performance ranking because the four options have not been benchmarked
on the same HUATUO data set and hardware.

## Option comparison

| Option | Filtering and aggregation | Views | Integration cost | Recommended role |
| --- | --- | --- | --- | --- |
| Grafana | Uses the existing huatuo-apiserver Pyroscope-compatible API and Elasticsearch selectors | Time series, Top N, flame graph, and comparison panels | Low for an existing HUATUO deployment | Default integrated display |
| Pyroscope OSS | Native profile labels and group-by queries | Flame graph, Top table, comparison, and diff | Requires a profile-specific storage service and ingestion path | Optional standalone profiling stack |
| Parca | Native profile labels and pprof ingestion | Flame graph and label or time comparison | Requires a new database, query service, and ingestion path | Alternative for profile-centric deployments |
| FlameGraph RS | Filtering and aggregation happen before rendering | Standalone interactive SVG | Requires an export adapter or another local conversion step | Portable single-profile viewer |

Grafana is the preferred integrated option because it reuses the current
Elasticsearch-backed pipeline. A standalone pprof or interactive SVG export is
the preferred second option because it is portable and does not require another
server. Pyroscope remains a good choice when a deployment wants a dedicated
profile store. Parca is not selected by default because it duplicates storage
and query infrastructure without improving compatibility with the current
pipeline.

## Integration model

The display layer should preserve the current data flow:

```text
profiler -> pprof documents -> Elasticsearch
                                 |
Grafana <- query API <- huatuo-apiserver
                                 |
                                 +-> pprof or SVG export
```

Both display paths should use the same profile type, exact label selector, time
range, pagination, and merge logic. This prevents Grafana and exported profiles
from returning different data for the same request.

The public cgroup selector should remain `container_id`; a host-specific cgroup
path is not a stable display dimension. Process-group filtering should use the
common thread-group label rather than introducing a second representation.

## Required query contract

The integrated display needs these operations:

- enumerate profile types, label names, and label values;
- merge stack traces for a selected time range;
- return bounded time series grouped by an exact label;
- compare two selected time ranges; and
- export the merged result as standard pprof or interactive SVG.

Selectors for CPU profiles, process or thread group, PID, container or cgroup,
and host should use equality matching. Exact indexed filters can be pushed down
to Elasticsearch and avoid decoding a broad candidate set merely to apply a
regular expression.

## View mapping

| Requirement | Grafana | Portable display |
| --- | --- | --- |
| Flame graph | Merged interactive flame graph | Interactive SVG or a pprof viewer |
| Top N | Grouped series and flame graph Top table | `go tool pprof -top` |
| Comparison | Current range against a previous equal range | Export both ranges for an external diff |
| Time series | Sum or average profile values in time buckets | A selected-range snapshot |

A document-count panel only reports ingestion availability. A profile-value
timeline reports accumulated sample values and must not be presented as system
CPU utilization.

## Performance boundaries

Every online query should have explicit limits and return an actionable error
instead of silently truncating results. The implementation should bound:

- selected profile documents and page size;
- returned series and label values;
- merged flame graph nodes and distinct stacks; and
- generated JSON, pprof, and SVG response sizes.

Users should be instructed to narrow the time range or selector after a limit
error. Release validation should record query latency, response size, and
huatuo-apiserver memory against a fixed large profile fixture.

## Acceptance checks

The implementation of this design is complete when:

- Grafana and at least one portable display use the same query semantics;
- CPU, process or thread group, PID, container or cgroup, and host filters are
  covered by query tests;
- flame graph, Top N, time series, and adjacent-window comparison contracts are
  covered by dashboard tests; and
- large selections fail with documented limits rather than partial results.
