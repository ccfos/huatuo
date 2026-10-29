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

指定文件名可以只运行一个集成测试，第二个参数指定循环次数，默认为 1。
循环次数范围为 1–1,000，所选测试数量乘以循环次数不得超过 10,000；超限返回 2，
不创建运行目录或报告：

```bash
bash integration/run.sh test_metrics_exclude_filter.sh 10
```

或通过 Makefile 执行：
```bash
make integration
```

## CPU 指标与自动追踪 E2E

先执行 `make build`，再在测试节点以 root 运行单用例：

```bash
sudo bash e2e/run.sh test_metrics_cpu_util.sh
sudo bash e2e/run.sh test_metrics_cpu_stat.sh
sudo bash e2e/run.sh test_metrics_cpu_runqlat_tracing.sh
sudo bash e2e/run.sh test_autotracing_cpuidle.sh
sudo bash e2e/run.sh test_autotracing_dload.sh
```

这些用例也由 `make e2e` 和标准 `make test` 自动发现。CPU 用例串行运行，
基准环境为 4 核、8 GB 内存的 Kubernetes VM；需要可访问本机 kubelet API，
客户端证书路径通过 `KUBELET_CERT`、`KUBELET_KEY` 指定。

本地运行需安装 `stress-ng`；CPU util/stat 的 worker 参考采样还需要 `sysstat`。
VM 流程会安装这两个软件包。
Kubernetes 运行时需要能拉取负载镜像；CPU 参考采集、cpuidle 和 dload
还需要 Docker 及其测试镜像的访问权限。提前执行 `make build`，
测试运行时不需要 Go 编译器。

cpu_util、cpu_stat 和 runqlat 支持 cgroup v1/v2。控制项可用时，cpu_stat 会临时切换
`kernel.sched_schedstats` 并恢复原值；缺少该控制项只跳过开关和争抢子场景。
仅在内核导出相应计数时执行可选的 `wait_sum_percent` 和 burst 检查。
runqlat 要求对应调度 tracepoint 和
`free_fair_sched_group` kprobe 可用；cpuidle 要求可加载 perf BPF 程序。

dload 功能场景需要 Legacy cgroup、taskstats、dm-delay、`dmsetup`、
`losetup`、`mkfs.ext4`、`modprobe`、`nsenter`、挂载权限和可用 loop 设备。
其他 cgroup 模式先检查不支持时的行为，再报告 SKIP。
这些用例会创建 Pod 和临时块设备，请在专用测试节点运行。

缺少外部命令、内核 hook、设备能力或 kubelet 证书时，用例会报告带原因的 SKIP。
构建产物缺失、指标错误、异常退出或清理失败属于 FAIL。日志和原始参考数据保存在
`/tmp/huatuo-test.XXXXXX/<用例名>.<运行序号>.XXXXXX/`；E2E 成功后同样保留。
排查失败时，先查看该目录中的 `stdout.log`、`stderr.log` 和 `huatuo.log`。

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
结束时停止服务并检查日志。两套测试均保留每次执行的日志和工作文件；
资源清理仍会停止进程、删除容器和释放挂载。

每次用例执行（串行、并行、独占、E2E、重复运行）还拥有独立 mount namespace 和
`/var/run`，保留宿主运行时 socket，隔离 Huatuo PID 锁及 Toolstream socket。
服务准备、用例执行和清理都在同一个用例环境内完成。

新增测试时，在 `lib_runner.sh` 中分别维护 `runner_test_groups`（资源组）、
`runner_test_namespaces`（隔离需求）和 `runner_shared_storage_tests`（共享存储消费者）。
三者均以 `套件/文件名` 为键，调度前校验显式声明；未声明的测试默认独占执行并使用 mount 隔离。
单次执行由 `runner_worker.sh` 承接；报告由 `lib_runner_report.sh` 管理。
使用 profiler 专属 helper 的测试应先加载 `lib.sh`，再显式加载 `lib_profiler.sh`。

