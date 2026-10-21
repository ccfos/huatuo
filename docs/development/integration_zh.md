---
title: 集成测试
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

集成测试运行真实程序，验证采集、接口和外部依赖之间的行为。指标测试使用模拟的 `/proc`、`/sys` 文件系统，并检查 `/metrics` 输出；其他用例按各自条件准备进程、网络或采集环境。

直接执行测试脚本前需完成 `make build`。本文说明运行入口、结果判定和新增指标测试的方法。

## 指标测试流程

指标测试依次执行：

1. 生成临时的 `bamai.conf` 配置文件
2. 使用模拟的 `procfs` 和 `sysfs` 启动 `huatuo-bamai` 服务
3. 等待 `/metrics` 接口可访问
4. 从 `/metrics` 接口拉取所有指标数据
5. 校验所有预期指标是否存在且内容匹配
6. 停止服务并清理相关资源

任意预期指标缺失或不匹配时，测试失败。

## 运行方式

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

## 前置检查与结果汇总

两套测试共用 `integration/run.sh`，默认运行 integration；`e2e/run.sh` 转发到
`--suite e2e`。现有 `make integration`、`make e2e` 和单用例入口继续可用：

```bash
bash integration/run.sh --suite e2e
bash integration/run.sh --suite e2e test_metrics.sh 2
bash e2e/run.sh test_metrics.sh 2
```

两套测试都在独立的 UTS、mount namespace 中运行，hostname 为 `huatuo-dev`，
挂载传播设为 private。integration 由用例启动服务；e2e 在每例之前启动默认 bamai，
结束时停止服务并检查日志。integration 成功或跳过后删除工作目录，失败时保留；
e2e 保留工作目录。

apiserver 的完整采样链路和 Kubernetes 多语言矩阵位于
`e2e/test_apiserver_profiling.sh`、`e2e/test_apiserver_profiling_matrix.sh`，
由 `make e2e` 自动发现。矩阵需设置 `APISERVER_TEST_NODE` 并提供 kubelet 凭证。
停止、同机并发、apiserver 重启恢复和 Node 故障恢复分别由同目录下的
`test_apiserver_profiling_{stop,concurrent,recovery,node_recovery}.sh` 验证。
其余 8 项 apiserver 功能由 `integration/test_apiserver_profiling.sh` 接入
`make integration`。
运行要求和命令见 [apiserver 测试](../../integration/apiserver/README.md)。

runner 只检查 `_output` 目录是否存在，不逐个检查项目二进制和 BPF 产物。
直接运行脚本前先执行 `make build`；
目录存在不代表构建完整，缺失产物会在实际使用时导致失败。

用例使用同一个函数检查 PATH 中的命令或可执行文件路径，缺失时跳过：

```bash
require_commands jq curl ss
require_commands "${PROFILER_TOOL_DIR}/bin/asprof"
```

证书等输入文件使用 `require_readable` 检查，不可读时输出具体路径并跳过：

```bash
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
```

`skip` 输出原因并以 77 退出，仍会执行 `EXIT` 清理。runner 将 0 记为 PASS、
77 记为 SKIP，其他状态记为 FAIL；清理失败也记为 FAIL。只有成功和跳过时，
runner 返回 0。失败时停止后续用例，并输出截至当前的统计。

每次执行都单独计数，包括重复运行，例如：

```text
summary: total=5 passed=3 skipped=2 failed=0
```

两套测试缺少 root 权限或 namespace 命令时，所选用例全部计为 SKIP。

### 失败处理

- `huatuo-bamai` 服务指标和日志将直接输出到标准输出，便于问题定位
- 临时工作目录将被保留，用于后续调试分析

### 成功结果

- 显示验证成功的指标列表

## 性能剖析

运行前需完成 `make build`，并根据 `integration/env.sh` 配置 Java 或 Python 采集工具路径。

仓库集成测试提供了可执行的端到端示例。测试会创建目标进程、运行 profiler，并校验输出中的预期调用栈：

