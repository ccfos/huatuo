---
title: Host Deployment
type: docs
description:
author: HUATUO Team, HAO022
date: 2026-01-11
weight: 3
---

This guide covers installing HUATUO from a tar archive or RPM and managing it with systemd. Check the [resource limits](#resource-limits-and-verification) before starting, then verify the service status after installation.

## Tar Archive

HUATUO releases provide static Linux tar archives for amd64 and arm64. Each archive contains the `huatuo-bamai` and `huatuo-apiserver` binaries, configuration files, and BPF objects.

These commands apply to releases with assets named `huatuo-bamai-<version>-static-linux-<arch>.tar.gz`. This naming scheme starts with `v2.2.0`; archives for `v2.0.0` and `v2.1.0` have different names and cannot use these commands unchanged.

Set `HUATUO_VERSION` to the target release, such as `v2.2.0`:

```bash
HUATUO_VERSION="<release-version>"
```

### 1. Download the Archive

On x86_64 hosts, download the amd64 archive:

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-amd64.tar.gz"
```

On aarch64 hosts, download the arm64 archive:

```bash
wget "https://github.com/ccfos/huatuo/releases/download/${HUATUO_VERSION}/huatuo-bamai-${HUATUO_VERSION}-static-linux-arm64.tar.gz"
```

### 2. Install the Archive

Create the installation, log, and data directories:

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

### 3. Install Service Units

Download the service units from the corresponding source release:

```bash
sudo wget -O /etc/systemd/system/huatuo-bamai.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-bamai.service"
sudo wget -O /etc/systemd/system/huatuo-apiserver.service "https://raw.githubusercontent.com/ccfos/huatuo/${HUATUO_VERSION}/build/rpm/huatuo-apiserver.service"
```

Then check that both downloads produced a real unit file. `wget` exits non-zero
and leaves a zero-byte file behind when the URL does not exist, and systemd
reports such a file as `masked` instead of a download error, so a failed download
would otherwise only surface as a confusing error in the next steps:

```bash
for unit in huatuo-bamai huatuo-apiserver; do
    sudo test -s "/etc/systemd/system/${unit}.service" &&
        sudo head -n 1 "/etc/systemd/system/${unit}.service" | grep -q '^\[Unit\]' ||
        { echo "${unit}.service was not downloaded correctly" >&2; exit 1; }
done
```

`build/rpm/huatuo-apiserver.service` was added after v2.3.0, so releases up to
and including v2.3.0 do not contain it and the second download fails on those
tags. On such a release, fetch the unit from the main branch instead (and enable
only `huatuo-bamai` if you do not want to run the API server as a service):

```bash
sudo wget -O /etc/systemd/system/huatuo-apiserver.service "https://raw.githubusercontent.com/ccfos/huatuo/main/build/rpm/huatuo-apiserver.service"
```

### 4. Edit the Configuration

Edit `/opt/huatuo-bamai/conf/huatuo-bamai.conf` and `/opt/huatuo-bamai/conf/huatuo-apiserver.conf` for your environment. See the [`huatuo-bamai` configuration](/docs/configuration/huatuo-bamai-configuration_en.md) and [`huatuo-apiserver` configuration](/docs/configuration/huatuo-apiserver-configuration_en.md) for field details.

Set resource limits with `CPUQuota`, `MemoryMax`, and `TasksMax` in the service unit. Configure `[Runtime]` only when running directly with `--enable-cgroup`.

### 5. Reload Service Configuration

Reload the systemd configuration:

```bash
sudo systemctl daemon-reload
```

### 6. Start the Services

Start the services and enable them at boot:

```bash
sudo systemctl enable --now huatuo-bamai huatuo-apiserver
```

## RPM Package

The OpenCloudOS repository provides HUATUO v2.1.0 RPM packages for x86_64 and aarch64. These packages install the HUATUO files and systemd service units.

### 1. Download the RPM

Download the package for your host architecture:

x86_64：

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

aarch64：

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/aarch64/os/Packages/huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 2. Install the RPM

x86_64：

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

aarch64：

```bash
sudo dnf install ./huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 3. Edit the Configuration

Edit `/etc/huatuo-bamai/huatuo-bamai.conf` for your environment. See the [`huatuo-bamai` configuration](/docs/configuration/huatuo-bamai-configuration_en.md) for field details.

Set resource limits with `CPUQuota`, `MemoryMax`, and `TasksMax` in the service unit. Configure `[Runtime]` only when running directly with `--enable-cgroup`.

### 4. Start the Service

The RPM installs `huatuo-bamai.service`. Start the service and enable it at boot:

```bash
sudo systemctl enable --now huatuo-bamai
```

> See the [RPM installation guide](https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ) for the full instructions.

## Resource Limits and Verification

Systemd manages resource limits and the process lifecycle for `huatuo-bamai.service`. The service unit does not enable Huatuo's own cgroup by default. It uses these native settings:

```ini
[Service]
CPUAccounting=yes
CPUQuota=200%
MemoryAccounting=yes
MemoryMax=2G
TasksAccounting=yes
TasksMax=32768
KillMode=control-group
```

`CPUQuota=200%` allows up to 2 CPU cores; `MemoryMax=2G` limits service memory. Adjust these values for host capacity, collection tasks, and peak usage. Do not pass `--enable-cgroup` in systemd deployments, as it moves the process out of the service cgroup.

After starting the service, check its status and cgroup:

```bash
systemctl status huatuo-bamai --no-pager
systemd-cgls --unit huatuo-bamai.service
```
