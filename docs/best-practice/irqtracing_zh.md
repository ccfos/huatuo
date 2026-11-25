---
title: IRQ 追踪
type: docs
description: ""
author: HUATUO Team
date: 2026-09-24
weight: 6
---

`irqtracing` 采集全部或指定 CPU 上的 softirq 调用栈并生成火焰图。`source` 表示触发 softirq 的来源，`victim` 表示被 softirq 直接抢占的任务；由 ksoftirqd 处理时，ksoftirqd 是执行者，不记录为 victim。

工具可独立输出本地结果，也由 `huatuo-bamai` 的 `AutoTracing.IRQTracing` 调用。本地输出无需存储后端或配置文件。

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

## 输出

必须显式同时指定 `--output-path` 和 `--output-format`。支持 `collapsed`（帮助中显示的默认值）、`flamegraph`、`svg`、`json`、`remote-store`。本地结果写入指定文件、目录或 `stdout`。使用 `stdout` 时，终端、重定向或日志系统可能保留采集数据。不存在的路径默认作为新文件，以 `/` 结尾则创建目录。`collapsed`
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

### 远端存储

需要上传标准 profile 和丢样计数时，使用 `remote-store`，指定 toolstream Unix socket 和唯一任务 ID：

```bash
sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irqtracing.o \
  --duration 5 \
  --output-format remote-store --output-path /var/run/huatuo-toolstream.sock \
  --task-id irq-demo-1
```

### JSON

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

`dropped_samples.collection` 记录内核采样预算或 counts map 已满导致的丢样总数，当前计数器无法区分两种原因。非零表示 profile 不完整，并在 stderr 输出告警；读取计数器失败会使采集失败。profiler 用户态队列满时等待聚合消费，处理上下文取消时终止等待；`dropped_samples.aggregation_queue` 仅保留用于兼容历史数据。

## 工具自身的 Go runtime profile

`--enable-pprof` 默认 false，表示关闭。启用后监听 `127.0.0.1:6000`，端口被占用时启动失败。这里采集的是 irqtracing 进程自身，不是 IRQ/softIRQ 栈，也不是 huatuo-bamai。采集期间须保持工具运行。

## 由 huatuo-bamai 调用

守护进程的 `AutoTracing.IRQTracing` tracer 在其规则检测到某个 CPU 出现 irq/softirq 突增或持续高利用率时，会调用守护进程可执行文件旁的 `irqtracing` 二进制（`CoreBinDir` 由守护进程自身路径推导；源码构建树中即 `_output/bin/irqtracing`）。

调用时传入 `--bpf-path <CoreBpfDir>/irqtracing.o`、`--target-cpu <cpu>`、`--duration <RunTracingToolTimeout>` 和 `--max-events-per-second-per-cpu <MaxEventsPerSecond>`。`MaxEventsPerSecond` 默认是 1000，因此 raise 和 entry 默认各限制为 500/s。

该路径要求配置 Elasticsearch，仅配置 tracing 本地文件存储不足以保存 IRQ profile。触发规则及命中 CPU 利用率保留在守护进程日志中。

### 结果与兼容性

IRQ 结果写入 profiling 存储的 `profile_data`，`tracer_name=irqtracing`、`tracer_type=profiling`，可通过 profiling API 按 `irqtracing:irq:count:irq:count` 查询。历史 tracing 文档继续通过原 tracing 查询读取，不改写历史数据。旧 `flamedata`/`nmissed` 传输格式已移除，协议版本 3 要求 CLI 与守护进程同时升级。

### 完成条件

只有 profile 已可查询、发布成功且工具收到守护进程的确认，采集任务才成功。仅结束数据传输、采集失败或断连均不发布任务完成标记。确认响应丢失时结果可能已发布，应按任务 ID 查询确认。实现细节见[Profiling 窗口提交](/docs/development/storage_zh.md#profiling-窗口提交)。
