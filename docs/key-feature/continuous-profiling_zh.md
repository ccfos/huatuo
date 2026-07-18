---
title: 持续性能剖析
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

{{% alert color="info" title="🎯 关于 HUATUO（华佗）" %}}
<div style="text-align: left;">
HUATUO（华佗）是由滴滴开源并依托 CCF（中国计算机学会）孵化的操作系统深度观测项目，广泛应用于AI 计算、AI 沙箱、云原生通用计算、云服务、基础架构服务等场景。
</div>
{{% /alert %}}

## 📖 功能概述

`profiler` 是 HUATUO 提供的独立性能剖析命令行工具。它可以直接对宿主机进程或容器内进程采样，不依赖 huatuo-apiserver、Elasticsearch 或 Grafana。工具支持 C、C++、Go、Java 和 Python 进程，并将调用栈输出为折叠栈或 SVG 火焰图。

C、C++ 和 Go 使用基于 eBPF 的原生采集器，可观测 CPU、虚拟内存分配、物理内存分配和物理内存驻留。Java 通过 async-profiler 观测 CPU、对象分配和存活对象；Python 通过 py-spy 观测 CPU。采集结果适合用于热点函数定位、内存增长归因、容器内进程分析和性能问题现场留存。

本文仅介绍 `_output/bin/profiler` 的独立使用方式。通过 apiserver 创建任务并在 Grafana 查询的服务化持续 Profiling 不在本文范围内。

## 🎯 应用场景

### 1. CPU 热点与调用路径定位

对 C、C++、Go、Java 或 Python 进程按固定频率采样调用栈，通过栈宽度判断 CPU 时间的主要消耗路径。原生采集器还可以使用 `--cpuid` 将采样限定到指定 CPU，以分析绑核任务或局部 CPU 热点。

### 2. 原生进程内存归因

对 C、C++ 和 Go 进程分别观测虚拟地址空间分配、物理页分配和当前物理页驻留。三种模式区分“申请了多少地址空间”“实际分配了多少物理页”和“当前仍驻留多少物理页”，用于定位 `mmap`、缺页分配和常驻内存增长的调用路径。

### 3. JVM 对象分配与存活对象分析

通过 async-profiler 采集 Java 对象分配或存活对象调用栈。对象分配适合定位高分配速率和 GC 压力来源；存活对象适合分析采集窗口内仍被引用的对象及其分配路径。

### 4. 容器与多进程任务分析

通过容器 ID 自动解析容器内目标进程，适合 Docker 和 containerd 工作负载。Java 和 Python 还支持逗号分隔的多个 PID，并可限制同时运行的采集子进程数量，适用于同一服务的多实例或父子进程分析。

## 🚀 功能使用

### 1. 构建与运行条件

在仓库根目录构建完整产物：

```bash
make all
```

生成的命令位于 `_output/bin/profiler`。原生采集依赖 Linux eBPF、perf event 和仓库构建出的 BPF 对象，通常需要 root 权限，并要求 `kernel.perf_event_paranoid` 允许采样。Java 需要 async-profiler，`--tool-path` 指向包含 `bin/asprof` 和 `lib/libasyncProfiler.so` 的目录。Python 需要 py-spy，`--tool-path` 指向包含可执行文件 `py-spy` 的目录。

查看当前版本的完整帮助：

```bash
_output/bin/profiler --help
```

命令的基本结构如下：

```bash
sudo _output/bin/profiler \
  --type <cpu|memory> \
  --language <c|c++|go|java|python> \
  --pid <pid> \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles
```

`--type` 和 `--language` 为必填参数。Java、Python 和原生内存采集必须在 `--pid` 与 `--container-id` 中指定且仅指定一个目标；原生 CPU 采集未指定目标时可进行宿主机级采样。

