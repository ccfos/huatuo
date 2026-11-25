---
title: 开发手册
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 8
---

开发前先阅读[贡献指南](/docs/contribute/contribute_code_zh.md)，并按[源码编译](/docs/compiling_zh.md)准备构建环境。

| 开发任务 | 文档 |
| --- | --- |
| 添加指标、事件或自动追踪 | [扩展功能](/docs/development/custom-observability_zh.md) |
| 定义 BPF 事件及生成 Go 类型 | [BPF ABI 指南](/docs/development/bpf_abi_codegen_zh.md) |
| 统一字段语义和时间格式 | [字段规范](/docs/development/event-field-naming_zh.md) |
| 联调组件、输出 BPF 日志或在虚拟机中排查问题 | [开发调试](/docs/development/debugging_zh.md) |
| 验证跨组件行为 | [集成测试](/docs/development/integration_zh.md) |
| 理解 TCP 重传与丢包关联设计 | [重传与丢包关联设计](/docs/development/tcp_retransmit_dropwatch_correlation_zh.md) |
| 理解存储写入和查询契约 | [存储接口](/docs/development/storage_zh.md) |
