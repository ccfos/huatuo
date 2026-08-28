---
title: 持续 Profiling 展示方案评估
type: docs
description: 展示方案与集成边界
author: HUATUO Team
date: 2026-07-27
weight: 6
---

## 范围

本文评估 [Issue #328](https://github.com/ccfos/huatuo/issues/328) 要求的展示
方案。展示层与 Profile 采集、存储解耦，使部署方可以在不替换现有采集管线的
情况下更换界面。

评估范围包括 Grafana、Pyroscope、Parca 和 FlameGraph RS。由于尚未在相同的
HUATUO 数据集和硬件上完成四种方案的统一基准测试，本文不提供绝对性能排名。

## 方案对比

| 方案 | 筛选与聚合 | 视图 | 集成成本 | 建议角色 |
| --- | --- | --- | --- | --- |
| Grafana | 复用 huatuo-apiserver 的 Pyroscope-compatible API 和 Elasticsearch selector | 时间序列、Top N、火焰图、对比 | 对现有 HUATUO 部署较低 | 默认集成展示 |
| Pyroscope OSS | 原生 Profile label 和 group-by 查询 | 火焰图、Top table、对比、diff | 需要 Profile 专用存储与写入路径 | 可选的独立 Profiling 栈 |
| Parca | 原生 Profile label 和 pprof 写入 | 火焰图、按 label 或时间对比 | 需要新增数据库、查询服务和写入路径 | Profile 中心化部署的备选 |
| FlameGraph RS | 渲染前完成筛选与聚合 | 独立交互式 SVG | 需要导出适配或额外本地转换 | 便携的单 Profile 查看器 |

Grafana 是首选集成方案，因为它可以复用当前基于 Elasticsearch 的管线。标准
pprof 或独立交互式 SVG 是首选第二方案，因为它便于携带且不要求增加服务。
需要独立 Profile 存储时可以选择 Pyroscope。Parca 会重复现有的存储和查询
基础设施，且不会提升与当前管线的兼容性，因此不作为默认方案。

## 集成模型

展示层应保持当前数据流：

```text
profiler -> pprof 文档 -> Elasticsearch
                              |
Grafana <- 查询 API <- huatuo-apiserver
                              |
                              +-> pprof 或 SVG 导出
```

两个展示入口应共用 Profile 类型、精确 label selector、时间范围、分页和合并
逻辑，避免 Grafana 与导出文件对同一请求返回不同数据。

公开的 cgroup selector 应保持为 `container_id`，而不是暴露与宿主机实现绑定的
cgroup path。进程组筛选应复用统一的 thread-group label，不增加第二套表达。

## 查询契约

集成展示需要以下操作：

- 枚举 Profile 类型、label 名称和值；
- 合并所选时间范围的调用栈；
- 按精确 label 分组并返回有边界的时间序列；
- 对比两个所选时间范围；
- 将合并结果导出为标准 pprof 或交互式 SVG。

CPU Profile、进程或线程组、PID、容器或 cgroup、主机等 selector 应使用等值
匹配。精确索引筛选可以下推到 Elasticsearch，避免为了执行正则表达式而解码
大量候选数据。

## 视图映射

| 需求 | Grafana | 便携展示 |
| --- | --- | --- |
| 火焰图 | 合并后的交互式火焰图 | 交互式 SVG 或 pprof 查看器 |
| Top N | 分组时序和火焰图 Top table | `go tool pprof -top` |
| 对比 | 当前范围与等长的上一范围 | 分别导出两个范围后使用外部工具 diff |
| 时间序列 | 按时间桶求和或平均的 Profile 值 | 所选范围的快照 |

文档计数面板只表示写入可用性。Profile 值时间线表示累计 sample 值，不能标为
系统 CPU 使用率。

## 性能边界

所有在线查询都应设置明确限制，超过限制时返回可操作的错误，不能静默截断。
实现至少需要限制：

- 选中的 Profile 文档数量和分页大小；
- 返回的 series 和 label 值数量；
- 合并火焰图节点数和不同调用栈数量；
- 生成的 JSON、pprof 和 SVG 响应大小。

触发限制后，应提示用户缩小时间范围或 selector。发布验收应使用固定的大型
Profile fixture，记录查询延迟、响应大小和 huatuo-apiserver 内存。

## 验收检查

完成该设计需要满足：

- Grafana 与至少一种便携展示使用相同的查询语义；
- 查询测试覆盖 CPU、进程或线程组、PID、容器或 cgroup、主机筛选；
- dashboard 测试覆盖火焰图、Top N、时间序列和相邻时间窗对比；
- 大范围选择命中文档化的限制并失败，而不是返回部分结果。
