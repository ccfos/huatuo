---
title: 容器部署
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
$ docker run --privileged --pid=host --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️：注意：此方式使用容器内置的默认配置文件，该配置不会连接 kubelet 与 Elasticsearch。

### 使用 Docker Compose 启动容器

通过 [Docker Compose](https://docs.docker.com/compose/) 可以启动完整服务，
也可以仅启动 profiling 所需服务。

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

`full` profile 会启动 huatuo-bamai、Elasticsearch、Prometheus、Pyroscope、
Grafana 和 huatuo-apiserver。

如果只需将 profiling 数据写入 Pyroscope：

```bash
$ COMPOSE_PROFILES=profiling docker compose --project-directory ./build/docker up
```

`profiling` profile 只启动 huatuo-bamai、Pyroscope 和 Grafana，不启动或等待
Elasticsearch、Prometheus、huatuo-apiserver。huatuo-bamai 会禁用 kubelet
发现，因此无需 kubelet 客户端证书。Grafana 地址为 http://localhost:3000，
数据源选择 `huatuo-bamai-pyroscope`。

> Docker Compose 安装方法请参阅 https://docs.docker.com/compose/install/linux/。
