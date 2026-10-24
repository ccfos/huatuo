---
title: IO 追踪
type: docs
description: ""
author: HUATUO Team
date: 2026-10-21
weight: 8
---

独立命令将块 IO 归属到进程及其访问的文件，并生成按进程、按文件的汇总：文件系统与磁盘读写速率，以及 queue-to-complete 和 dispatch-to-complete 延迟。`huatuo-bamai` 守护进程的 `AutoTracing.IOTracing` tracer 正是通过 shell 调用这一工具；直接运行它即可得到相同的结果，无需任何存储后端或配置文件。

## 构建

常规项目构建会自动发现 `cmd/iotracing` 并编译 `bpf/iotracing.c`：

```bash
make build
```

命令输出到 `_output/bin/iotracing`，BPF 对象输出到 `_output/bpf/iotracing.o`；运行时需要通过 `--bpf-path` 指定该对象。

## 运行

```bash
sudo ./_output/bin/iotracing \
  --bpf-path ./_output/bpf/iotracing.o \
  --duration 8 \
  --schedule-threshold 100 \
  --output json
```

`--duration` 是采集窗口的秒数（默认 8），必须大于零。`--schedule-threshold` 是 IO 调度阈值，单位为毫秒（默认 100），必须大于零；块 IO 请求在 `io_schedule` 中等待超过该阈值时，会记录到 stall 栈列表中。`--output` 可选 `text`（帮助中显示的默认值）或 `json`。

`--max-stack`（默认 10）最多保留最近多少条 IO stall 栈；窗口溢出时较早的样本会被淘汰。`--max-process`（默认 10）限制显示的进程数，`--max-files-per-process`（默认 5）限制每个进程显示的文件数。Top-K 按块 IO 总量排序；未截断的文件总数仍会单独报告，因此可以看到列表是否被截断。

`--enable-pprof` 会在 `127.0.0.1:6000` 上暴露本进程的 Go 运行时 profile，便于排查采集窗口异常或进程提前退出的问题。

`--device` 将采集限制在一个或多个块设备上，格式为 `major:minor`，多个设备以逗号分隔：

```bash
sudo ./_output/bin/iotracing \
  --bpf-path ./_output/bpf/iotracing.o \
  --device 8:0,253:0
```

主次设备号在挂载前会做校验；最多可指定 16 个设备。

## 输出

文本输出先打印按进程的汇总表，再为每个进程打印按文件的详情块。字节列为该采集窗口内的每秒速率；延迟列单位为微秒：`q2c`/`max_q2c` 表示 queue-to-complete，`d2c`/`max_d2c` 表示 dispatch-to-complete。以 direct IO 打开的文件会标注 `[direct IO]`。

`--output json` 则输出标准快照：

```json
{
  "process_file_io_stats": [
    {
      "pid": 1234,
      "comm": "fio",
      "total_fs_read_bps": 0,
      "total_fs_write_bps": 1048576,
      "total_disk_read_bps": 0,
      "total_disk_write_bps": 1048576,
      "total_files": [
        {
          "major": 8,
          "minor": 0,
          "path": "/data/file",
          "fs_write_bps": 1048576,
          "disk_write_bps": 1048576,
          "q2c_us": 120,
          "max_q2c_us": 480,
          "d2c_us": 90,
          "max_d2c_us": 300
        }
      ],
      "total_file_count": 1
    }
  ],
  "io_schedule_timeout_stacks": []
}
```

## 与 huatuo-bamai 集成

守护进程的 `AutoTracing.IOTracing` tracer 在容器超过 IO 阈值时触发该工具：`WbpsThreshold`（默认 1500 MB/s）或 `RbpsThreshold`（默认 2000 MB/s），对于 NVMe 设备还需满足 `UtilThreshold`（默认 90%）或 `AwaitThreshold`（默认 100 ms）。`RunTracingToolTimeout`（默认 10s）限定执行时间，`MaxProcDump` / `MaxFilesPerProcDump` 分别对应 `--max-process` / `--max-files-per-process`。使用 `--output-storage` 可通过 toolstream Unix socket 将快照转发给运行中的守护进程，并用 `--task-id` 指定唯一任务 ID。`--output` 与 `--output-storage` 互斥。
