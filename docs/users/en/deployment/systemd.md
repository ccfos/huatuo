---
title: Systemd
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

## Install by RPM

> OpenCloudOS currently provides the v2.1.0 RPM package; the master is for reference only.

Tencent OpenCloudOS provides an official HUATUO package:  
https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm  
This allows HUATUO to be quickly installed and enabled on OpenCloudOS.

- **x86_64 architecture**
```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm
```

- **arm64 architecture**
```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/aarch64/os/Packages/huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

- **Install HUATUO on OC8**
```bash
sudo rpm -ivh huatuo-bamai*.rpm
```

Other RPM-based operating systems can install HUATUO the same way.  
As usual, you must update the config file according to your environment (e.g., kubelet connection, Elasticsearch settings).

> Full OpenCloudOS installation guide:  
> https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ

## Install by Binary Package

> The latest binary package provided is v2.1.0; the master branch is for reference only.

You can also download the binary package and configure/manage it manually.  
Again, update the configuration file based on your actual environment (kubelet connection, Elasticsearch settings, etc.).

- v2.1.0: https://github.com/ccfos/huatuo/releases/tag/v2.1.0