```bash
# 原生 CPU
sudo ./integration/run.sh test_profiler_native_cpu.sh

# 原生 off-CPU 阻塞与调度延迟
sudo ./integration/run.sh test_profiler_native_cpu_offcpu.sh

# 原生虚拟内存与物理内存
sudo ./integration/run.sh test_profiler_native_memory_virtual_alloc.sh
sudo ./integration/run.sh test_profiler_native_memory_physical_usage.sh

# Java CPU 与内存
sudo ./integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo ./integration/run.sh test_profiler_java_memory_usage_alloc.sh

# Python 多进程 CPU
sudo ./integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

容器、线程组和指定 CPU 的示例分别位于 `test_profiler_native_cpu_container.sh`、`test_profiler_native_cpu_thread_group.sh` 和 `test_profiler_native_cpu_cpuid.sh`。

## 存储测试

以下用例均不需要 Elasticsearch 服务：

| 用例 | 验证内容 |
| --- | --- |
| `test_basic_storage_elasticsearch_config.sh` | `Address`、`Username`、`Password` 的全部八种组合：部分配置必须以配置错误退出，全空或全配必须启动就绪。全配用例使用 `--disable-storage`，隔离后端可用性的影响。 |
| `test_basic_storage_localfile_rotation.sh` | 只启用 LocalFile，向未监听的 UDP 端口发包，以 Dropwatch 事件触发轮转。确认首个备份被淘汰，最终保留两个备份和一个活动文件；每个文件不超过 1 MiB，且包含有效的目标事件 JSON。 |

轮转用例需要内核 BTF、`skb/kfree_skb` tracepoint，以及 Dropwatch 二进制和 BPF 对象。它与后端一致性测试共用 UDP 发包和配置函数。调用 `write_storage_events_config` 时必须设置 `STORAGE_EVENT_RATE`、`STORAGE_ROTATION_MIB` 和 `STORAGE_MAX_ROTATED_FILES`。

两个用例均在启动服务前调用 `integration_isolate_run_directory`，隔离 PID 锁和 Toolstream socket。配置、轮转和后端一致性测试在检查最终数据和日志前调用 `assert_huatuo_bamai_shutdown`，确认服务正常退出；`EXIT` trap 负责尽力清理。

## 后台进程清理

`stop_and_wait_by_pid PID [TIMEOUT]` 停止并等待子进程，返回其退出码。
未传入 PID 或 PID 为空时立即返回成功，调用方可以直接传入已初始化的 PID 变量。
等待结束后清空变量；尽力清理时使用 `|| true`，退出码属于测试契约时应进行断言。

## 网络命名空间辅助函数

网络测试可以在加载 `lib.sh` 后加载 `integration/lib_namespace.sh`：

- `namespace_setup NAME` 创建一个网络命名空间，通过 `NETNS` 暴露名称。
  调用方负责启用 `lo` 并配置所需地址。
- `namespace_setup_with_pair PREFIX SERVER_ADDR CLIENT_ADDR [PREFIX_LENGTH]`
  创建通过 veth 连接的两个命名空间，配置地址并启用接口，地址前缀长度默认为 24。
  命名空间、接口和地址通过 `NETNS_SERVER`、`NETNS_CLIENT`、
  `NETNS_VETH_SERVER`、`NETNS_VETH_CLIENT`、`NETNS_SERVER_ADDR`、
  `NETNS_CLIENT_ADDR` 暴露。
- `namespace_cleanup` 删除上述函数创建的命名空间并清空状态，支持重复调用。
  用例应在 `EXIT` 清理中先停止命名空间内的进程，再调用该函数。

## 共用发包程序

需要构造原始报文的测试统一复用 `integration/testdata/test_packet_sender.py`，
新增场景在该程序中扩展。程序依赖 Python 3 和 raw socket 权限，支持
`ipv4-options`（TCP/UDP EOL options，包含最大 IHL）与 `icmpv6-echo`。
通过子命令的 `--help` 查看地址、端口或 Echo 标识参数。
程序每 100 ms 重复发包，收到 SIGTERM 后正常退出。
Shell 用例负责命名空间、丢包规则、断言、日志和进程清理。
需要真实 TCP 连接的负载继续使用 `testdata/tcp_server.py`。

## 新增指标测试

### 1. 准备模拟数据

如果新增的指标依赖 `/proc` 或 `/sys` 文件内容，请在以下目录中新增或修改模拟数据：
```bash
integration/fixtures/
```
此目录仅保留内核文件系统镜像（`proc/`、`sys/`、`dev/`），保持原始路径和文件名。
工具夹具放在 `integration/testdata/tools/`，文件名以 `test_` 或 `Test` 开头。
例如，测试启动前将 `test_perf.sh` 复制到临时工具目录并命名为 `perf`。

### 2. 添加预期指标

在以下目录中新建一个文件：
```bash
integration/testdata/expected_metrics/
├── test_cpu.txt
├── test_memory.txt
└── ...
```
每个非空、非注释行表示一条预期的 Prometheus 指标，必须与 `/metrics` 返回的指标行完全一致。
测试自动加载 `test_*.txt`，从文件名去掉 `test_` 前缀后确定指标前缀。

### 3. 运行测试

```bash
bash integration/run.sh
```
当任意一个预期指标缺失或不匹配时，测试将失败。
