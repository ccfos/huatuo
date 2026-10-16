---
title: 变更日志
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 50
---

## 未发布

### 新增

- 新增 `tcpshark --with-dropwatch --bpf-path-dir <dir>`，通过一个共享过滤器将
  TCP 重传与 embedded dropwatch 源关联。
- 新增 `EventTracing.TCPRetransmit.EnableDropwatch`，用于 huatuo-bamai 的
  tcpshark 子进程。
- 为每次已定型的本地关联新增唯一的 `correlation_reason`，包括等待到期
  且重传发生时间早于 source ready 时输出的 `warmup`，以及独立的
  namespace 匹配诊断。

### 变更

- 将守护进程级的四元组缓存替换为严格的本地匹配，匹配依据
  namespace、报文方向、sequence 或 ACK 证据以及内核单调时间顺序。
- no-match 结果现在报告 `unknown`，并携带 embedded dropwatch 的累计丢失计数、
  map 计数可用性和唯一终态。重传最多等待 100ms，候选 drop 的因果年龄上限为 1s。
- `EventTracing.TCPRetransmit.Filter` 现在同时控制两种模式下的重传采集。本地
  模式将其应用于两条输入，空过滤器默认规范化为 `tcp`，并拒绝无法在
  合成 L3 数据上等价执行的以太网地址过滤器。
- 退出时 tcpshark 会在子进程结束前完成待处理本地结果的定型。embedded
  drop 保持私有；standalone dropwatch 的原始输出不变。
