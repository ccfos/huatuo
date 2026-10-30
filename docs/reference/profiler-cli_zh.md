---
title: profiler 命令行参数
type: docs
author: HUATUO Team
date: 2026-10-21
weight: 6
---

本文列出 `profiler` 的参数、默认值和适用范围。构建条件与采集示例见[命令行采集](/docs/best-practice/continuous-profiling_zh.md#命令行采集)，格式选择见[输出格式](/docs/best-practice/continuous-profiling_zh.md#输出格式)。

`--output-path` 和 `--output-format` 都必须显式传入。`--output-format` 的声明默认值为 `collapsed`，但不能依靠该默认值省略参数。

## 通用参数

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--type`, `-t` | 无 | 全部 | 观测类型：`cpu` 或 `memory`，必填 |
| `--language`, `-l` | 无 | 全部 | 目标语言：`c`、`c++`、`go`、`java` 或 `python`，必填 |
| `--pid`, `-p` | 无 | 全部 | 目标 PID；Java、Python 可使用逗号分隔多个 PID，原生采集最多一个 PID |
| `--container-id` | 无 | 全部 | 目标容器 ID；原生 CPU 采集可与 `--pid`（宿主机 PID）组合，两个过滤条件同时生效；其他采集类型与 `--pid` 互斥 |
| `--duration`, `-d` | `10` | 全部 | 总采集时长，单位为秒，最小为 1 |
| `--aggr-interval` | `10` | 全部 | 聚合周期，单位为秒，不得大于采集时长 |
| `--freq`, `-F` | `99` | CPU | 每秒采样次数；Java 最大为 1000 |
| `--output-path` | 必填 | 全部 | 本地文件、目录或 `stdout`，`remote-store` 时为 Unix socket；新目录路径以 `/` 结尾 |
| `--output-format` | `collapsed` | 全部 | `collapsed`、`flamegraph`、`svg`、`json` 或 `remote-store`；必须与 `--output-path` 一起显式指定 |
| `--max-concurrent-procs` | `0` | Java、Python | 并发采集子进程上限；`0` 表示不限制 |
| `--tool-path` | 无 | Java、Python | Java/Python CPU 必填；Python 内存默认使用可执行文件旁的 `../tools/memray`，此参数可指定其他包根目录（相对路径以 profiler 而非目标进程的工作目录为基准） |
| `--binary-match-path` | 无 | Java、Python | 按可执行文件路径匹配容器内目标进程 |
| `--huatuo-api-address` | `127.0.0.1:19704` | 容器目标 | 用于解析容器元数据的 HUATUO API 地址 |
| `--task-id` | 空；本地输出时内部生成 | 全部；`remote-store` 必填 | toolstream 和远端存储共用的稳定采集任务 ID |
| `--enable-pprof` | `false` | 工具自身 | 在 `127.0.0.1:6000` 暴露工具自身的 Go runtime profile |
| `--version-format` | `text` | 版本查询 | `--version` 的输出格式：`text`、`json` 或 `short` |
| `--help`, `-h` | - | 全部 | 显示命令帮助 |
| `--version`, `-v` | - | 全部 | 显示版本与构建信息 |

这些 pprof 接口描述 profiler 进程自身的 Go runtime，不是 `--pid` 指定的目标或 Java/Python 采集子进程。监听范围由所有网卡收紧为 loopback；端口被占用时启动失败。

## 采集参数

| 参数 | 默认值 | 适用范围 | 说明 |
| --- | --- | --- | --- |
| `--memory-mode` | Python 默认 `object_usage` | 内存 | 原生和 Java 内存必填；Python 仅支持存活分配模式 `object_usage` |
| `--python-memory-stack` | `python` | Python 内存 | 调用栈视图：`python`、`native` 或 `hybrid` |
| `--python-memory-merge-threads` | `false` | Python 内存 | 合并不同线程的分配 |
| `--cpuid` | 全部 CPU | 原生 CPU | CPU 列表或范围；off-CPU 样本按任务切出时所在 CPU 过滤 |
| `--cpu-mode` | `oncpu` | 原生 CPU | `oncpu` 按频率采样，`offcpu` 归因阻塞与可运行调度延迟 |
| `--require-hardware-pmu` | `false` | 原生 on-CPU | 强制使用硬件 PMU 采样；不可用时失败，不回退软件 CPU clock |
| `--offcpu-phase` | `all` | 原生 off-CPU | 累计 `all`、`blocked` 或 `runqueue` 时间 |
| `--offcpu-min-duration-us` | `1000` | 原生 off-CPU | 丢弃持续时间小于该微秒数的阶段 |
| `--offcpu-stats` | `false` | 原生 off-CPU | 收集 BPF 诊断统计；错误和清理路径会产生额外开销 |
| `--thread-group` | `false` | 原生 | 同时采集目标 PID 所在线程组中的其他线程 |
| `--physical-memory-probability` | `100` | 原生物理内存 | 物理内存事件采样概率，范围为 1～100 |
| `--log-bpf-debug` | `false` | 原生 | 输出 BPF 调试事件，常规采集不建议启用 |

Python 内存的 `python` 和 `hybrid` 视图仅按函数名显示 Python 帧，不包含文件或
分配行号。显示出的调用链相同时，不同文件中的同名函数、不同行的分配可能合并。

硬件 PMU 事件或采样不受支持时，命令退出码为 `1`，标准错误以 `Error: hardware_pmu_unavailable:` 标识该错误；权限及其他启动错误不使用此标识。

## 日志参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--log-level` | `error` | `trace`、`debug`、`info`、`warn` 或 `error` |
| `--log-file` | `stdout` | 日志文件路径，或 `stdout` |
| `--log-size` | `100` | 日志轮转大小，单位 MB；`0` 表示不轮转，仅用于文件输出 |
| `--verbose` | `false` | 等价于 `--log-level debug --log-file stdout`，并覆盖显式日志设置 |
