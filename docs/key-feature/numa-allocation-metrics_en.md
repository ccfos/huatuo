---
title: NUMA Allocation Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

`memory_numa` reads `/sys/devices/system/node/node*/numastat` to distinguish
preferred-node allocation failures from allocations made by CPUs on another
node. Host-wide VM counters cannot identify which NUMA node is affected.

All metrics have the prefix `huatuo_bamai_memory_numa_`, labels `host`,
`region`, `node`, and type counter. Units are pages allocated, not bytes or
currently resident pages.

| Suffix | Allocation outcome |
| --- | --- |
| `numa_hit_pages_total` | Allocated here as preferred |
| `numa_miss_pages_total` | Allocated here despite preferring another node |
| `numa_foreign_pages_total` | Preferred here but allocated elsewhere |
| `interleave_hit_pages_total` | Interleave allocation succeeded here |
| `local_node_pages_total` | CPU on this node allocated here |
| `other_node_pages_total` | CPU on another node allocated here |

```promql
# Fraction of allocations on each memory node made by remote CPUs
rate(huatuo_bamai_memory_numa_other_node_pages_total[5m])
/
(rate(huatuo_bamai_memory_numa_local_node_pages_total[5m])
 + rate(huatuo_bamai_memory_numa_other_node_pages_total[5m]))
```

The collector is enabled unless `memory_numa` is in `BlackList`. Nodes are
rediscovered each scrape; unsupported hosts produce no series. A bad node
reports a scrape error while healthy nodes retain their metrics. Six series
and one small file read are added per node.

Memory policy can differ from CPU locality. These counters describe allocation,
not the latency or count of remote memory accesses. Huge pages have separate
accounting. See the [kernel NUMA statistics documentation](https://docs.kernel.org/admin-guide/numastat.html).
