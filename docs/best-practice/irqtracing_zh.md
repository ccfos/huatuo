---
title: IRQ 追踪
type: docs
description: ""
author: HUATUO Team
date: 2026-09-24
weight: 6
---

独立命令采集系统全部 CPU 或指定 CPU 的 irq/softirq 栈，并生成火焰图：其中 sources 表示发起 softirq 的来源，victims 表示被 softirq 直接抢占的任务。由 ksoftirqd 处理 softirq 时，ksoftirqd 是执行者而不是 victim，因此不会记录为 victim。`huatuo-bamai` 守护进程的 `AutoTracing.IRQTracing` tracer 正是通过 shell 调用这一工具；直接运行它即可得到相同的结果，无需任何存储后端或配置文件。

## 构建

常规项目构建会自动发现 `cmd/irqtracing` 并编译 `bpf/irqtracing.c`：

```bash
make build
```

命令输出到 `_output/bin/irqtracing`，BPF 对象输出到 `_output/bpf/irqtracing.o`；运行时需要通过 `--bpf-path` 指定该对象。

## 运行

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 > irqtracing.folded
```

`--target-cpu` 可选，默认值为 `-1`，表示跟踪系统中的全部 CPU。工具只挂载 `irq/softirq_raise` / `irq/softirq_entry` tracepoint；全量模式聚合所有 CPU 的 source 和直接被抢占的 victim，指定非负 CPU 时只保留该 CPU 的数据。两种模式都采集 `--duration` 秒（默认 3）。

`--max-events-per-second-per-cpu N` 可选，用于限制每个被跟踪 CPU 每秒采集的 source 和 victim 栈样本总数。省略该参数或设置为 `0` 时不限速；启用时 `N` 必须在 2 到 8589934590 之间。每 CPU 额度在 `softirq_raise` 和 `softirq_entry` 之间尽量均分；奇数额度多出的一个样本分给 raise。全 CPU 模式下，系统总额度随被跟踪 CPU 数量增长。

本地结果写入 stdout。`--output text` 是默认值，也是 `collapsed` 的兼容别名，
输出可供火焰图工具使用的 folded stack：

```text
source;source[raiser,NET_RX];raise_softirq_[k] 12
victim;victim[worker(123),NET_RX];work 4
```

使用 `--output flamegraph` 或 `--output svg` 可直接输出 SVG 火焰图：

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output flamegraph > irqtracing.svg
```

需要平台 profile 数据和丢样计数时使用 `--output json`：

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output json > irqtracing.json
```

JSON 内容：

```json
{
  "flamedata": { "...": "profile 树，未采集到任何数据时为 null" },
  "nmissed": 1234
}
```

`flamedata` 与平台消费的 profile 格式一致（`ProfileType` 为 `irqtracing:irq:count:irq:count`）。每条栈以 `source` 或 `victim` 为根，后接 `source[comm,VEC]` 或 `victim[comm(pid),VEC]` 标签帧，再依次是用户态栈帧和带 `_[k]` 后缀的内核态栈帧。`VEC` 取值为 `HI`、`TIMER`、`NET_TX`、`NET_RX`、`BLOCK`、`IRQ_POLL`、`TASKLET`、`SCHED`、`HRTIMER`、`RCU` 之一，未知向量显示为 `VEC<n>`。

`nmissed` 是采集窗口内丢弃的样本总数。样本丢弃要么来自 `--max-events-per-second-per-cpu` 启用的 first-N 预算，要么来自 counts 映射已满——两者都意味着火焰图不完整。当丢弃计数无法读取时，工具会直接失败而不是写出结果。非零的 `nmissed` 同时会在 stderr 输出一条告警；JSON 字段是守护进程消费的持久化契约。

`--output-storage <socket>` 会通过 Toolstream 发送 JSON，而不在本地输出，且
必须同时指定 `--task-id`；此时显式指定的 `--output` 会被忽略。运行日志被
丢弃，本地结果写入 stdout，失败信息和丢样告警写入 stderr。

进程在以下情况退出：采集时长到达、调用方取消、或收到 `SIGHUP`、`SIGQUIT`、`SIGINT`、`SIGTERM` 信号。

## 由 huatuo-bamai 调用

守护进程的 `AutoTracing.IRQTracing` tracer 在其规则检测到某个 CPU 出现 irq/softirq 突增或持续高利用率时，会调用守护进程可执行文件旁的 `irqtracing` 二进制（`CoreBinDir` 由守护进程自身路径推导；源码构建树中即 `_output/bin/irqtracing`），传入 `--bpf-path <CoreBpfDir>/irqtracing.o`、`--target-cpu <cpu>`、`--duration <RunTracingToolTimeout>` 和 `--max-events-per-second-per-cpu <MaxEventsPerSecond>`。`MaxEventsPerSecond` 默认是 1000，因此 raise 和 entry 默认各限制为 500/s。CLI 通过 Toolstream 返回结果并合并进保存的 tracing 数据；守护进程另外记录 `rule`、`trigger_cpu`、`trace_duration` 和 `hit_cpus`。