用例的额外隔离需求在 `integration/lib_runner.sh` 的 `runner_test_namespaces`
中声明，键为 `integration/test_*.sh` 或 `e2e/test_*.sh`：

| 声明 | 执行环境 |
| --- | --- |
| 未声明或 `mount` | 独立 mount，保留宿主 PID 和网络可见性 |
| `mount net` | 增加独立网络并启用 loopback；不保证宿主服务及代理可达 |
| `mount pid` | 增加独立 PID 和匹配的 procfs，执行入口负责等待子进程；退出 namespace 时剩余进程由内核终止 |

隔离声明与并发资源组独立。未知组合和无效用例名称在调度前失败；namespace 创建失败
记为 FAIL，不降级到宿主环境。明确缺失的命令（例如私有网络需要的 `ip`）记为 SKIP。
用例不再通过 `unshare` 重启自身；`lib_namespace.sh` 构造的客户端、服务端网络拓扑仍由
场景创建和清理。新增或修改隔离声明时应验证服务可达性、退出码及取消清理。

`test_tcpshark_output_cancellation_{blocked,healthy,timeout}.sh` 分别运行三个接收端场景，
共用 `lib_tcpshark_output_cancellation.sh`，每个场景都有全新的 `mount net` 环境。
用例目录中的 `execution.json` 是执行入口交给 runner 的内部清理结果；对外消费
`result.json`。执行入口被强制终止且无法提交结果时，无法确认的用例退出码为 `null`。

apiserver 测试遵循相同的平铺目录约定：

- `integration/test_apiserver_profile_disabled.sh`：能力表、认证和无存储 API。
- `integration/test_apiserver_profiling.sh`：主机/全局配额、Node 容量、参数校验、
  容器不存在、主机不可达和 Node 离线，共 7 项功能检查。
- `e2e/test_apiserver_profiling.sh`：任务完成、list/raw 分页、火焰图和 agent 指标。
- `e2e/test_apiserver_profiling_{stop,concurrent,recovery,node_recovery}.sh`：
  停止、同机并发、apiserver 重启恢复和 Node 故障恢复。
- `e2e/test_apiserver_profiling_matrix.sh`：19 个语言/采样模式组合。

公共服务配置、请求、断言和清理由 `integration/lib_apiserver.sh` 提供，
并复用 `integration/lib_storage.sh` 和 `integration/lib_continuous_profiling.sh`。
协议客户端和源码夹具位于 `integration/testdata/`；`make build` 提前构建
`_output/tests/apiserver-probe` 和静态 Go 负载 `apiserver-go-workload`。

```bash
make build
sudo bash integration/run.sh test_apiserver_profile_disabled.sh
sudo bash integration/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling_matrix.sh
# 上述脚本也由标准入口自动发现；支持脚本名后加重复次数
sudo make integration
sudo make e2e
```

静态 API 检查不依赖 Docker、perf 或编译器。采样检查需要 Linux、root、Docker、
curl、jq、ss、timeout、允许采样的 perf 配置及已构建的 BPF/profiler 工具。
宿主机负载用例还需要 gcc；矩阵在 Pod 内编译 C/C++，不需要宿主机编译器。
ES 数据绑定到工作目录的 `elasticsearch-data/`；报告包含请求响应、采样结果、日志及
`summary.tsv`。生成配置含测试 token，仅供测试环境使用。

矩阵还需要 Python 3、ip、kubectl 管理权限、crictl、本机 kubelet 凭证和已打包的
asprof/py-spy。
通过节点 InternalIP 自动匹配本机节点；`APISERVER_TEST_NODE` 可显式指定，仍须属于
本机。使用 `KUBELET_CERT`、`KUBELET_KEY` 覆盖凭证路径。无法唯一匹配节点时失败。
先通过本机 CRI 集中准备镜像，再以 `imagePullPolicy: Never` 启动 Pod；
下载失败保留独立日志。默认镜像为 gcc:14、golang:1.24、eclipse-temurin:17-jdk、
python:3.11-slim，可用 `APISERVER_TEST_{NATIVE,GO,JAVA,PYTHON}_IMAGE` 替换。

