# Standalone irqtracing（独立 irqtracing）

独立命令采集目标 CPU 的 irq/softirq 栈，并生成火焰图：其中 sources 表示发起 softirq 的来源，victims 表示被 softirq 抢占的任务。victim 通过两种方式记录：当 softirq 抢占正在运行的任务时，直接捕获该任务的栈；当由 ksoftirqd 处理 softirq 时，则将目标 CPU runqueue 上排队的任务近似视为 victim（栈未知）。`huatuo-bamai` 守护进程的 `AutoTracing.IrqTracing` tracer 正是通过 shell 调用这一工具；直接运行它即可得到相同的 JSON 结果，无需任何存储后端或配置文件。

## 构建

常规项目构建会自动发现 `cmd/irqtracing` 并编译 `bpf/irq_tracing.c`：

```bash
make all
```

命令输出到 `_output/bin/irqtracing`，BPF 对象输出到 `_output/bpf/irq_tracing.o`；运行时需要通过 `--bpf-path` 指定该对象。

## 运行

```bash
mkdir -p ./irqtracing-out

sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irq_tracing.o \
  --target-cpu 3 \
  --duration 5 \
  --output-path ./irqtracing-out
```

`--target-cpu` 必填，选择要跟踪的 CPU。工具在 `activate_task` / `deactivate_task` 上挂载 kprobe，并在 `irq/softirq_raise` / `irq/softirq_entry` 上挂载 tracepoint，均限定在该 CPU，然后采集 `--duration` 秒（默认 3）。

采集窗口结束时，每次运行向 `--output-path`（默认 `.`）写入一个 JSON 结果，并向 stdout 打印其完整路径。输出目录必须事先存在：工具不会创建目录。

```text
./irqtracing-out/irqtracing_<unix-nano>.json
```

文件内容：

```json
{
  "flamedata": { "...": "profile 树，未采集到任何数据时为 null" },
  "rq_tasks": ["comm(pid)", "..."],
  "nmissed": 1234
}
```

`flamedata` 与平台消费的 profile 格式一致（`ProfileType` 为 `irq_tracing:irq:count:irq:count`）。每条栈以 `source` 或 `victim` 为根，后接 `source[comm,VEC]` 或 `victim[comm(pid),VEC]` 标签帧，再依次是用户态栈帧和带 `_[k]` 后缀的内核态栈帧。`VEC` 取值为 `HI`、`TIMER`、`NET_TX`、`NET_RX`、`BLOCK`、`IRQ_POLL`、`TASKLET`、`SCHED`、`HRTIMER`、`RCU` 之一，未知向量显示为 `VEC<n>`。

`rq_tasks` 列出采集窗口内探针在目标 CPU runqueue 上观察到、窗口结束时尚未 deactivate 的任务。当 ksoftirqd 在目标 CPU 上处理了 softirq 时，这些任务就是排在其后的 victim；它们同时以 `[UNKNOWN]` 栈帧追加到 `flamedata` 的 `victim` 条目中。这是近似结果：runqueue 在窗口结束后整体转储一次，而不是在每个 softirq entry 时转储，且所有条目共享最后一次上报的 softirq 向量。此外，`rq_tasks` 映射只包含探针挂载后观察到的 `activate_task` 调用，并被 `deactivate_task` 清除，因此采集开始前已在目标 CPU 上可运行、且窗口内未再次被激活的任务不会被捕获。空闲任务（pid 0）永远不会被报告为 victim。

`nmissed` 是采集窗口内丢弃的样本总数。样本丢弃要么来自每条流的 first-N 预算（source 与 victim 两条流分别独立限流；上限固化在 BPF 程序里），要么来自 counts 映射已满——两者都意味着火焰图不完整：它不包含窗口内目标 CPU 上发生的每一个 irq/softirq 样本。当丢弃计数或 runqueue 转储无法读取时，工具会直接失败而不是写出结果。非零的 `nmissed` 同时会在 stderr 输出一条告警；JSON 字段是守护进程消费的持久化契约。

运行日志被丢弃。只有结果路径写入 stdout，失败信息写入 stderr。

进程在以下情况退出：采集时长到达、调用方取消、或收到 `SIGHUP`、`SIGQUIT`、`SIGINT`、`SIGTERM` 信号。

## 由 huatuo-bamai 调用

守护进程的 `AutoTracing.IrqTracing` tracer 在其规则检测到某个 CPU 出现 irq/softirq 突增或持续高利用率时，会调用守护进程可执行文件旁的 `irqtracing` 二进制（`CoreBinDir` 由守护进程自身路径推导；源码构建树中即 `_output/bin/irqtracing`），传入 `--bpf-path <CoreBpfDir>/irq_tracing.o`、`--target-cpu <cpu>` 和 `--duration <RunTracingToolTimeout>`。JSON 被解析并合并进保存的 tracing 数据：守护进程 `IrqTracingData` 的 `flamedata` 和 `rq_tasks` 字段与独立命令的结果相同，另额外记录 `rule`、`trigger_cpu`、`trace_duration` 和 `hit_cpus`。
