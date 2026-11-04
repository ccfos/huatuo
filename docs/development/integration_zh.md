---
title: 集成测试
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

集成测试和 E2E 测试用于验证 Huatuo 的采集、服务接口及外部依赖。本文说明如何运行测试、配置测试环境和查看结果。

## 运行测试

在项目根目录执行。测试需要 Linux 和 root 权限；涉及容器、Pod 或块设备的用例应在专用测试节点运行。

通过 Makefile 构建并运行：

```bash
sudo make integration
sudo make e2e
sudo make test
```

`make test` 包含单元测试、集成测试和 E2E 测试。也可以先构建，再以 root 运行脚本：

```bash
make build test-build
sudo bash integration/run.sh
sudo bash e2e/run.sh
```

### 单用例与重复运行

脚本名后可指定重复次数，默认为 1，范围为 1–1,000。所选测试数量乘以重复次数不得超过 10,000。

```bash
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
sudo bash e2e/run.sh test_metrics.sh 2
```

也可以通过 `--suite e2e` 选择 E2E：

```bash
sudo bash integration/run.sh --suite e2e test_metrics.sh 2
```

无效参数返回退出码 2，不创建运行目录或报告。

### 并发运行

集成测试默认单 worker，`-j` 支持 1–8；也可以设置 `INTEGRATION_JOBS`：

```bash
sudo bash integration/run.sh -j 2
sudo env INTEGRATION_JOBS=2 make integration
```

部分用例需要独占运行，实际并发数可能低于设置值。每台主机同时只运行一个测试套件。E2E 始终串行运行，不支持 `-j` 大于 1。

## 环境要求

运行前需完成 `make build test-build`；缺失构建产物会导致失败。报告依赖 `jq`，测试环境还需提供 `unshare`、`mount`、`flock`、`ss`、`stat`。具体用例会检查其所需的外部命令、内核能力、服务和输入文件。

缺少 root 权限或必要的 namespace 命令时，所选用例报告 SKIP。用例缺少外部依赖时也会报告具体跳过原因；SKIP 不表示验证通过。

常用环境变量：

| 变量 | 用途 |
| --- | --- |
| `INTEGRATION_JOBS` | 集成测试 worker 数，默认 1 |
| `KUBELET_CERT`、`KUBELET_KEY` | 本机 kubelet 的客户端证书和私钥路径 |
| `PROFILER_TOOL_DIR` | Java/Python 采集工具目录，默认 `_output/tools` |
| `APISERVER_TEST_NODE` | apiserver 矩阵测试使用的本机 Kubernetes 节点名 |
| `APISERVER_TEST_NATIVE_IMAGE`、`APISERVER_TEST_GO_IMAGE`、`APISERVER_TEST_JAVA_IMAGE`、`APISERVER_TEST_PYTHON_IMAGE` | apiserver 矩阵测试镜像 |
| `TEST_IOCOST_MODE` | IOCOST 测试范围，默认 `all` |
| `TEST_NVME_DEVICE`、`TEST_SCSI_DEVICE` | IO 健康取证使用的专用测试设备 |
| `TEST_IO_CONFIRM_NON_SYSTEM` | 显式指定 IO 取证设备时设为 `1`，确认其不是系统设备 |

通过 `sudo env NAME=value ...` 传入需要覆盖的环境变量，避免被 sudo 的环境过滤规则丢弃。

## 结果与日志

| 结果 | 含义 |
| --- | --- |
| PASS | 测试和清理成功 |
| SKIP | 依赖不满足、未启动或被取消；具体原因见报告 |
| FAIL | 测试、服务准备或清理失败 |

失败后停止启动新用例；未启动项和被取消的执行记为 SKIP。INT/TERM 中断返回 130/143。仅有跳过项时可能返回成功，因此应同时检查计数和跳过原因。

每次有效调用创建 `/tmp/huatuo-test.XXXXXX/`。成功、失败和跳过的日志及工作文件均保留，可整体归档或删除。运行目录包含 `test_run_results.json`；每次执行的工作目录包含 `stdout.log`、`stderr.log` 及服务日志、配置和响应数据。

### 终端摘要

终端显示各用例结果，最后输出 JSON 摘要：

