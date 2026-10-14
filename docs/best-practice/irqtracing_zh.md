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
  --duration 5 \
  --output-format collapsed --output-path irqtracing.folded
```

`--target-cpu` 可选，默认值为 `-1`，表示跟踪系统中的全部 CPU。工具只挂载 `irq/softirq_raise` / `irq/softirq_entry` tracepoint；全量模式聚合所有 CPU 的 source 和直接被抢占的 victim，指定非负 CPU 时只保留该 CPU 的数据。两种模式都采集 `--duration` 秒（默认 3）。

`--max-events-per-second-per-cpu N` 可选，用于限制每个被跟踪 CPU 每秒采集的 source 和 victim 栈样本总数。省略该参数或设置为 `0` 时不限速；启用时 `N` 必须在 2 到 8589934590 之间。每 CPU 额度在 `softirq_raise` 和 `softirq_entry` 之间尽量均分；奇数额度多出的一个样本分给 raise。全 CPU 模式下，系统总额度随被跟踪 CPU 数量增长。

必须显式同时指定 `--output-path` 和 `--output-format`。支持 `collapsed`（帮助中显示的默认值）、`flamegraph`、`svg`、`json`、`remote-store`。本地结果写入指定文件、目录或 `stdout`。危险使用提醒：终端、重定向或日志捕获可能暴露采集数据，请谨慎使用 stdout。不存在的路径默认作为新文件，以 `/` 结尾则创建目录。`collapsed`
输出可供火焰图工具使用的 folded stack：

```text
source;source[raiser,NET_RX];raise_softirq_[k] 12
victim;victim[worker(123),NET_RX];work 4
```

使用 `--output-format flamegraph` 或 `--output-format svg` 可直接输出 SVG 火焰图：

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format flamegraph --output-path irqtracing.svg
```

公共采集框架在窗口存在丢样时统一记录警告，包含任务 ID 和各类丢样数量。IRQ 的 warning 及以上日志写入 stderr，采集结果写入指定目标；诊断日志写入失败不会阻止数据导出。

需要上传标准 profile 和丢样计数时，使用 `remote-store`，指定 toolstream Unix socket 和唯一任务 ID：

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format remote-store --output-path /var/run/huatuo-toolstream.sock \
  --task-id irq-demo-1
```

使用 `--output-format json --output-path irqtracing.json` 可将相同的窗口数据保存到本地。JSON 内容：

```json
{
  "profile_type": "irqtracing:irq:count:irq:count",
  "profile": {
    "time_nanos": 1790000000000000000,
    "duration_nanos": 3000000000,
    "sample_type": [{"type": 1, "unit": 2}],
    "string_table": ["", "irq", "count"]
  },
  "dropped_samples": {"collection": 1234}
}
```

`profile` 与 profiler 工具使用相同的 `types.ProfilingWindow` 格式。示例为空窗口；有样本的窗口还包含 sample、location 和 function 数据。没有样本时仍返回有效 profile，保留开始时间和实际采集时长。调用栈保留 `source` / `victim` 根及进程、中断向量分组，其后是用户态和带 `_[k]` 后缀的内核态栈帧。指定的目标 CPU 同时写入样本标签。

`dropped_samples.collection` 记录内核采样预算或 counts map 已满导致的丢样总数，当前计数器无法区分两种原因。非零表示 profile 不完整，并在 stderr 输出告警；读取计数器失败会使采集失败。profiler 用户态队列溢出使用独立的 `dropped_samples.aggregation_queue` 原因。

## 由 huatuo-bamai 调用

守护进程的 `AutoTracing.IRQTracing` tracer 在其规则检测到某个 CPU 出现 irq/softirq 突增或持续高利用率时，会调用守护进程可执行文件旁的 `irqtracing` 二进制（`CoreBinDir` 由守护进程自身路径推导；源码构建树中即 `_output/bin/irqtracing`），传入 `--bpf-path <CoreBpfDir>/irqtracing.o`、`--target-cpu <cpu>`、`--duration <RunTracingToolTimeout>` 和 `--max-events-per-second-per-cpu <MaxEventsPerSecond>`。`MaxEventsPerSecond` 默认是 1000，因此 raise 和 entry 默认各限制为 500/s。CLI 接入公共 profiler runner 和单窗口 Pipeline，通过 Toolstream 发送 `types.ProfilingWindow`。`internal/profiling.Register` 注册的处理器调用 `pkg/profiling/store.SaveSync` 保存窗口，同一处理器在提交请求无错误时再调用 `Store.Commit` 写入持久化完成标记。只有 profile 已可查询且发布成功，采集任务才成功。该路径要求配置 Elasticsearch，仅配置 tracing 本地文件存储不足以保存 IRQ profile。触发规则及命中 CPU 利用率保留在守护进程日志中。

新的 IRQ 结果写入 profiling 存储的 `profile_data`，`tracer_name=irqtracing`、`tracer_type=profiling`，可通过 profiling API 按 `irqtracing:irq:count:irq:count` 查询。历史 tracing 文档继续通过原 tracing 查询读取，不改写历史数据。旧 `flamedata`/`nmissed` 传输格式已移除，CLI 与守护进程需要同时升级。

远端输出成功后发送 `end + commit`，并等待 daemon 返回存储和发布结果 ACK。普通 End、采集失败和断连均不发布任务完成标记。协议版本 3 要求工具与 daemon 同时升级；ACK 丢失时结果可能已发布，应按任务 ID 查询确认。
