---
title: 源码编译
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

本文说明容器和宿主机上的构建方法。以下命令均在项目根目录执行；Go 版本要求以 `go.mod` 为准。

## 容器编译

运行脚本，执行容器构建和静态检查：
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

## 构建运行镜像

以下命令构建本地运行镜像，不会推送到镜像仓库：

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

## 宿主机编译

### 安装依赖

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

### 编译

```bash
$ make
```

## Memray 运行时包

`make` 和 `make build` 默认下载固定版本的 Memray 包到
`_output/tools/memray`，包含 Linux glibc、x86-64 和 AArch64 上的
CPython 3.7–3.14 运行时。无需 Python 编译依赖或 Memray 源码，下载需要
`curl`、`tar` 和 `sha256sum`，每种架构都有固定校验值。

仅准备运行时，或使用已下载的发布归档：

```bash
make prep-memray-bundle
MEMRAY_BUNDLE_ARCHIVE=/path/to/memray-huatuo.tar.gz make prep-memray-bundle
```

不需要 Python 内存采集时，可用 `MEMRAY_BUNDLE_ENABLED=0 make build`
跳过下载。之后采集内存前，仍需在默认目录或 `--tool-path` 指定的位置提供
运行时包。更换版本需同时设置 `MEMRAY_BUNDLE_VERSION` 和对应的
`MEMRAY_BUNDLE_SHA256`；发布镜像地址通过 `MEMRAY_BUNDLE_REPOSITORY` 指定。

实际运行 profiler 的环境还需要 `gdb` 和 ptrace 附加权限。目标 Python
必须使用 glibc，当前发布包不支持 musl 或 free-threaded CPython。

## BPF 调试编译

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 Clang，把调试代码编译进 BPF 对象：

```bash
$ make BPF_DEBUG=1     # 或只重新生成构建产物：make BPF_DEBUG=1 gen-build
```

埋点、运行时开关和日志说明参见[调试](/docs/development/debugging_zh.md)。