各语言 Pod 与存储启动并行准备，准备完成后保持空闲，收到启动信号才运行负载。
每次只激活一个负载并执行一个采样任务，依次覆盖 C/C++/Go 的 oncpu、offcpu 和三种
内存模式、Java CPU/两种内存模式及 Python CPU。Go Pod 只读挂载预编译静态负载；
C/C++ 和 Java 在对应运行时镜像中准备负载。原生负载保留 16 个 64 KiB 映射，
使最终快照仍有存活内存，并在收到 SIGTERM 后退出。
矩阵使用 2 秒聚合周期，原生任务采样 6 秒、Java 采样 8 秒，均至少检查 3 个结果窗口；
Python 采样 5 秒并输出一个窗口。其他 profiling 用例仍使用 5 秒聚合周期。
矩阵的 `summary.tsv` 逐项记录 19 个组合。

结果检查包括类型、容器归属、非空采样结果和正采样值火焰图；Python 只有整个任务
时长的采集窗口，不要求多个窗口。临时 namespace、ConfigMap、Pod 在退出时清理。
Node 故障恢复主动 SIGKILL 测试 Node，验证原任务失败及恢复后新任务可用。
故障期间允许预期错误日志，仍拒绝 runtime panic；不验证 Grafana 页面渲染。

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

`skip` 输出原因并以 77 退出，仍会执行 `EXIT` 清理。runner 将正常完成记为 PASS、
依赖不满足记为 SKIP、测试或清理失败记为 FAIL。失败后停止调度，等待正在执行的
用例收尾；未启动项和被 runner 取消的执行均记为 SKIP，由原因说明区别。
INT/TERM 中断整次运行时仍返回 130/143，不因用例计为 SKIP 而返回成功。

`skip` / `fatal` 的原因由调用它们的用例主 shell 提交。需要把子进程的失败或跳过
作为整个用例的结果时，使用 `run_test_step`；它执行外部命令，失败时立即以原始退出码
终止当前用例，并传播该步骤的原因。步骤脚本应在主 shell 中加载 `lib.sh` 后调用
`skip` / `fatal`；嵌套步骤继续使用同一 helper。

```bash
run_test_step bash -euc 'source "$ROOT_DIR/integration/lib.sh"; require_commands some_tool'
```

需要检查预期失败时，继续使用普通的 `if` / `wait` / `||`，不要用 `run_test_step`。
普通子进程或裸 subshell 的原因不自动提升为用例原因；若最终退出非零且没有显式原因，
报告保留实际退出码并提示查看日志。这避免把已经捕获的错误误报为最终失败原因。

每次有效调用（包括单用例）创建 `/tmp/huatuo-test.XXXXXX/`，终端在用例结束时
显示结果及原因，在运行结束时打印统计和 `result.json` 路径。报告依赖 `jq`；
无效参数不创建执行计划或报告。

```text
/tmp/huatuo-test.XXXXXX/
├── result.json
├── test_metrics.1.ABCDEF/
│   ├── stdout.log
│   ├── stderr.log
│   └── 配置、响应、服务日志等
├── shared-elasticsearch.1.GHIJKL/
└── ports/
```

用例目录名包含测试名、从 1 开始的执行序号和随机后缀。共享存储与端口目录按需
创建；共享实例使用每个用例独立的索引。全部资料保留在运行目录，可整体归档或删除。
stdout 与 stderr 分别保存，跨两个文件的先后顺序不作保证。

报告的 `schema_version` 当前为 1：

| 字段 | 含义 |
| --- | --- |
| `suite`、`runs_per_test` | 测试套件、每个测试计划执行的总次数 |
| `status` | 运行状态：`running`、`completed`、`aborted`、`interrupted` |
| `exit_code`、`reason` | runner 退出码和提前结束原因 |
| `started_at`、`finished_at` | 带时区的 RFC 3339 时间 |
| `duration_seconds` | 按单调时间计量的秒数 |
| `execution_counts` | `total`、`passed`、`failed`、`skipped`；总数包含未启动项 |
| `test_executions` | 每个测试每次计划执行的记录 |

