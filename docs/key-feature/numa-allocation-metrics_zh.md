---
title: NUMA Allocation Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

`memory_numa` 读取 `/sys/devices/system/node/node*/numastat`，按 NUMA 节点暴露
内存分配结果，用于定位首选节点分配失败和远端 CPU 分配内存的情况。

指标前缀为 `huatuo_bamai_memory_numa_`，标签为 `host`、`region`、`node`，
类型均为 counter，单位为累计分配页数，不是字节数或当前驻留页数。

| 后缀 | 含义 |
| --- | --- |
| `numa_hit_pages_total` | 按首选策略在本节点分配成功 |
| `numa_miss_pages_total` | 首选其他节点，实际在本节点分配 |
| `numa_foreign_pages_total` | 首选本节点，实际在其他节点分配 |
| `interleave_hit_pages_total` | 按交错策略在本节点分配成功 |
| `local_node_pages_total` | 本节点 CPU 在本节点分配 |
| `other_node_pages_total` | 其他节点 CPU 在本节点分配 |

```promql
# 每个内存节点由远端 CPU 发起的分配比例
rate(huatuo_bamai_memory_numa_other_node_pages_total[5m])
/
(rate(huatuo_bamai_memory_numa_local_node_pages_total[5m])
 + rate(huatuo_bamai_memory_numa_other_node_pages_total[5m]))
```

默认启用，可在 `BlackList` 中添加 `memory_numa` 关闭。每次采集重新发现节点，
兼容热插拔；不支持的主机不产生指标。某个节点读取失败时报告采集错误，保留其他节点的数据。
每个节点增加六条时间序列和一次小文件读取。

内存策略的首选节点不一定是 CPU 所在节点。这些计数表示分配行为，不表示远端内存访问次数或延迟；
大页有独立统计。参见[内核 NUMA 统计说明](https://docs.kernel.org/admin-guide/numastat.html)。
