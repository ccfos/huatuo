---
title: 集成测试
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

集成测试用于验证 ``huatuo-bamai``在使用模拟的 ``/proc`` 和 ``/sys`` 文件系统时，能够正确启动并对外暴露符合预期的``Prometheus``指标。

测试运行的是真实的可执行文件，并通过校验 ``/metrics`` 接口的输出结果，确保指标采集与暴露逻辑正确，而不依赖宿主机的内核或硬件环境。

### 脚本执行流程

该集成测试脚本主要包含以下步骤：

1. 生成临时的``bamai.conf``配置文件
2. 使用模拟的 ``procfs`` 和 ``sysfs`` 启动 ``huatuo-bamai`` 服务
3. 等待 ``/metrics`` 接口可访问
4. 从 ``/metrics`` 接口拉取所有指标数据
5. 校验所有预期指标是否存在且内容匹配
6. 停止服务并清理相关资源
7. 若任意一个预期指标缺失或不匹配，测试将直接失败

### 运行方式

请在项目根目录下执行集成测试：
```bash
bash integration/run.sh
```

指定文件名可以只运行一个集成测试，第二个参数指定循环次数，默认为 1：

```bash
bash integration/run.sh test_metrics_exclude_filter.sh 10
```

或通过 Makefile 执行：
```bash
make integration
```

#### 失败时的行为
- ``huatuo-bamai`` 服务指标和日志将直接输出到标准输出，便于问题定位
- 临时工作目录将被保留，用于后续调试分析

#### 成功时的行为
- 显示验证成功的``metrics`` 列表
---

### 如何新增指标测试
#### 第一步：新增或更新模拟数据
如果新增的指标依赖 ``/proc`` 或 ``/sys`` 文件内容，请在以下目录中新增或修改模拟数据：
```bash
integration/fixtures/
```
目录结构需与真实内核文件系统保持一致。

#### 第二步：添加预期指标
在以下目录中新建一个文件：
```bash
integration/fixtures/expected_metrics/
├── cpu.txt
├── memory.txt
└── ...
```
每一行（非空、非注释行）表示一条期望的 Prometheus 指标，指标内容必须与 ``/metrics`` 接口返回结果完全一致，新增的``*.txt`` 文件会被测试脚本自动加载并参与校验。

#### 第三步：运行测试
```bash
bash integration/run.sh
```
当任意一个预期指标缺失或不匹配时，测试将失败。

### 真实 cgroup CPU 容量测试

`test_cpu_capacity.sh` 验证祖先与叶级配额、不同 period、cpuset 继承、resize
配置变化以及两个兄弟 cgroup 共享的 CPU 预算。运行需要 Linux cgroup v2、Go，
以及显式委派的可写目录，其 `cgroup.subtree_control` 已启用 `cpu` 和 `cpuset`：

```bash
HUATUO_CPU_CAPACITY_CGROUP_ROOT=/path/to/delegated/test-cgroup \
  bash integration/run.sh test_cpu_capacity.sh
```

请使用专用测试环境，不要使用生产层级。测试不会替所传目录启用控制器或修改其
限额，只在其下创建独占子树：总上限为 1 CPU，两个运行两秒的工作进程共享父级
0.5 CPU 配额，结束后仅清理自建进程和 cgroup。前置条件不足会明确输出 `SKIP`，
不代表验证通过；cpuset 只有一个 CPU 时仅跳过 cpuset resize 断言。collector
跨配置区间的弃样行为由独立的假时钟 CPU 利用率单元测试验证。