| 字段 | 含义 |
| --- | --- |
| `status` | `completed`、`aborted` 或 `interrupted` |
| `output_dir`、`results_path` | 运行目录和完整报告路径 |
| `runs_per_test` | 每个测试计划执行的次数 |
| `started_at`、`finished_at`、`duration_seconds` | 开始、结束时间和耗时 |
| `execution_counts` | `total`、`passed`、`failed`、`skipped`，包含重复执行和未启动项 |
| `skipped_tests` | 跳过的测试名称 |
| `failed_tests` | 失败测试的名称、执行序号、原因和日志 |
| `reason` | 提前结束原因；正常结束时省略 |

`failed_tests` 每项包含 `name`、`run_index`、`reason` 和 `logs`。每个日志项包含相对于 `output_dir` 的 `path`、`content` 和截断标记 `truncated`。

日志覆盖该次失败工作目录中的非空 `.log`、`.err`、`.out`、`.json`、`.conf` 文本文件，排除二进制、`test_run_results.json` 和 `reason.*.log`。每个文件最多展示末尾 64 KiB；完整内容仍保留在原文件中。排查失败时先读 `reason` 和 `stderr.log`，再查看相关服务日志及响应数据。

完整、合法的 `.json` 文件以 JSON 值嵌入 `content`；无效或截断的 JSON 保留为文本。失败原因优先使用显式诊断；未捕获的命令失败记录脚本、行号、命令和退出码。无法记录原因时保留退出码兜底，已捕获的子进程错误不会作为测试失败原因。

### 完整报告

`test_run_results.json` 的 `schema_version` 为 1：

| 字段 | 含义 |
| --- | --- |
| `suite`、`runs_per_test` | 测试套件和每个测试计划执行的次数 |
| `status` | `running`、`completed`、`aborted`、`interrupted` |
| `exit_code`、`reason` | 测试运行的退出码和提前结束原因 |
| `started_at`、`finished_at`、`duration_seconds` | 开始、结束时间和耗时 |
| `execution_counts` | 执行计数 |
| `test_executions` | 每个测试每次计划执行的记录 |

执行记录包含 `test_name`、`run_index`、`result`、`exit_code`、`reason`、时间字段及 `logs.stdout` / `logs.stderr`，日志路径相对于报告目录。`result` 为 `passed`、`failed` 或 `skipped`；运行中未结束的记录为 `null`。未启动项没有时间和日志；无法确认的进程退出码为 `null`。清理失败可能使进程退出码为 0 的执行仍被判为 `failed`。

`status: running` 表示报告尚未完成，不能作为最终结果。运行被强制终止或报告写入失败时，可能保留此状态。

查看失败或跳过原因：

```bash
jq '.test_executions[] | select(.result == "failed" or .result == "skipped") | {test_name, run_index, result, reason, logs}' /tmp/huatuo-test.XXXXXX/test_run_results.json
```

## 常用专项测试

### CPU 指标与自动追踪

```bash
sudo bash e2e/run.sh test_metrics_cpu_util.sh
sudo bash e2e/run.sh test_metrics_cpu_stat.sh
sudo bash e2e/run.sh test_metrics_cpu_runqlat_tracing.sh
sudo bash e2e/run.sh test_autotracing_cpuidle.sh
sudo bash e2e/run.sh test_autotracing_dload.sh
```

这些用例需要本机 Kubernetes/kubelet 访问权限；基准环境为 4 核、8 GB 内存的 VM。安装 `stress-ng`，CPU util/stat 还需要 `sysstat`。容器运行时需能获取负载镜像，CPU 参考采集、cpuidle 和 dload 还需要 Docker。

CPU 指标支持 cgroup v1/v2；runqlat 和 cpuidle 需要相应内核采样能力。dload 功能检查需要 Legacy cgroup、taskstats、dm-delay、`dmsetup`、`losetup`、`mkfs.ext4`、`modprobe`、`nsenter`、挂载权限和可用 loop 设备。其他 cgroup 模式报告不支持及跳过原因。

### Apiserver

```bash
sudo bash integration/run.sh test_apiserver_profile_disabled.sh
sudo bash integration/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling_matrix.sh
```

集成测试覆盖认证、能力、参数和异常请求；E2E 覆盖采样结果、分页、火焰图及语言/采样模式矩阵。停止、并发和恢复场景可分别运行 `test_apiserver_profiling_{stop,concurrent,recovery,node_recovery}.sh`。

