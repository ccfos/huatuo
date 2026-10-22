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

## BPF 调试编译

通过 `BPF_DEBUG=1` 将 `-DDEBUG_BPF` 传给 Clang，把调试代码编译进 BPF 对象：

```bash
$ make BPF_DEBUG=1     # 或只重新生成构建产物：make BPF_DEBUG=1 gen-build
```

埋点、运行时开关和日志说明参见[调试](/docs/development/debugging_zh.md)。
