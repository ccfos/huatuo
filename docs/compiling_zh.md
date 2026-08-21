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
$ docker run -it --pid=host --privileged --cgroupns=host --network=host -v $(pwd):/go/huatuo-bamai huatuo/huatuo-bamai-dev:latest sh
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

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 clang，把调试代码编译进 BPF 对象：

```bash
$ make BPF_DEBUG=1            # 或单独编译 BPF：make BPF_DEBUG=1 bpf-build
```

> 注意：两级开关需同时满足才会有输出——`BPF_DEBUG=1` 编译 **且** 运行时带 `--log-bpf-debug`。
埋点、运行时开关和日志说明参见[调试](development/debugging_zh.md)。