无存储 API 检查不需要 Docker、perf 或编译器。采样测试需要 Docker、curl、jq、ss、timeout、允许采样的 perf 配置及已构建的采集工具；宿主机原生负载还需要 gcc。

矩阵测试还需要 Python 3、ip、kubectl 管理权限、crictl、本机 kubelet 凭证和 asprof/py-spy。`APISERVER_TEST_NODE` 可指定本机节点；未指定时自动匹配，无法唯一匹配则失败。

默认镜像为 `gcc:14`、`golang:1.24`、`eclipse-temurin:17-jdk`、`python:3.11-slim`，可通过对应的 `APISERVER_TEST_*_IMAGE` 变量替换。运行时需要镜像访问权限。工作目录中的 `summary.tsv` 记录矩阵各组合的结果。

### 性能剖析

```bash
sudo bash integration/run.sh test_profiler_native_cpu.sh
sudo bash integration/run.sh test_profiler_native_cpu_offcpu.sh
sudo bash integration/run.sh test_profiler_native_memory_virtual_alloc.sh
sudo bash integration/run.sh test_profiler_native_memory_physical_usage.sh
sudo bash integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo bash integration/run.sh test_profiler_java_memory_usage_alloc.sh
sudo bash integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

Java/Python 用例需在 `PROFILER_TOOL_DIR` 中准备对应采集工具。容器、线程组和指定 CPU 的测试分别为 `test_profiler_native_cpu_container.sh`、`test_profiler_native_cpu_thread_group.sh`、`test_profiler_native_cpu_cpuid.sh`。

### 存储与资源限额

以下存储测试不需要 Elasticsearch 服务：

| 用例 | 验证内容 |
| --- | --- |
| `test_basic_storage_elasticsearch_config.sh` | Elasticsearch 配置完整性和启动行为 |
| `test_basic_storage_localfile_rotation.sh` | LocalFile 轮转、保留数量和事件数据 |

LocalFile 轮转测试需要内核 BTF、`skb/kfree_skb` tracepoint 及已构建的 Dropwatch 工具。

```bash
sudo bash integration/run.sh test_node_runtime_limits.sh
```

资源限额测试验证启动和运行阶段的 CPU、内存配置，需要可写的 CPU、memory 控制器；cgroup v2 还需要可访问的 systemd。分别在 v1/hybrid 和 v2 环境运行，才能确认两种层级的覆盖。环境被占用或能力不足时报告 SKIP；该测试不验证 CPU 吞吐或 OOM 行为。

### IO

在具备 BPF/BTF 的专用 Linux 测试节点运行。IO 内核测试需要 Go；IOCOST 还需要 BPF 编译工具链及可写的 `io.cost` 或 `blkio.cost` 控制项：

```bash
sudo bash integration/run.sh test_iocost.sh
sudo env TEST_IOCOST_MODE=pressure bash integration/run.sh test_iocost.sh
```

`TEST_IOCOST_MODE` 支持 `functional`、`lifecycle`、`faults`、`pressure`、`compat`，默认 `all`。IO 测试优先使用临时测试盘；无临时盘时对合适的已有磁盘只做只读检查，没有合适磁盘则跳过。

真实 NVMe/SCSI 健康取证需要显式指定非系统专用设备：

```bash
sudo env TEST_IO_CONFIRM_NON_SYSTEM=1 TEST_NVME_DEVICE=nvme0 \
  bash integration/run.sh test_io_kernel.sh
```

SCSI 使用 `TEST_SCSI_DEVICE=sdb`。NVMe 和 SCSI 分别需要 `nvme-cli`、`smartmontools`。

### SchedBlame

```bash
sudo bash integration/run.sh test_sched_blame.sh
```

需要隔离的 Linux 4.18–6.5 测试环境、BTF、调度和 cgroup 钩子、本机 Kubernetes 节点、kubelet 认证及已缓存的 `BUSINESS_POD_IMAGE`。不支持的内核或缺失能力报告 SKIP。

成功日志为 `sched-blame live collection verified`；失败时工作目录保留 CSV、命令日志和 Pod 诊断信息。该测试验证真实采集和竞争场景，不证明精确资源记账。
