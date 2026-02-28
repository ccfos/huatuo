---
title: 自定义测试
type: docs
description:
author: HUATUO Team
date: 2026-01-30
weight: 5
---

集成测试用于验证 ``huatuo-bamai``在使用模拟的 ``/proc`` 和 ``/sys`` 文件系统时，能够正确启动并对外暴露符合预期的``Prometheus``指标。

测试运行的是真实的可执行文件，并通过校验 ``/metrics`` 接口的输出结果，确保指标采集与暴露逻辑正确，而不依赖宿主机的内核或硬件环境。

## 脚本执行流程

该集成测试脚本主要包含以下步骤：

1. 生成临时的``bamai.conf``配置文件
2. 使用模拟的 ``procfs`` 和 ``sysfs`` 启动 ``huatuo-bamai`` 服务
3. 等待 ``/metrics`` 接口可访问
4. 从 ``/metrics`` 接口拉取所有指标数据
5. 校验所有预期指标是否存在且内容匹配
6. 停止服务并清理相关资源
7. 若任意一个预期指标缺失或不匹配，测试将直接失败

## 运行方式

请在项目根目录下执行集成测试：
```bash
bash integration/integration.sh
```
或通过 Makefile 执行：
```bash
make integration
```

### 失败时的行为
- ``huatuo-bamai`` 服务指标和日志将直接输出到标准输出，便于问题定位
- 临时工作目录将被保留，用于后续调试分析

### 成功时的行为
- 显示验证成功的``metrics`` 列表
---

## 如何新增指标测试
### 第一步：新增或更新模拟数据
如果新增的指标依赖 ``/proc`` 或 ``/sys`` 文件内容，请在以下目录中新增或修改模拟数据：
```bash
integration/fixtures/
```
目录结构需与真实内核文件系统保持一致。

### 第二步：添加预期指标
在以下目录中新建一个文件：
```bash
integration/fixtures/expected_metrics/
├── cpu.txt
├── memory.txt
└── ...
```
每一行（非空、非注释行）表示一条期望的 Prometheus 指标，指标内容必须与 ``/metrics`` 接口返回结果完全一致，新增的``*.txt`` 文件会被测试脚本自动加载并参与校验。

### 第三步：运行测试
```bash
bash integration/integration.sh
```
当任意一个预期指标缺失或不匹配时，测试将失败。
