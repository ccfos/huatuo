---
title: 源码编译
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

### 1. 容器编译

可以执行如下命令，完成编译，静态代码检查。
```bash
$ sh build/build-run-testing-image.sh
```

或者单独执行：

**1. 准备编译环境**
```bash
$ docker build --network host -t huatuo/huatuo-bamai-dev:latest -f ./Dockerfile.devel .
```

**2. 启动编译容器** 
```bash
$ docker run -it --privileged --cgroupns=host --network=host -v $(pwd):/go/huatuo-bamai huatuo/huatuo-bamai-dev:latest sh
```

**3. 进入容器编译**
```bash
$ make
```

### 2. 镜像发布

通过 docker build 方式能够快速的发布，最新二进制容器镜像。

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

#### 2.1 多架构支持（linux/amd64 + linux/arm64）

**1. 环境准备**

```bash
# 注册 QEMU 用户态模拟器
docker run --rm --privileged tonistiigi/binfmt --install all

# 创建多架构 builder
docker buildx create --name multiarch \
    --driver docker-container \
    --driver-opt network=host \
    --use

# 验证（同时触发 bootstrap）
docker buildx inspect multiarch --bootstrap
```

**2. 构建并推送**

```bash
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    --network=host \
    -t <your-registry>/huatuo-bamai:latest \
    -f Dockerfile \
    --push .
```

**3. 验证多架构 Manifest**

```bash
docker buildx imagetools inspect <your-registry>/huatuo-bamai:latest
```

期望输出包含 `linux/amd64` 和 `linux/arm64` 两个 platform 条目：

```
Manifests:
  Platform:  linux/amd64
  Platform:  linux/arm64
```

### 3. 物理机编译

#### 3.1 安装依赖

Ubuntu 24.04:
```bash
apt install make git clang libbpf-dev linux-tools-common curl capnproto
```

Fedora 40:
```bash
dnf install make git clang libbpf-devel bpftool curl capnproto capnproto-devel glibc-static
```

```bash
go install mvdan.cc/gofumpt@v0.8.0
go install mvdan.cc/sh/v3/cmd/shfmt@v3.11.0
go install golang.org/x/tools/cmd/goimports@v0.36.0
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
go install github.com/vektra/mockery/v2@v2.53.6
go install capnproto.org/go/capnp/v3/capnpc-go@v3.1.0-alpha.2
```

#### 3.2 编译
```bash
$ make
```

### 4. BPF 调试编译

BPF 代码中可以使用 `bpf_dbg()` / `bpf_dbg_msg()` 宏（定义于 `bpf/include/bpf_dbg.h`）在内核态打印调试信息，便于排查 eBPF 程序的运行逻辑。该功能采用两级开关，默认完全关闭，对生产路径零开销。

#### 4.1 在 BPF 代码中埋点

```c
#include "bpf_dbg.h"

// 在每个需要调试的 BPF 程序所在的 .c 文件中声明调试 map（map 名与下方一致）
BPF_DBG_MAP(native_cpu);

SEC("perf_event")
int prog(void *ctx)
{
        // 仅打印一条消息
        bpf_dbg_msg(ctx, native_cpu, "enter prog");

        // 打印消息并附带最多 3 个 u64 参数
        bpf_dbg(ctx, native_cpu, "pid and addr", pid, addr, 0);
        return 0;
}
```

`BPF_DEBUG=0`（默认）时，上述宏会展开为空操作，调试 perf event array、栈上事件结构、`bpf_ktime_get_ns`、`bpf_perf_event_output` 等都**不会生成**，verifier 也看不到它们，`.o` 体积更小，加载时不消耗额外 fd。

#### 4.2 编译时开启（第一级：DEBUG_BPF）

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 clang，把调试代码编译进 BPF 对象：

```bash
$ make BPF_DEBUG=1            # 或单独编译 BPF：make BPF_DEBUG=1 bpf-build
```

#### 4.3 运行时开启（第二级：log-bpf-debug）

即使已编译进对象，调试输出在运行时默认仍被抑制。需要在启动 profiler 时加上 `--log-bpf-debug` 才会真正打开（当前仅 native profiler 生效）：

```bash
$ ./profiler --type cpu --language native --log-bpf-debug ...
```

其原理是：加载 BPF 对象时通过 `bpf.NewDbg(true)` 创建的 `BpfDbg` 实例，在 `LoadBpf` 前把 `bpf_dbg_enabled` 常量改写为 1；未改写时 verifier 会把 `if (bpf_dbg_enabled)` 当作死代码消除。每个 BPF 对象持有独立的 `BpfDbg`，调试开关互不影响。

#### 4.4 输出内容

调试事件由用户态以 Debug 级别日志打印，每条包含：

- `file`：触发埋点的 BPF 源文件名（`__FILE_NAME__`）
- `line`：源文件行号
- `ts`：事件时间戳（由 `bpf_ktime_get_ns` 转换为 UTC 墙钟时间）
- `msg`：埋点传入的消息字符串
- `args`：可选，最多 3 个 `u64` 参数（全为 0 时省略）

示例：

```text
bpf_dbg: file=native_cpu_profiler.c line=120 ts=2026-01-11T08:30:00.123456Z msg=enter prog args=[0x1f4 0xffff8881 0x0 0x0]
```

> 注意：两级开关需同时满足才会有输出——`BPF_DEBUG=1` 编译 **且** 运行时带 `--log-bpf-debug`。