### 2. 通用命令参数

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--type`, `-t` | 无 | 全部 | 观测类型：`cpu` 或 `memory`，必填 |
| `--language`, `-l` | 无 | 全部 | 目标语言：`c`、`c++`、`go`、`java` 或 `python`，必填 |
| `--pid`, `-p` | 无 | 全部 | 目标 PID；Java、Python 可使用逗号分隔多个 PID，原生采集最多一个 PID |
| `--container-id` | 无 | 全部 | 目标容器 ID；不能与 `--pid` 同时使用 |
| `--duration`, `-d` | `10` | 全部 | 总采集时长，单位为秒，最小为 1 |
| `--aggr-interval` | `10` | 全部 | 聚合周期，单位为秒，不得大于采集时长 |
| `--freq`, `-F` | `99` | CPU | 每秒采样次数；Java 最大为 1000 |
| `--output-path` | `.` | 本地输出 | 输出目录，不是输出文件名 |
| `--output-format` | `collapsed` | 全部 | `collapsed`、`flamegraph`、`svg` 或 `remote` |
| `--output-storage` | `/var/run/huatuo-toolstream.sock` | `remote` | 远端上传使用的 Unix socket |
| `--max-concurrent-procs` | `0` | Java、Python | 并发采集子进程上限；`0` 表示不限制 |
| `--tool-path` | 无 | Java、Python | 第三方采集工具根目录，必填 |
| `--binary-match-path` | 无 | Java、Python | 按可执行文件路径匹配容器内目标进程 |
| `--huatuo-api-address` | `127.0.0.1:19704` | 容器目标 | 用于解析容器元数据的 HUATUO API 地址 |
| `--tracer-id` | 自动生成 | 全部 | 采集任务 ID，主要用于远端存储关联 |
| `--enable-pprof` | `false` | 工具自身 | 在 `:6000` 暴露 profiler 进程自身的 Go pprof 接口 |
| `--version-format` | `text` | 版本查询 | `--version` 的输出格式：`text`、`json` 或 `short` |
| `--help`, `-h` | - | 全部 | 显示命令帮助 |
| `--version`, `-v` | - | 全部 | 显示版本与构建信息 |

原生采集专用参数：

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--memory-mode` | 无 | 原生内存、Java 内存 | 内存观测维度；使用 `--type memory` 时必填 |
| `--cpuid` | 全部 CPU | 原生 CPU | CPU 列表或范围，例如 `1,3,5-10` |
| `--thread-group` | `false` | 原生 | 同时采集目标 PID 所在线程组中的其他线程 |
| `--physical-memory-probability` | `100` | 原生物理内存 | 物理内存事件采样概率，范围为 1～100 |
| `--log-bpf-debug` | `false` | 原生 | 输出 BPF 调试事件，常规采集不建议启用 |

日志参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--log-level` | `error` | `trace`、`debug`、`info`、`warn` 或 `error` |
| `--log-file` | `stdout` | 日志文件路径，或 `stdout` |
| `--log-size` | `100` | 日志轮转大小，单位 MB；`0` 表示不轮转，仅用于文件输出 |
| `--verbose` | `false` | 等价于 `--log-level debug --log-file stdout`，并覆盖显式日志设置 |

### 3. C、C++ 和 Go 观测

C、C++ 和 Go 均使用原生 eBPF 采集器，命令只需替换 `--language`。CPU 模式按采样次数统计调用栈宽度，同时包含可解析的用户态栈和内核态栈。

```bash
sudo _output/bin/profiler \
  --type cpu \
  --language go \
  --pid 12345 \
  --duration 30 \
  --aggr-interval 10 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/go-cpu
```

如需包含同一进程的工作线程，增加 `--thread-group`。如需限定 CPU，增加 `--cpuid 2,4-7`。原生 CPU 也支持容器和宿主机级采样：

```bash
# 采集指定容器
sudo _output/bin/profiler \
  --type cpu --language c --container-id <container-id> \
  --duration 30 --aggr-interval 10 \
  --output-format collapsed --output-path ./profiles/container

# 不指定 PID 或容器，采集宿主机
sudo _output/bin/profiler \
  --type cpu --language c \
  --duration 30 --aggr-interval 10 \
  --output-format flamegraph --output-path ./profiles/host
```

原生内存支持以下维度：

| `--memory-mode` | 统计内容 | 适用问题 |
| --- | --- | --- |
| `virtual_alloc` | 虚拟地址空间分配量及其调用栈 | `mmap` 等虚拟内存申请过多、地址空间增长 |
| `physical_alloc` | 采集窗口内新分配的物理内存量 | 缺页触发的物理页分配热点、分配速率分析 |
| `physical_usage` | 采集时仍驻留的物理内存量 | 常驻内存来源、物理页未释放路径 |

```bash
sudo _output/bin/profiler \
  --type memory \
  --language c++ \
  --memory-mode physical_usage \
  --pid 12345 \
  --thread-group \
  --physical-memory-probability 100 \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles/native-memory
```

`--physical-memory-probability` 仅适用于 `physical_alloc` 和 `physical_usage`。降低该值可减少高频内存事件的处理量，但火焰图中的值由采样事件估算，不再是逐事件统计。

### 4. Java 观测

Java CPU 采集依赖 async-profiler。单 PID、容器和多 PID 均可使用：

```bash
_output/bin/profiler \
  --type cpu \
  --language java \
  --pid 12345,12346 \
  --tool-path /opt/async-profiler \
  --max-concurrent-procs 2 \
  --duration 30 \
  --aggr-interval 10 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/java-cpu
```

Java 内存支持两个维度：

| `--memory-mode` | 统计内容 | 适用问题 |
| --- | --- | --- |
| `object_alloc` | 采集窗口内的对象分配及分配调用栈 | 高分配速率、短命对象和 GC 压力来源 |
| `object_usage` | 存活对象及其分配调用栈 | 长生命周期对象、堆占用来源和疑似内存泄漏 |

```bash
_output/bin/profiler \
  --type memory \
  --language java \
  --memory-mode object_usage \
  --pid 12345 \
  --tool-path /opt/async-profiler \
  --duration 30 \
  --aggr-interval 10 \
  --output-format flamegraph \
  --output-path ./profiles/java-memory
