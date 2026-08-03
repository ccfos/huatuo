---
title: Bare-Metal
type: docs
description:
author: HUATUO Team, HAO022
date: 2026-01-11
weight: 3
---

## Production resource limits

Set explicit collector resource limits in `huatuo-bamai.conf`:

```toml
[Runtime]
StartupCPULimitCores = 0.5
CPULimitCores = 2.0
MemoryLimitMiB = 2048
```

Binary installations use
`/opt/huatuo-bamai/conf/huatuo-bamai.conf`; RPM installations use
`/etc/huatuo-bamai/huatuo-bamai.conf`. Configure the limits before the first
start. After changing them for a running service, restart the collector:

```bash
sudo systemctl restart huatuo-bamai
```

The `0.5` core startup limit reduces initialization spikes. The `2` core runtime
limit preserves collection capacity, while the `2048 MiB` memory limit prevents
abnormal growth from exhausting host memory. Adjust these values based on host
capacity, collection jobs, and observed resource peaks.

The provided `huatuo-bamai.service` uses `Delegate=yes`, allowing the collector
to manage its delegated cgroup. Do not add `CPUQuota` or `MemoryMax`; a lower
stacked limit can cause startup failures or unexpected throttling.

After starting the service, check its status and delegated cgroup:

```bash
systemctl status huatuo-bamai --no-pager
systemd-cgls --unit huatuo-bamai.service
```

## Binary

The HUATUO release provides static Linux tar packages for amd64 and arm64. The tar package contains the `huatuo-bamai` and `huatuo-apiserver` binaries, configuration files, and BPF objects.

This section applies to releases that provide assets named `huatuo-bamai-<version>-static-linux-<arch>.tar.gz`. This naming starts with `v2.2.0` in the current releases. The `v2.0.0` and `v2.1.0` tar packages use different names, so the commands below do not apply to them directly.

The commands below use `HUATUO_VERSION` for the target version. Change it to the release version you want to install, for example `v2.2.0`:

```bash
HUATUO_VERSION="<release-version>"
```

### 1. Download the tar package

For x86_64 hosts, download the amd64 package:

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-amd64.tar.gz"
```

For aarch64 hosts, download the arm64 package:

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-arm64.tar.gz"
```

### 2. Install the tar package

Create the installation, log, and data directories:

```bash
sudo install -d -m 0755 /opt/huatuo-bamai /var/log/huatuo-bamai /var/lib/huatuo-bamai
```

For amd64:

```bash
sudo tar -xzf "huatuo-bamai-${HUATUO_VERSION}-static-linux-amd64.tar.gz" --strip-components=1 --no-same-owner -C /opt/huatuo-bamai
```

For arm64:

```bash
sudo tar -xzf "huatuo-bamai-${HUATUO_VERSION}-static-linux-arm64.tar.gz" --strip-components=1 --no-same-owner -C /opt/huatuo-bamai
```

### 3. Install the service unit files

Download the service unit files from the matching source version:

```bash
sudo wget -O /etc/systemd/system/huatuo-bamai.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-bamai.service"
sudo wget -O /etc/systemd/system/huatuo-apiserver.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-apiserver.service"
```

### 4. Modify the configurations

Edit `/opt/huatuo-bamai/conf/huatuo-bamai.conf` and `/opt/huatuo-bamai/conf/huatuo-apiserver.conf` to match the deployment environment. For detailed configuration options, see the [`huatuo-bamai` configuration](/docs/configuration/huatuo-bamai-configuration_en.md) and [`huatuo-apiserver` configuration](/docs/configuration/huatuo-apiserver-configuration_en.md).

Set `[Runtime]` in `huatuo-bamai.conf` as described in "Production resource
limits."

### 5. Register the HUATUO services

Reload the systemd configuration:

```bash
sudo systemctl daemon-reload
```

### 6. Start the HUATUO services

Start the services and enable them at system startup:

```bash
sudo systemctl enable --now huatuo-bamai huatuo-apiserver
```

## RPM Package

The OpenCloudOS repository provides HUATUO v2.1.0 RPM packages for x86_64 and aarch64. The RPM package installs the HUATUO files and systemd service unit file.

### 1. Download the RPM package

Download the package for the host architecture:

For x86_64:

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

For aarch64:

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/aarch64/os/Packages/huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 2. Install the RPM package

For x86_64:

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

For aarch64:

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 3. Modify the configuration

Edit `/etc/huatuo-bamai/huatuo-bamai.conf` to match the deployment environment. For detailed configuration options, see the [`huatuo-bamai` configuration](/docs/configuration/huatuo-bamai-configuration_en.md).

Set `[Runtime]` as described in "Production resource limits."

### 4. Start the HUATUO service

The RPM package installs the `huatuo-bamai.service` service unit file. Start the service and enable it at system startup:

```bash
sudo systemctl enable --now huatuo-bamai
```

> For complete RPM installation instructions, see [https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ](https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ).