执行记录包含 `test_name`、`run_index`、`result`、`exit_code`、`reason`、三个时间字段
及 `logs.stdout` / `logs.stderr`（相对于报告目录的路径）。最终 `result` 为
`passed`、`failed` 或 `skipped`；运行中未结束的记录为 `null`。未启动项的时间、
退出码和 `logs` 均为 `null`。准备或清理失败时，结果为 `failed`，退出码仍保留
测试进程实际值；进程未启动则为 `null`。最终三类计数之和等于 `total`。

报告随执行更新并原子替换。报告写入失败会停止调度、取消活动用例并继续资源清理，
runner 返回非零并标明报告不完整，不补造用例结果。写入失败、SIGKILL 或主机退出均
可能留下 `status: running` 的部分报告，不能把其中的计数当作最终结果。
收尾阶段收到 INT/TERM 时，保留已完成用例的结果，更新运行整体的中断状态和退出码。
读取跳过原因可以使用：

```bash
jq '.test_executions[] | select(.result == "skipped") | {test_name, run_index, reason}' /tmp/huatuo-test.XXXXXX/result.json
```

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

轮转用例需要内核 BTF、`skb/kfree_skb` tracepoint，以及 Dropwatch 二进制和 BPF 对象。它与后端一致性测试共用 UDP 发包和配置函数。调用 `write_config_bamai_storage_events` 时必须设置 `STORAGE_EVENT_RATE`、`STORAGE_ROTATION_MIB` 和 `STORAGE_MAX_ROTATED_FILES`。

框架在启动服务前统一隔离每次执行的 PID 锁和 Toolstream socket。配置、轮转和后端一致性测试在检查最终数据和日志前调用 `assert_huatuo_bamai_shutdown`，确认服务正常退出；`EXIT` trap 负责尽力清理。

## 后台进程清理

`stop_and_wait_by_pid PID [TIMEOUT]` 停止并等待子进程，返回其退出码。
未传入 PID 或 PID 为空时立即返回成功，调用方可以直接传入已初始化的 PID 变量。
等待结束后清空变量；尽力清理时使用 `|| true`，退出码属于测试契约时应进行断言。

## IO 测试

`make integration` 包含 IO 内核资格测试，覆盖延时和大小统计、blk-throttle 与
IOCOST 等待统计、设备及 cgroup 生命周期和健康取证。这些组件测试不替代
服务启动、配置及公开输出的黑盒验证。

在具备 BPF/BTF 的独立 Linux 测试环境以 root 权限运行。用例自动创建并
清理临时虚拟设备和 cgroup。单独运行 IOCOST：
`sudo bash integration/run.sh test_iocost.sh`。`TEST_IOCOST_MODE` 可选
`functional`、`lifecycle`、`faults`、`pressure`、`compat`，默认 `all`。

IO 测试优先通过 `scsi_debug` 创建测试盘；不可用时选择已有合格
SCSI/NVMe/virtio 磁盘，只做只读验证。没有合适磁盘时跳过相关检查。配置切换和
设备删除仅操作测试创建的磁盘。指标用例自行启动 Huatuo 验证输出。

IO health 使用真实 Linux 内核验证健康事件与取证结果。真实 NVMe/SCSI
取证命令需要显式指定专用测试设备，例如：

```bash
sudo env TEST_IO_CONFIRM_NON_SYSTEM=1 TEST_NVME_DEVICE=nvme0 \
  bash integration/run.sh test_io_kernel.sh
```

SCSI 使用 `TEST_SCSI_DEVICE=sdb`。这两类用例分别需要 `nvme-cli` 和
`smartmontools`。

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

### Daemon cgroup 资源限额

