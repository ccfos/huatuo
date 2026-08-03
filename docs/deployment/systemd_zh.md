---
title: 物理机部署
type: docs
description:
author: HUATUO Team, HAO022
date: 2026-01-11
weight: 3
---

## 生产环境资源限制

在 `huatuo-bamai.conf` 中显式配置采集器资源上限：

```toml
[Runtime]
StartupCPULimitCores = 0.5
CPULimitCores = 2.0
MemoryLimitMiB = 2048
```

二进制安装使用 `/opt/huatuo-bamai/conf/huatuo-bamai.conf`，RPM 安装使用
`/etc/huatuo-bamai/huatuo-bamai.conf`。应在首次启动前完成配置；运行中的服务
修改配置后需要执行：

```bash
sudo systemctl restart huatuo-bamai
```

启动阶段限制为 `0.5` 核，降低初始化 CPU 峰值；启动后限制为 `2` 核，保障持续
采集。`2048 MiB` 内存上限可防止异常增长耗尽宿主机内存。示例值应根据主机
规格、采集任务和资源峰值调整。

提供的 `huatuo-bamai.service` 使用 `Delegate=yes`，由采集器在委派的 cgroup 中
管理资源。不要叠加 `CPUQuota` 或 `MemoryMax`，否则较小的限制可能导致启动失败
或意外限流。

服务启动后，检查运行状态和委派的 cgroup：

```bash
systemctl status huatuo-bamai --no-pager
systemd-cgls --unit huatuo-bamai.service
```

## 二进制

HUATUO Release 提供 amd64 和 arm64 的 Linux 静态 tar 包。tar 包包含 `huatuo-bamai` 和 `huatuo-apiserver` 二进制文件、配置文件和 BPF 对象。

本节命令适用于发布资产名为 `huatuo-bamai-<version>-static-linux-<arch>.tar.gz` 的版本。当前该命名从 `v2.2.0` 开始提供；`v2.0.0` 和 `v2.1.0` 的 tar 包名不同，不能直接套用以下命令。

以下命令使用 `HUATUO_VERSION` 表示目标版本，可按实际发行版本调整，例如 `v2.2.0`：

```bash
HUATUO_VERSION="<release-version>"
```

### 1. 下载 tar 包

x86_64 主机下载 amd64 包：

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-amd64.tar.gz"
```

aarch64 主机下载 arm64 包：

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-arm64.tar.gz"
```

### 2. 安装 tar 包

创建安装、日志和数据目录：

```bash
sudo install -d -m 0755 /opt/huatuo-bamai /var/log/huatuo-bamai /var/lib/huatuo-bamai
```

amd64：

```bash
sudo tar -xzf "huatuo-bamai-${HUATUO_VERSION}-static-linux-amd64.tar.gz" --strip-components=1 --no-same-owner -C /opt/huatuo-bamai
```

arm64：

```bash
sudo tar -xzf "huatuo-bamai-${HUATUO_VERSION}-static-linux-arm64.tar.gz" --strip-components=1 --no-same-owner -C /opt/huatuo-bamai
```

### 3. 安装服务单元文件

从对应版本源码下载服务单元文件：

```bash
sudo wget -O /etc/systemd/system/huatuo-bamai.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-bamai.service"
sudo wget -O /etc/systemd/system/huatuo-apiserver.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-apiserver.service"
```

### 4. 修改配置

根据实际部署环境编辑 `/opt/huatuo-bamai/conf/huatuo-bamai.conf` 和 `/opt/huatuo-bamai/conf/huatuo-apiserver.conf`。详细配置项说明请参见 [`huatuo-bamai` 配置](/docs/configuration/huatuo-bamai-configuration_zh.md) 和 [`huatuo-apiserver` 配置](/docs/configuration/huatuo-apiserver-configuration_zh.md)。

按照“生产环境资源限制”一节设置 `huatuo-bamai.conf` 中的 `[Runtime]`。

### 5. 注册 HUATUO 服务

重新加载 systemd 配置：

```bash
sudo systemctl daemon-reload
```

### 6. 启动 HUATUO 服务

启动服务并设置开机启动：

```bash
sudo systemctl enable --now huatuo-bamai huatuo-apiserver
```

## RPM 包

OpenCloudOS 镜像仓库提供 HUATUO v2.1.0 的 x86_64 和 aarch64 RPM 包。RPM 包会安装 HUATUO 文件和 systemd 服务单元文件。

### 1. 下载 RPM 包

根据主机架构下载对应的 RPM 包：

x86_64：

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

aarch64：

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/aarch64/os/Packages/huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 2. 安装 RPM 包

x86_64：

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

aarch64：

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 3. 修改配置

根据实际部署环境编辑 `/etc/huatuo-bamai/huatuo-bamai.conf`。详细配置项说明请参见 [`huatuo-bamai` 配置](/docs/configuration/huatuo-bamai-configuration_zh.md)。

按照“生产环境资源限制”一节设置 `[Runtime]`。

### 4. 启动 HUATUO 服务

RPM 包会安装 `huatuo-bamai.service` 服务单元文件。启动服务并设置开机启动：

```bash
sudo systemctl enable --now huatuo-bamai
```

> 完整的 RPM 安装指引请参阅 [https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ](https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ)。