```

使用容器 ID 时，将 `--pid` 替换为 `--container-id <container-id>`。如果容器内存在多个候选进程，可通过 `--binary-match-path` 指定目标可执行文件路径。

### 5. Python 观测

Python 当前仅支持 CPU 观测。`--aggr-interval` 必须与 `--duration` 相等，即一次采集只生成一个聚合窗口。`--tool-path` 指向包含 `py-spy` 的目录。

```bash
_output/bin/profiler \
  --type cpu \
  --language python \
  --pid 12345,12346 \
  --tool-path /opt/py-spy \
  --max-concurrent-procs 2 \
  --duration 30 \
  --aggr-interval 30 \
  --freq 99 \
  --output-format flamegraph \
  --output-path ./profiles/python-cpu
```

Python 不支持 `--type memory`。若需要 Python 内存分析，应使用独立的内存分析工具；当前 `profiler` 命令不会调用 memray 生成 Python 内存结果。

### 6. 火焰图与输出格式选择

| 格式 | 生成内容 | 选择建议 |
| --- | --- | --- |
| `collapsed` | `perf_<Unix 时间戳>.folded`；每行是以分号分隔的调用栈及末尾计数 | 用于脚本检索、结果比较，或交给其他火焰图工具二次渲染 |
| `flamegraph` | `flamegraph_<Unix 时间戳>.svg`；内嵌交互脚本的 SVG | 默认的人工分析格式，可在浏览器中搜索、缩放和查看栈帧数值 |
| `svg` | 与 `flamegraph` 相同的交互式 SVG | 兼容显式要求 SVG 的调用方；当前实现与 `flamegraph` 等价 |
| `remote` | 不生成本地火焰图，通过 Unix socket 上传 pprof 兼容数据 | 接入 HUATUO 存储链路时使用，不适合离线查看 |

火焰图从下到上表示调用方向，矩形宽度表示该调用栈在当前观测维度中的累计值。不同类型的宽度含义不同：CPU 表示采样次数折算的 CPU 时间占比；内存模式表示相应的虚拟分配、物理分配、物理驻留、Java 对象分配或存活对象量。横向位置不表示时间先后。

折叠栈示例：

```text
main;handleRequest;parsePayload 428
main;handleRequest;writeResponse 172
```

需要保留原始数据并支持后续使用不同配色或过滤规则重新渲染时，选择 `collapsed`。只需直接定位热点时，选择 `flamegraph`。`remote` 依赖 HUATUO toolstream Unix socket，独立离线使用时不应选择该格式。

### 7. 根据集成测试复现

仓库集成测试提供了可执行的端到端示例。测试会创建目标进程、运行 profiler，并校验输出中的预期调用栈：

```bash
# 原生 CPU
sudo ./integration/run.sh test_profiler_native_cpu.sh

# 原生虚拟内存与物理内存
sudo ./integration/run.sh test_profiler_native_mem_virtual_alloc.sh
sudo ./integration/run.sh test_profiler_native_mem_physical_usage.sh

# Java CPU 与内存
sudo ./integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo ./integration/run.sh test_profiler_java_memory_usage_alloc.sh

# Python 多进程 CPU
sudo ./integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

容器、线程组和指定 CPU 的示例分别位于 `test_profiler_native_cpu_container.sh`、`test_profiler_native_cpu_thread_group.sh` 和 `test_profiler_native_cpu_cpuid.sh`。运行前需完成 `make all`，并根据 `integration/env.sh` 配置 Java 或 Python 采集工具路径。

## ⚙️ 功能原理介绍

`profiler` 先根据语言和观测类型选择采集器。原生 CPU 采集器将 eBPF 程序挂载到 perf event，原生内存采集器通过内核事件记录分配与释放路径；Java 和 Python 采集器分别启动 async-profiler 和 py-spy 子进程。采集记录进入统一聚合流水线，按调用栈合并计数，最后写入本地文件或上传远端存储。

```mermaid
flowchart LR
    CLI[profiler 命令参数] --> Select{语言与观测类型}
    Select -->|C/C++/Go| Native[eBPF 原生采集器]
    Select -->|Java| Java[async-profiler]
    Select -->|Python| Python[py-spy]
    Native --> Queue[采样记录队列]
    Java --> Queue
    Python --> Queue
    Queue --> Aggregate[按调用栈聚合]
    Aggregate --> Folded[collapsed 折叠栈]
    Aggregate --> SVG[交互式 SVG 火焰图]
    Aggregate --> Remote[Unix socket 远端上传]
```

`--duration` 控制采集生命周期，`--aggr-interval` 控制远端上传的快照周期。本地 `collapsed`、`flamegraph` 和 `svg` 在采集结束时写出最终聚合结果；`remote` 按聚合周期生成并上传快照。队列将采集与符号化、聚合和输出解耦，避免文件渲染阻塞采样路径。

## 🌟 结尾

{{% alert color="info" %}}
<div style="text-align: left;">
🌟 欢迎 Star: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
<br><br>
👀 欢迎订阅官方微信公众号<br>
<img src="/img/contact-weixin.png" alt="微信公众号二维码" style="max-width: 200px; margin-top: 10px;">
</div>
{{% /alert %}}
