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

### 2. 物理机编译

#### 2.1 安装依赖

Ubuntu 24.04:
```bash
apt install make git clang libbpf-dev linux-tools-common curl
```

Fedora 40:
```bash
dnf install make git clang libbpf-devel bpftool curl
```

#### 2.2 编译
```bash
$ make
```

### 3. 镜像发布

#### 3.1 镜像构建

通过 docker build 方式能够快速的发布，最新二进制容器镜像。

```bash
docker build --network host -t huatuo/huatuo-bamai:latest .
```

#### 3.2 多架构支持（linux/amd64 + linux/arm64）

**环境准备**

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

**构建并推送**

```bash
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    --network=host \
    -t <your-registry>/huatuo-bamai:latest \
    -f Dockerfile \
    --push .
```

**验证多架构 Manifest**

```bash
docker buildx imagetools inspect <your-registry>/huatuo-bamai:latest
```

期望输出包含 `linux/amd64` 和 `linux/arm64` 两个 platform 条目：

```
Manifests:
  Platform:  linux/amd64
  Platform:  linux/arm64
```
