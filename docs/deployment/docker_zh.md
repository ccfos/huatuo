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
docker run --detach \
  --name huatuo-bamai \
  --restart unless-stopped \
  --privileged \
  --pid=host \
  --cgroupns=host \
  --network=host \
  --cpus=2 \
  --memory=4g \
  --volume /sys:/sys \
  --volume /proc:/proc \
  --volume /run:/run \
  huatuo/huatuo-bamai:latest
```

> 注意：容器内置的默认配置不会连接 kubelet 和 Elasticsearch。

生产环境应限制 CPU 和内存，避免异常采集任务影响宿主机业务。`4 GiB` 容器
内存上限为默认的 `2048 MiB` 进程上限预留运行时空间，降低 OOM 风险。

通过以下命令确认限制已经生效，并观察实际使用量：

```bash
docker inspect huatuo-bamai \
  --format 'NanoCPUs={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
docker stats huatuo-bamai
```

示例值为初始基线，应根据节点规格、采集任务和资源峰值调整。

### 使用 Docker Compose 启动容器

通过 [Docker Compose](https://docs.docker.com/compose/) 可在本地快速搭建一套完整环境，自行管理采集器、Elasticsearch、Prometheus、Grafana 等组件。

```bash
$ docker compose --project-directory ./build/docker up
```

> Docker Compose 安装方法请参阅 https://docs.docker.com/compose/install/linux/。
