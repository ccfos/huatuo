---
title: 开发调试
type: docs
description:
author: HUATUO Team
date: 2026-07-18
weight: 4
---

按调试场景选择：[全组件联调](#全组件联调)、[BPF 调试](#bpf-调试)或 [GitHub Actions 虚拟机调试](#虚拟机调试)。

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

只重新生成构建产物而不重新编译二进制：

```bash
make BPF_DEBUG=1 gen-build
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
bpf_dbg: file=native_oncpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0]
```

只有同时使用 `BPF_DEBUG=1` 编译并在运行时指定 `--log-bpf-debug`，才会产生
调试输出。

## 虚拟机调试

使用 `OS Distro QEMU CI` workflow 可以在支持的 Linux 虚拟机中测试分支，
无需在本地准备虚拟机。

### 准备 Fork 仓库

1. Fork 仓库，并在 fork 仓库的 **Actions** 页面启用 workflows。
2. 将 SSH 公钥添加到
   [GitHub 账号](https://github.com/settings/keys)。只有启动 workflow 的用户
   可以连接。
3. 将待调试分支推送到 fork 仓库。

### 启动虚拟机

1. 在 fork 仓库中打开 **Actions > OS Distro QEMU CI > Run workflow**。
2. 选择需要调试的分支。
3. 保留默认值 `ubuntu24.04`、填写以逗号分隔的多个发行版，或者使用 `all`
   选择所有支持的发行版。
4. 在 **Debug VM** 选项中选择保留虚拟机的时机和时长。
5. 启动 workflow，并打开对应的 `Test in VM` job。

如果没有 **Run workflow** 按钮，请先同步 fork 仓库的默认分支。GitHub 只会
显示默认分支中存在的手动 workflow。

选择 **Before test** 可以在测试前检查或修改虚拟机；选择 **After test on
failure** 可以排查失败测试；选择 **After test always** 会在测试结束后保留
虚拟机。

### 连接虚拟机

打开 job 中的 `Debug VM: Retain ...` step，并按日志提示操作：

1. 在本地终端运行日志中的完整 SSH 命令，连接 GitHub runner。
2. 在 runner 中运行日志中的 `ssh -i ... root@<VM_IP>` 命令，进入虚拟机。

日志中的命令已经包含正确的密钥路径和虚拟机地址，直接复制即可。

### 结束调试

退出虚拟机，然后在 runner 中释放 workflow：

```bash
cd "$GITHUB_WORKSPACE"
touch continue
```

workflow 随后继续执行测试或清理。仅断开 SSH 不会释放 workflow；如果不执行
上述命令，workflow 会一直等待到所选保留时间结束。
