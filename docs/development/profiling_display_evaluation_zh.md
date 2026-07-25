---
title: 持续 Profiling 展示方案评估
type: docs
description: 展示方案、已交付能力和扩展边界
author: HUATUO Team
date: 2026-07-25
weight: 6
---

## 结论

本文评估 [Issue #328](https://github.com/ccfos/huatuo/issues/328) 要求的展示
方案。

HUATUO 提供两个可以独立使用的展示入口：

1. **Grafana + huatuo-apiserver Pyroscope-compatible API** 是集成式在线
   方案。它复用 Elasticsearch 中现有的 Profile 文档，提供时间序列、Top N、
   火焰图和相邻时间窗对比。
2. **标准 pprof 与独立交互式 SVG 导出** 是便携方案。它和 Grafana 共用
   selector、时间范围、分页和合并逻辑，但不要求部署 Grafana。

Grafana provisioning 同时保留 #327 standalone Pyroscope 后端使用的直连
datasource。该可选后端不是上述两个入口的依赖。

## 方案对比

| 方案 | 筛选与聚合 | 视图 | 扩展方式 | 运维成本 | 结论 |
| --- | --- | --- | --- | --- | --- |
| Grafana + huatuo-apiserver | 精确索引维度、分组时序和有边界的 merge/diff | 时间序列、Top 10、火焰图 Top table、对比 | Elasticsearch 筛选后由 apiserver 有界聚合 | 复用现有组件 | 已交付 |
| Pyroscope OSS | 原生 series label、标签浏览和 group-by | 原生火焰图、Top table、对比和 diff | Profile 专用存储，可独立扩展组件 | 增加存储和生命周期 | 可通过 #327 启用 |
| Parca | Profile 原生标签和 pprof 写入 | 火焰图和跨标签/时间比较 | 独立 Profile 数据库和查询引擎 | 需要新增服务和写入路径 | 已评估，未部署 |
| FlameGraph RS | 必须在渲染前完成筛选 | 独立交互式 SVG | 本地按需渲染 | 较低，但不是持续 Profiling UI | 参考交互体验，不增加 Rust 依赖 |

目前没有在同一 HUATUO 数据集和硬件上对四个方案进行可复现的公开基准测试。
表格只记录架构和已验证的集成边界，不给出绝对性能排名。

## 已实现数据流

```text
profiler -> pprof + managed labels -> Elasticsearch
                                        |
Grafana <- Pyroscope-compatible API <- huatuo-apiserver
                                        |
                                        +-> pprof 下载
                                        +-> 交互式 SVG
```

Grafana 和导出接口使用相同的 Profile 类型、精确 label selector 和时间窗。
pprof 响应是 `go tool pprof` 可读取的 gzip 压缩标准 protobuf；SVG 可直接在
浏览器中搜索和缩放。

#327 启用直写 Pyroscope 后，Grafana 还可使用独立配置的
`huatuo-bamai-pyroscope` datasource；带鉴权的
`huatuo-apiserver-pyroscope` datasource 会同时保留。

## 多维筛选

采集器在 Profile 元数据和每个 pprof sample 中保存以下 managed label：

| 用户维度 | Selector |
| --- | --- |
| 采集范围 | `profiling_scope` |
| 逻辑 CPU 集合 | `cpu` |
| 精确进程或线程 | `pid` |
| 线程组 / 进程组 | `tgid` |
| 容器 / cgroup 目标 | `container_id` |
| 主机 | `hostname` |

`container_id` 是稳定的公开 cgroup selector，不暴露与宿主机实现绑定的
cgroup path。`tgid` 复用统一的 thread-group 语义，不再增加单独的
process-group 参数。

宿主机和容器 dashboard 提供可选的
`profiling_scope`/`cpu`/`pid`/`tgid` 精确变量，Top 10 面板可按任一 managed
dimension 或 tracer 分组。对比 dashboard 会把相同维度传给两个相邻时间窗。
变量为空时不添加该筛选条件。

查询 API 只接受等值 matcher，使 managed dimension 可以全部下推到
Elasticsearch，避免为正则条件解码大范围候选集。

## 视图覆盖

| 能力 | Grafana | pprof / SVG |
| --- | --- | --- |
| 火焰图 | 时间窗合并后的交互式火焰图和 Top table | 交互式 SVG 或任意 pprof 工具 |
| Top N | Top 10 分组时序和火焰图 Top table | `go tool pprof -top` |
| 对比 | 当前窗与紧邻的等长前一时间窗 | 下载两个 pprof 时间窗后由外部工具 diff |
| 时间序列 | 按时间桶求和或平均的 Profile 值 | 单个所选时间窗快照 |

文档计数面板表示写入可用性；Profile 值时间线表示累计 Profile 值，不等同于
系统 CPU 使用率。

## 海量数据边界

在线查询和导出在超过限制时明确失败，不静默截断：

- 单次最多选择 10,000 份 Profile 文档。
- 使用每页 1,000 份的稳定分页。
- `SelectSeries` 最多返回 100 条 series，dashboard 请求 Top 10。
- 火焰图默认最多 5,000 个节点，拒绝超过 10,000 的请求。
- 对比 JSON 适配器和独立 SVG 都拒绝超过 8 MiB 的响应。
- 独立 SVG 拒绝超过 10,000 个不同调用栈。
- 每个高基数 dashboard 维度最多列出 500 个精确值。

触发限制后需要缩小时间窗或 selector。这些边界用于保持 UI 响应，但不能替代
生产负载测试。发布验收应使用固定的大型 Profile fixture，记录延迟、响应大小和
apiserver 内存。

## 验收验证

仓库测试使用带 managed label 的 pprof fixture 验证：

- `cpu` 与 `profiling_scope` selector 能过滤存储数据；
- `SelectSeries` 可按 `pid` 分组；
- 同一个 managed label 可选择并生成合并火焰图；
- 相邻时间窗对比会传递所有 managed dimension；
- pprof 与 SVG 使用同一个有界加载器；
- dashboard 合约包含多维筛选、Top 10、时间序列、火焰图和对比请求。

真实 Grafana 渲染仍依赖部署环境中的 Grafana、Elasticsearch 和
huatuo-apiserver 服务可用。
