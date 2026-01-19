---
title: Systemd 物理机部署
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 3
---

### 1. 腾讯云下载

腾讯操作系统 OpenCloudOS 提供 [HUATUO 安装包](https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm)

```bash
wget https://mirrors.opencloudos.tech/epol/9/Everything/x86_64/os/Packages/huatuo-bamai-2.1.0-2.oc9.x86_64.rpm  
wget https://mirrors.opencloudos.tech/epol/9/Everything/aarch64/os/Packages/huatuo-bamai-2.1.0-2.oc9.aarch64.rpm
```

### 2. 安装 RPM
```bash
sudo rpm -ivh huatuo-bamai*.rpm
```

### 3. 启动华佗
```bash
sudo systemctl start huatuo-bamai
sudo systemctl enable huatuo-bamai
```

> 完整安装可参考[https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ](https://mp.weixin.qq.com/s/Gmst4_FsbXUIhuJw1BXNnQ)

