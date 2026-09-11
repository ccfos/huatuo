# Standalone irqtracing

The standalone command collects irq/softirq stacks of one target CPU and builds a flame graph of the sources (who raised the softirq) and the victims (the tasks the softirq preempted). Victims are recorded in two ways: when the softirq preempts a running task, that task's stack is captured directly; when ksoftirqd services the softirq instead, the tasks queued on the target CPU runqueue are approximated as the victims (with unknown stacks). It is the same tool the `huatuo-bamai` daemon shells out to from its `AutoTracing.IrqTracing` tracer; running it directly produces the same result JSON without any storage backend or configuration file.

## Build

The normal project build discovers `cmd/irqtracing` automatically and compiles `bpf/irq_tracing.c`:

```bash
make all
```

The command is written to `_output/bin/irqtracing` and the BPF object to `_output/bpf/irq_tracing.o`; the object must be present at run time (`--bpf-path`).

## Run

```bash
mkdir -p ./irqtracing-out

sudo ./_output/bin/irqtracing \
  --bpf-path ./_output/bpf/irq_tracing.o \
  --target-cpu 3 \
  --duration 5 \
  --output-path ./irqtracing-out
```

`--target-cpu` is required and selects the CPU to trace. The tool attaches kprobes on `activate_task` / `deactivate_task` and tracepoints `irq/softirq_raise` / `irq/softirq_entry` restricted to that CPU, then collects for `--duration` seconds (default 3).

At the end of the window it writes one JSON result per run into `--output-path` (default `.`) and prints its full path to stdout. The output directory must already exist: the tool does not create it.

```text
./irqtracing-out/irqtracing_<unix-nano>.json
```

The file contains:

```json
{
  "flamedata": { "...": "profile tree, null when nothing was collected" },
  "rq_tasks": ["comm(pid)", "..."],
  "nmissed": 1234
}
```

`flamedata` is the same profile format the platform consumes (`ProfileType` `irq_tracing:irq:count:irq:count`). Every stack is rooted at `source` or `victim`, followed by a `source[comm,VEC]` or `victim[comm(pid),VEC]` label frame, then the user-space frames and the kernel frames suffixed with `_[k]`. `VEC` is one of `HI`, `TIMER`, `NET_TX`, `NET_RX`, `BLOCK`, `IRQ_POLL`, `TASKLET`, `SCHED`, `HRTIMER`, `RCU`, or `VEC<n>` for unknown vectors.

`rq_tasks` lists the tasks observed by the probes on the target CPU runqueue during the collection window that were still not deactivated when the window ended. When ksoftirqd serviced a softirq on the target CPU, these are the victims that were waiting behind it; they are also appended to `flamedata` as `victim` entries with `[UNKNOWN]` frames. This is an approximation: the runqueue is dumped once after the whole window, not at each softirq entry, and all entries share the last reported softirq vector. Also, the `rq_tasks` map is only populated from `activate_task` calls seen after the probes attach and is cleared by `deactivate_task`, so tasks already runnable on the target CPU before collection started and not re-activated during the window are not captured. The idle task (pid 0) is never reported as a victim.

`nmissed` is the total number of samples dropped during the collection window. Samples are dropped either by the per-stream first-N budgets (the source and victim streams are capped independently; the caps are baked into the BPF program) or because a counts map is full — both mean the flame graph is incomplete: it does not contain every irq/softirq sample that occurred on the target CPU during the window. When the drop count or the runqueue dump cannot be read, the tool fails instead of writing a result. A non-zero `nmissed` also produces a warning line on stderr; the JSON field is the persisted contract consumed by the daemon.

Operational logs are discarded. Only the result path is written to stdout and failures to stderr.

The process exits when the duration elapses, when the caller cancels, or when it receives `SIGHUP`, `SIGQUIT`, `SIGINT`, or `SIGTERM`.

## Use from huatuo-bamai

The daemon's `AutoTracing.IrqTracing` tracer invokes the `irqtracing` binary located next to the daemon executable itself (`CoreBinDir` is derived from the daemon's own path; in the source build tree that is `_output/bin/irqtracing`) when its rules detect an irq/softirq spike or sustained high utilization on some CPU, passing `--bpf-path <CoreBpfDir>/irq_tracing.o`, `--target-cpu <cpu>` and `--duration <RunTracingToolTimeout>`. The JSON is parsed and merged into the saved tracing data: the `flamedata` and `rq_tasks` fields of the daemon's `IrqTracingData` are the same as those of the standalone result, while the daemon additionally records `rule`, `trigger_cpu`, `trace_duration` and `hit_cpus`.
