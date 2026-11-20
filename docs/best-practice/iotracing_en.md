---
title: IO Tracing
type: docs
description: ""
author: HUATUO Team
date: 2026-10-21
weight: 8
---

The standalone command attributes block IO to processes and to the files they
touch, then builds a per-process and per-file summary of filesystem and disk
read/write rates plus queue-to-complete and dispatch-to-complete latency.
It is the same tool the `huatuo-bamai` daemon shells out to from its
`AutoTracing.IOTracing` tracer; running it directly produces the same result
without any storage backend or configuration file.

## Build

The normal project build discovers `cmd/iotracing` automatically and compiles
`bpf/iotracing.c`:

```bash
make build
```

The command is written to `_output/bin/iotracing` and the BPF object to
`_output/bpf/iotracing.o`; the object must be present at run time
(`--bpf-path`).

## Run

```bash
sudo ./_output/bin/iotracing \
  --bpf-path ./_output/bpf/iotracing.o \
  --duration 8 \
  --schedule-threshold 100 \
  --output json
```

`--duration` is the collection window in seconds (default 8) and must be
greater than zero. `--schedule-threshold` is the IO schedule threshold in
milliseconds (default 100) and must be greater than zero; a block IO request
that spends longer than this waiting in `io_schedule` is reported in the stall
stack list. `--output` selects `text` (the displayed default) or `json`.

`--max-stack` (default 10) keeps at most that many of the most recent IO stall
stacks; older samples are evicted when the window overflows. `--max-process`
(default 10) caps the number of processes shown and `--max-files-per-process`
(default 5) caps the files shown per process. The top-K selection is ordered by
total block IO; the untruncated file count is still reported separately so a
truncated listing is visible.

`--enable-pprof` serves this process's Go runtime profiles on
`127.0.0.1:6000`, which is useful when the collection window looks wrong or the
tool exits early.

`--device` restricts collection to one or more block devices given as
`major:minor`, separated by commas:

```bash
sudo ./_output/bin/iotracing \
  --bpf-path ./_output/bpf/iotracing.o \
  --device 8:0,253:0
```

Major and minor numbers are validated before attach; at most 16 devices may be
specified.

## Output

Text output prints a per-process summary table followed by a per-file detail
block for each process. Byte columns are per-second rates over the trace
window; the latency columns are microseconds: `q2c`/`max_q2c` measure
queue-to-complete and `d2c`/`max_d2c` measure dispatch-to-complete. Files opened
with direct IO are marked `[direct IO]`.

`--output json` emits the standard snapshot instead:

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

## Integration with huatuo-bamai

The daemon's `AutoTracing.IOTracing` tracer samples host block devices from
`/proc/diskstats` and triggers the tool when metrics cross configured thresholds
on **two consecutive sampling intervals** for the same device:

- **Non-NVMe devices**: `io_util` > `UtilThreshold` for two consecutive
  intervals is sufficient to trigger.
- **NVMe devices**: `io_util` > `UtilThreshold` for two consecutive intervals
  **and** either `ReadBPS` > `RbpsThreshold`×1024×1024 for two consecutive
  intervals **or** `WriteBPS` > `WbpsThreshold`×1024×1024 for two consecutive
  intervals.
- **All devices**: `ReadAwait` > `AwaitThreshold` for two consecutive intervals
  **or** `WriteAwait` > `AwaitThreshold` for two consecutive intervals can trigger
  independently.

`RunTracingToolTimeout` (default 10s) is passed as `--duration` to the
iotracing tool and defines the collection window. It is the collection duration,
not necessarily a hard deadline for the entire subprocess including
initialization and result transfer. `MaxProcDump` / `MaxFilesPerProcDump` map to
`--max-process` / `--max-files-per-process`.

To forward the snapshot to a running daemon over the toolstream Unix socket, the
daemon must launch the tool with `--output-storage` and a `--task-id`. The
daemon registers that task ID in `pendingReasons` before starting the
subprocess so `handleIotracingEvent` can match the snapshot. Do **not** supply
an arbitrary `--task-id` when running the tool manually: snapshots sent that way
will be rejected as having no pending request. For standalone use, use
`--output text` or `--output json`. `--output` and `--output-storage` are
mutually exclusive.
