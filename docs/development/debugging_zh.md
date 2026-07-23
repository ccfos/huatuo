---
title: 开发调试
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

## 全组件联调

开发 Compose 使用当前工作区源码构建开发镜像，并启动采集器、API Server、
Elasticsearch、Prometheus 和 Grafana。采集器需要访问宿主机内核和 cgroup，
因此应在 Linux 主机的项目根目录使用 root 权限运行：

```bash
sudo make compose-dev-up
```

Compose 在前台聚合所有组件日志。修改源码后，按 `Ctrl+C` 停止环境并重新执行
命令；Docker 会复用工具链和 Go 编译缓存。

调试结束后删除容器、数据卷和开发镜像：

```bash
sudo make compose-dev-down
```

该命令会删除 Elasticsearch 数据卷。需要保留联调数据时，不要执行该命令。

## BPF 调试

BPF 代码可以使用 `bpf_dbg()` 和 `bpf_dbg_msg()` 宏在内核态输出调试信息。
宏定义位于 `bpf/include/bpf_dbg.h`。调试功能包含编译时和运行时两级开关，
默认完全关闭。

### 添加调试埋点

每个使用调试宏的 BPF 源文件都需要声明独立的调试 map：

```c
#include "bpf_dbg.h"

BPF_DBG_MAP(native_cpu);

SEC("perf_event")
int prog(void *ctx)
{
        bpf_dbg_msg(ctx, native_cpu, "enter prog");
        bpf_dbg(ctx, native_cpu, "pid and addr", pid, addr, 0);
        return 0;
}
```

`bpf_dbg_msg()` 只输出消息；`bpf_dbg()` 还可以携带最多三个 `u64` 参数。

### 编译调试对象

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 Clang：

```bash
make BPF_DEBUG=1
```

只重新编译 BPF 对象：

```bash
make BPF_DEBUG=1 bpf-build
```

`BPF_DEBUG=0` 是默认值。此时宏展开为空操作，调试 perf event array、事件结构、
`bpf_ktime_get_ns` 和 `bpf_perf_event_output` 都不会进入 BPF 对象。

### 启用运行时输出

编译调试对象后，启动 profiler 时还需要增加 `--log-bpf-debug`。当前只有 native
profiler 支持该开关：

```bash
./profiler --type cpu --language native --log-bpf-debug ...
```

加载 BPF 对象时，`bpf.NewDbg(true)` 会在 `LoadBpf` 前将
`bpf_dbg_enabled` 常量改写为 1。未启用时，verifier 会将对应分支作为死代码
消除。每个 BPF 对象维护独立开关，互不影响。

### 查看调试输出

用户态以 Debug 级别输出调试事件：

- `file`：BPF 源文件名。
- `line`：源文件行号。
- `ts`：转换为 UTC 墙钟时间的事件时间戳。
- `msg`：调试消息。
- `args`：最多三个 `u64` 参数；全部为零时省略。

```text
bpf_dbg: file=native_cpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0]
```

只有同时使用 `BPF_DEBUG=1` 编译并在运行时指定 `--log-bpf-debug`，才会产生
调试输出。