`test_node_runtime_limits.sh` 先验证默认启动继承测试进程的 cgroup。启用
`--enable-cgroup` 后，通过 `/proc/PID/cgroup` 和 `cgroup.procs` 验证实际归属，
读取启动、运行两阶段的 CPU 限额，并验证 `MemoryLimitMiB` 到字节的换算。
正常停止后修改三个 Runtime 配置项，重启并重复验证，同时检查退出状态、
daemon cgroup 删除及日志中没有 error/panic/fatal。
这里验证内核限额与配置一致，不测试 CPU 吞吐或 OOM 行为。

用例复制 fixture，仅将副本中的 XFS 设备文件替换为 FIFO。观察进程等待 XFS
打开该设备后检查启动限额，再写入 fixture 数据放行初始化。启动和观察进程完成
均有超时；同步点未被使用时测试失败。这依赖 XFS 构造函数在运行 CPU 限额生效前
同步执行。完整的 512 字节 fixture 一次写入：XFS 读完超级块就关闭 FIFO，
后续写入可能在初始化成功后失败。不增加生产代码测试钩子，也不修改真实设备文件。

在隔离的 Linux 测试 VM 中运行，需要可写的 CPU、memory 控制器。
同一套断言在 v1/hybrid 上读取 quota/period、memory 限额文件，在纯 v2 上读取
`cpu.max`/`memory.max`；v2 还需要可访问的 systemd manager。
主机级锁防止该用例并发运行；锁已占用、缺少能力或已有 bamai runtime cgroup 时
报告 SKIP。由 daemon 清理自身 cgroup；若仍有残留，则保留并使测试失败，
因为仅凭组名无法确认归属。
必须分别在两种层级上运行，才能确认 v1、v2 覆盖。用例禁用存储和 kubelet。

构建完成后，进入 integration 目录单独运行：

```bash
cd integration
```

```bash
bash run.sh test_node_runtime_limits.sh
```

## SchedBlame 真实采集

`test_sched_blame.sh` 运行已构建的 `_output/bin/sched-blame`，使用真实 BPF
对象、调度钩子、perf 传输和 kubelet 容器发现，不再用 Shell 包装 Go 组件测试，
也不注入伪造 perf 记录。

用例在本机节点创建两个名称唯一的 Pod，将负载绑定到同一个 CPU，依次采集
目标独占、存在竞争、竞争容器删除三个阶段，再删除并重建高亮目标。每个阶段
留出两秒稳定时间，随后至少获得五个运行时间非零的有效样本。检查目标身份、
dense ID、比例计算、目标替换后的历史清零，以及传输没有丢失。竞争阶段的外部
竞争比例应比前后两个安静阶段至少高五个百分点，不断言精确比例。SIGTERM
必须让命令成功退出，最后必须完成测试 namespace 的删除。

需要隔离的 Linux 4.18 至 6.5 环境、BTF、所需的调度和 cgroup 钩子、本机
Kubernetes 节点、可认证访问的 kubelet，以及已缓存的 `BUSINESS_POD_IMAGE`。
缺少前置条件时报告 SKIP；环境准备完成后，启动、采集、断言和清理失败均使
测试失败。较新的不支持内核会直接跳过，不加载 SchedBlame BPF。不根据容器
身份或根文件系统类型判断资格；满足前置条件的容器和 btrfs 主机不会因此跳过。

CI 构建完成后，可以单独运行：

```bash
sudo bash integration/run.sh test_sched_blame.sh
```

成功日志为 `sched-blame live collection verified`。失败时保留 CSV、阶段时间窗、
命令日志和 Pod 诊断信息。用例控制 CPU 负载，因此保持默认独占执行。CSV 断言
不等待 60 个样本的异常基线；精确归因、P99、异常数据、上传、丢失计数和最终
队列排空仍由普通 Go 组件测试验证。真实采集用例不证明精确记账，也不保证
dense ID 复用时的事件代次隔离；后者仍是设计接受的近似。
