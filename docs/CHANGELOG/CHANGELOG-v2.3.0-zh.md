---
title: 变更日志
type: docs
description:
author: HUATUO Team
date: 2026-08-13
weight: 50
---

#### 特性

- 增加统一 profiler 工具，支持原生程序、Java 和 Python CPU/内存性能剖析
- 增加原生程序 Off-CPU 剖析，区分阻塞等待与运行队列调度延迟
- 增加原生程序虚拟内存分配、物理内存分配及物理内存使用剖析
- 增加 huatuo-apiserver 和持续性能剖析任务 API
- 增加持续性能剖析数据聚合、持久化及 Pyroscope 兼容查询接口
- 增加 perf 终端交互式火焰图
- 增加独立 dropwatch 工具，支持 pcap 过滤、设备过滤和事件限速
- 增加 devlink 硬件丢包观测及软硬件丢包去重
- 增加 tcpshark 工具和 TCP 重传事件，支持关联丢包位置
- 增加 Agent SSE 内核事件实时订阅接口
- 增加 Agent 节点任务和 tracer 控制接口
- 增加昇腾 NPU 芯片、HBM、PCIe、RoCE 和光模块指标
- 增加磁盘 IO 基础指标及块设备、容器维度 I/O 延迟指标
- 增加 OOM 事件内存限制、使用量及系统/容器内存快照
- 增加 ARM GHES 处理器错误检测
- 增加 CPU 外部等待率指标
- 增加 AutoTracing 容器过滤、组合问题规则及增量阈值配置
- 增加 Elasticsearch 7/8、OpenSearch 和 SQLite 存储后端
- 增加按 tracer 分文件及轮转的本地数据日志
- 增加 Helm Chart、Prometheus Kubernetes 服务发现及部署资源基线
- 增加宿主机、容器持续性能剖析及磁盘 IO Grafana 面板
- 增加所有主要二进制版本输出及 HTTP `/healthz`、`/version` 接口
- 增加 toolstream 类型化事件通道，统一承载诊断子进程输出
- 增加 BPF ABI 自动生成及 BPF 对象租约管理

#### BUG 修复/优化

- 优化 net_rx_latency，兼容多版本 skb 时间戳布局及非 ESTABLISHED TCP 接收路径
- 优化 RAS 事件结构化输出及内核 tracepoint 能力探测
- 优化采集器依赖检查，缺失 procfs 或 memcg 能力时自动降级
- 优化 AutoTracing 触发规则、iotracing 采集上限及结果存储链路
- 优化存储异步写入退出排空及 insert-only 写入语义
- 优化 Kubernetes Pod cgroup 生命周期兼容性
- 优化 CPU 事件批量读取、perf reader 丢样恢复及 kretprobe 并发配置
- 优化 ARM GHES、folio、skb 时间戳及 tracefs kprobe 符号兼容性
- 优化丰富文档 https://docs.huatuo.tech/zh/
