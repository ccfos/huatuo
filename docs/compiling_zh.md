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

### 3. 物理机编译

#### 3.1 安装依赖

Ubuntu 24.04:
```bash
apt install make git clang g++ libbpf-dev linux-tools-common curl capnproto \
  libunwind-dev liblz4-dev libdebuginfod-dev python3-dev python3-pip pkg-config
```

Fedora 40:
```bash
dnf install make git clang gcc-c++ libbpf-devel bpftool curl capnproto \
  capnproto-devel glibc-static libunwind-devel lz4-devel \
  elfutils-debuginfod-client-devel python3-devel python3-pip pkgconf-pkg-config
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

默认构建会为检测到且可用的 Python 解释器分别生成 Memray-Lite 运行时。
每个需要打包的解释器都要安装以下 Python 构建模块：

```bash
python3 -m pip install pip setuptools wheel Cython pkgconfig
```

通过 `MEMRAY_PYTHON_BINS` 指定需要构建的解释器：

```bash
MEMRAY_PYTHON_BINS="python3.8 python3.9" make
```

只重新构建 Memray-Lite 运行时：

```bash
MEMRAY_PYTHON_BINS="python3.8 python3.9" make memray-bundle
```

### 4. BPF 调试编译

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 clang，把调试代码编译进 BPF 对象：

```bash
$ make BPF_DEBUG=1            # 或单独编译 BPF：make BPF_DEBUG=1 bpf-build
```

埋点、运行时开关和日志说明参见[调试](development/debugging_zh.md)。
