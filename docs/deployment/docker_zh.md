---
title: Docker Compose 容器部署
type: docs
description: 
author: HUATUO Team, hao022
date: 2026-01-11
weight: 1
---

### 镜像下载
镜像仓库地址：https://hub.docker.com/r/huatuo/huatuo-bamai/tags

### 使用 Docker 启动容器

```bash
$ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️：注意：此方式使用容器内置的默认配置文件，该配置不会连接 kubelet 与 Elasticsearch。

### 使用 Docker Compose 启动容器

通过 [Docker Compose](https://docs.docker.com/compose/) 可在本地快速搭建一套完整环境，自行管理采集器、Elasticsearch、Prometheus、Pyroscope、Grafana 等组件。

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

> Docker Compose 安装方法请参阅 https://docs.docker.com/compose/install/linux/。

如需在不启动 `huatuo-apiserver` 的情况下展示 AutoTracing CPU 火焰图，请参阅 [AutoTracing Pyroscope 火焰图](../development/autotracing_pyroscope_zh.md)。
