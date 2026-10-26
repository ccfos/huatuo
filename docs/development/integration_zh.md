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

### 网卡硬件丢包指标

两个用例要求 root 及设备管理所需的内核权限。
在具备 `netdevsim` 和 `carrier_down_count_show` 探针的主机运行
`sudo bash integration/run.sh test_metrics_netdev_hw.sh`。用例使用独立模拟设备，
不选择或修改物理网卡。缺少内核能力时明确跳过；跳过不代表已经验证内核支持
或物理驱动兼容性。

### RDMA 链路指标

安装 `rdma` 工具后运行 `sudo bash integration/run.sh test_metrics_netdev_rdma_link.sh`。
用例优先只读现有硬件，其次尝试现有软件 RDMA；都无法提供统计时，在独立
veth 对上尝试 `rdma_rxe`。缺少 RDMA 能力时明确跳过，不记为通过；选定设备后，
启动或指标错误使测试失败。

已有设备保持只读，不产生物理 RDMA 流量。两个用例先停止 daemon，再删除
测试自建设备；若 daemon 仍存活，则保留设备并报告失败，避免注销仍在使用的
资源。其他清理失败也会使测试失败。共享模块保持加载。

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
