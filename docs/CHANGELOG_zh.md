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

- 增加 `tcpshark --with-dropwatch --bpf-path-dir <dir>`，使用同一个共享
  filter 将 TCP 重传与 embedded dropwatch 源关联。
- 为 huatuo-bamai 的 tcpshark 子进程增加
  `EventTracing.TCPRetransmit.EnableDropwatch`。
- 为每次本地关联定型增加单一 `correlation_reason`，其中 `warmup` 对应等待
  已到期且重传早于来源就绪的情况，并增加独立的 namespace 匹配诊断。

### 变更

- 以跨 namespace、方向、sequence/ACK 证据与单调时序的严格本地匹配，取代
  守护进程全局的元组缓存。
- no-match 结果现在报告 `unknown`，并附带 embedded dropwatch 的累计丢失
  计数、map 计数可用性与唯一终态原因。重传最多等待 100 ms，候选丢包受
  1 秒因果年龄上限约束。
- `EventTracing.TCPRetransmit.Filter` 现在在两种模式下都控制重传采集。
  本地模式将该 filter 应用于两个输入，空 filter 默认为 `tcp`，并拒绝无法
  在 synthetic L3 数据上等价执行的 Ethernet 地址 filter。
- 现在关闭时会在子进程退出前于 tcpshark 内部定型尚未完成的本地结果。
  embedded drop 仍保持私有；独立 dropwatch 的 raw 输出保持不变。
