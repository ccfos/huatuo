---
title: 持续 Profiling 展示方案评估
type: docs
description: 持续 Profiling 在线与独立展示方案、能力边界及扩展选择
author: HUATUO Team
date: 2026-07-16
weight: 6
---

## 范围与结论

本文评估 [Issue #328](https://github.com/ccfos/huatuo/issues/328) 中提出的
Grafana、Pyroscope、Parca 和 FlameGraph RS，并记录本次实现的能力边界。

本次实现提供两个可独立验收的展示入口：

1. **方案 A：Grafana + huatuo-apiserver**。Grafana 的 Pyroscope 数据源访问
   apiserver 的 Pyroscope-compatible 查询接口，apiserver 从 Elasticsearch
   查询并聚合现有 pprof 数据。这是集成式、默认的在线展示方案。
2. **方案 B：standalone pprof / interactive SVG**。使用与方案 A 相同的标签
   selector 和时间窗口，从 apiserver 下载标准 pprof，或直接打开独立的交互式
   SVG 火焰图。该入口不依赖 Grafana，也不引入新的存储后端。

Pyroscope 原生服务和 Parca 在本次实现中仅作为候选方案评估，**没有部署、没有
写入数据，也不应被视为本分支已经支持的展示入口**。

## 方案对比

| 方案 | 多维筛选与聚合 | 视图能力 | 性能与扩展方式 | 运维成本 | 本次选择 |
| --- | --- | --- | --- | --- | --- |
| Grafana + Pyroscope-compatible 数据源 | 支持 Prometheus 风格的 label selector；时序数据可按标签分组。实际能力取决于后端实现的 Label、Series、Merge 和 Diff API | Flame graph、Top table、时间序列；交付的对比页提供双时间窗，后端另提供 Diff RPC | Grafana 只负责查询和渲染；存储与聚合性能由 apiserver/Elasticsearch 决定，可通过限制时间窗、返回 series 数和火焰图节点数控制响应量 | 复用现有 Grafana、ES 和 apiserver，增量成本最低 | **方案 A，已支持** |
| Pyroscope OSS 原生 UI | Tag Explorer 和 label selector 适合标签筛选；时间序列支持 group-by | Flame graph、Top table、Sandwich、Comparison 和归一化 Diff 均为原生能力 | v2 可将读写路径独立扩容并使用对象存储；单节点模式适合开发和小规模验证 | 需要新增 Pyroscope 存储、数据双写、持久化、升级和鉴权代理 | 已评估，未随本分支交付 |
| Parca | 多维标签模型和 label-selector 查询；标准 pprof 可作为交换格式 | 面向持续 Profiling 的火焰图和跨标签/时间比较 | 内置针对 Profile 的存储和查询引擎，支持保留原始数据后切片和聚合 | 需要新增 Parca 服务、写入适配和独立的生命周期管理 | 已评估，未随本分支交付 |
| FlameGraph RS | 本身不提供持续数据的标签索引；必须在生成 SVG 前由调用方完成筛选和聚合 | 生成可交互 SVG，适合单次或离线分析；不提供持续 Profiling 的 Top N、时间序列或标签浏览器 | 本地生成、依赖少；符号内联处理可能明显增加生成时间。项目文档也指出传统比例火焰图较粗粒度且难以直接 diff | 最低，但不是持续 Profiling 后端或完整 Web UI | 参考其独立 SVG 体验；本分支不增加 Rust 依赖 |

相关官方资料：

- [Grafana flame graph panel](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/visualizations/flame-graph/)
- [Grafana Pyroscope query editor](https://grafana.com/docs/grafana/latest/datasources/pyroscope/query-profile-data/)
- [Pyroscope UI](https://grafana.com/docs/pyroscope/latest/view-and-analyze-profile-data/pyroscope-ui/)
- [Pyroscope server API](https://grafana.com/docs/pyroscope/latest/reference-server-api/)
- [Pyroscope v2 architecture](https://grafana.com/docs/pyroscope/latest/reference-pyroscope-v2-architecture/about-pyroscope-v2-architecture/)
- [Parca](https://github.com/parca-dev/parca)
- [FlameGraph RS](https://github.com/flamegraph-rs/flamegraph)
- [pprof：标签与 Profile 对比](https://github.com/google/pprof/blob/main/doc/README.md)

目前没有使用同一数据集和硬件对四个项目进行公开、可复现的基准测试，因此本文
不对它们给出绝对性能排名。表中的性能结论只描述各项目官方文档公开的架构和本次
实现可以验证的边界。

## 已实现的数据流

### 方案 A：Grafana 在线查询

```text
profiler -> standard pprof + labels -> Elasticsearch
                                      |
Grafana <- Pyroscope-compatible API <- huatuo-apiserver
```

apiserver 提供 ProfileTypes、LabelNames、LabelValues、SelectSeries、
SelectMergeStacktraces 和 Diff 等 Pyroscope-compatible 接口。Grafana 使用同一个
profile type、label selector 和时间范围查询时间序列、普通火焰图与双时间窗对比。
Diff 在服务端分别聚合 baseline 和 comparison 后返回差异数据，供显式提交两组
selection 的兼容客户端使用；它不是 Pyroscope 服务的代理，也不要求部署 Pyroscope。

`Continuous Profiling Comparison` 仪表盘提供两个并排的火焰图与 Top table：左侧
使用当前时间窗，右侧默认使用同一时间窗向前偏移一小时。两侧共享 profile type、
主机/容器、CPU、PID/TGID、cgroup、进程组等 selector，便于快速定位变化。当前
分支没有配置 Grafana Profiles Drilldown 所需的 `service_name` 服务发现，因此不把
其归一化红/绿 Diff UI 作为已验收功能；后端 Diff RPC 可单独进行协议级验证。

### 方案 B：独立 pprof 与 SVG

```text
                         +-> pprof download -> go tool pprof / other pprof tools
Elasticsearch -> apiserver
                         +-> interactive SVG -> browser
```

独立入口和方案 A 共用查询解析、权限检查、时间范围及文档合并逻辑，避免出现
“Grafana 看到一组数据、下载接口得到另一组数据”的差异。pprof 保留完整 Profile
结构，SVG 适合无需 Grafana 的快速浏览。

方案 B 不提供 Pyroscope 风格的归一化红/绿 Diff SVG。需要内置对比视图时使用
方案 A；下载的两份 pprof 也可交给支持 pprof diff 的外部工具处理。

两个独立入口使用相同的必填查询参数：`profile_type`、Prometheus 格式的
`selector`、毫秒 Unix 时间戳 `start` 和 `end`：

```text
GET /v1/profiles/flamegraph/export/pprof?profile_type=...&selector=...&start=...&end=...
GET /v1/profiles/flamegraph/export/svg?profile_type=...&selector=...&start=...&end=...
```

pprof 响应是 gzip 压缩的标准 protobuf，可直接交给 `go tool pprof`；SVG 响应可
直接在浏览器打开，并支持搜索和缩放。selector 和 profile type 应进行 URL 编码，
接口沿用 profiles API 的访问控制。

## 多维筛选与聚合

Profile 类型用于选择 CPU、内存或锁数据。由 #329 注入的以下 series/sample 标签
用于进一步筛选：

| 维度 | 标签 |
| --- | --- |
| 采集范围 | `profiling_scope` |
| 逻辑 CPU 集合 | `cpu` |
| 线程 / 进程 | `pid`、`tgid` |
| cgroup | `cgroup_id`、`cgroup_path` |
| 进程组 | `process_group_id` |
| 容器 | `container_id` |
| 锁 | `lock_type` |

多个非空标签条件按 AND 组合，并在所选时间窗口内合并匹配的 Profile。方案 A 的
Grafana 变量和方案 B 的查询参数最终都转换为同一 label selector。

当前实现的边界：

- Profile type 用于选择 CPU Profile；`cpu` 标签只适用于使用 native provider 的
  C、C++、Go CPU Profiling，记录采集任务选择的逻辑 CPU 集合（例如 `0,1`）。
  Java/Python provider 不接受该选项。它是任务级精确标签，不表示每个样本实际
  运行的 CPU，因此不能据此把同一份 Profile 重新拆分为逐核样本。
- Grafana 变量从 Elasticsearch keyword 读取精确值并保持单选，以控制基数；生成
  selector 时使用转义后的正则，All 使用标准 `=~".*"`，不占用 API 中的特殊字面量。
  查询 API 支持 Prometheus 的 `=`、`!=`、`=~`、`!~` matcher；非精确条件在最多
  10000 份候选 Profile 解码后过滤，候选集超限时请求会失败而不是返回不完整结果。
- 自定义 pprof label 会随原始 pprof 保留，也可在上述有界候选集内通过 selector
  过滤或 group-by；但它不会自动出现在 Grafana 变量或 LabelValues 中，也不能像
  已公开的 collection dimension 一样下推到 Elasticsearch，因此高基数查询成本更高。

## 视图能力与边界

| 能力 | 方案 A：Grafana | 方案 B：pprof / SVG |
| --- | --- | --- |
| 火焰图 | 支持按时间窗聚合的交互式 flame graph | 支持独立交互式 SVG；pprof 可由兼容工具打开 |
| Top N | Grafana flame graph panel 的 Top table 按 Self/Total 排序；分组时序面板按所选维度限制 Top 10 series | 下载 pprof 后可使用 `go tool pprof -top`；SVG 本身不是 Top N 表格 |
| 对比 / Diff | `Continuous Profiling Comparison` 提供当前窗/前一小时双视图；apiserver 另提供可协议验证的 Diff RPC | 不内置归一化 Diff SVG；可分别下载 baseline/comparison pprof |
| 时间序列 | 展示匹配 Profile 的时间分布；可据此缩小火焰图时间窗 | SVG 是所选时间窗的快照，不提供持续时间轴 |

`SelectSeries` 时间序列按 Profile sample value 求和或平均，可按一个公开标签分组；
原有 Profile 文档计数面板仍只表示采样点分布。两者都不等价于系统 CPU 使用率，
不能替代系统 CPU 指标。

## 性能边界

本次实现保持原采集管线不变，但查询仍有明确边界：

- Profile 从 Elasticsearch 读取后在 apiserver 进程内合并，内存和 CPU 开销随
  时间窗内的 Profile 数、样本数和栈基数增长。
- 普通火焰图、独立 pprof/SVG 和 Diff 设置 10000 份 Profile 文档的硬上限。
  存储计数超过上限时请求会失败并要求缩小时间窗或 selector，不会静默返回
  “最新 10000 份”的不完整结果。
- Diff 要执行 baseline 和 comparison 两次聚合，通常比单火焰图占用更多查询时间
  和内存；生产环境应限制两侧时间窗。
- 标签值查询依赖 Elasticsearch keyword 索引。高基数 PID、cgroup 或自定义标签
  会增加索引和聚合成本，不应在没有上限的变量中一次展开全部值。
- standalone SVG 是按需生成的快照，不新增长期缓存或第二份存储，因此不会影响
  数据采集，但重复的大时间窗请求仍会重复消耗合并和渲染资源。

“海量数据不卡顿”不能仅由单元测试证明。发布验证应固定数据规模，分别记录
LabelValues、SelectSeries、单火焰图、Diff 和 SVG 的延迟、响应体大小以及
apiserver 峰值内存，并以同一版本的基线判断是否回退。

## 与 #329、#353 的边界

- [#329](https://github.com/ccfos/huatuo/issues/329) 负责 PID/TGID、cgroup、
  进程组等采集范围，以及把这些维度注入 Profile 标签。#328 的多维展示依赖这些
  标签存在，但不应把锁采集或 eBPF 采集改动重复计入前端功能。
- [PR #353](https://github.com/ccfos/huatuo/pull/353) 面向 #327 的 AutoTracing
  standalone Pyroscope 模式。它不是 #328 的依赖，本次实现也没有把该 PR 的
  Pyroscope 存储或 Docker 编排整体合入。
- 如果以后选择 Pyroscope/Parca 作为新的在线存储，应新增独立的 profile sink，
  将 pprof protobuf 和所有维度提升为 series labels，并与现有 Elasticsearch 写入
  并行；不应为了第二种展示方案改变 eBPF/第三方 profiler 的采样逻辑。
