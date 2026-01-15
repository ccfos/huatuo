---
title: Docker 容器部署
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 1
---

### 镜像下载
镜像存储地址: [https://hub.docker.com/r/huatuo/huatuo-bamai/tags](https://hub.docker.com/r/huatuo/huatuo-bamai/tags)。

### docker 启动容器

```bash
docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️：该方式使用容器内的默认配置文件，容器内的默认配置不会连接 kubelet 和 ES。

### docker compose 启动容器
通过[docker compose](https://docs.docker.com/compose/) 方式，可以在本地快速搭建部署一套完整的环境自行管理采集器、ES、prometheus、grafana 等组件。

```bash
docker compose --project-directory ./build/docker up
```

> 安装docker compose 参考 [https://docs.docker.com/compose/install/linux/](https://docs.docker.com/compose/install/linux/)
