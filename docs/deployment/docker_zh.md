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
  --memory=2g \
  --volume /sys:/sys \
  --volume /proc:/proc \
  --volume /run:/run \
  huatuo/huatuo-bamai:latest
```

> 注意：容器内置的默认配置不会连接 kubelet 和 Elasticsearch。

生产环境应限制 CPU 和内存，避免异常采集任务影响宿主机业务。Docker 的容器 cgroup
负责资源限制；Huatuo 默认不创建自身 cgroup，也不要在 Docker 部署中传入
`--enable-cgroup`。

通过以下命令确认限制已经生效，并观察实际使用量：

```bash
docker inspect huatuo-bamai \
  --format 'NanoCPUs={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
docker stats huatuo-bamai
```

示例值为初始基线，应根据节点规格、采集任务和资源峰值调整。

### 使用 Docker Compose 启动容器

通过 [Docker Compose](https://docs.docker.com/compose/) 可以启动完整服务，
也可以仅启动 profiling 所需服务。

```bash
$ docker compose --project-directory ./build/docker up
```

默认的 `profiling` profile 只启动 huatuo-bamai、Pyroscope 和 Grafana，
不启动或等待 Elasticsearch、Prometheus、huatuo-apiserver。huatuo-bamai
会禁用 kubelet 发现，因此无需 kubelet 客户端证书；它会在不重新构建
镜像的情况下写入
`AutoTracing.Display.Backend = "pyroscope"`。Grafana 地址为
http://localhost:3000，
可打开 `HuaTuo AutoTracing Pyroscope Flamegraph` Dashboard。CPUIdle 或
CPUSys 达到配置的 AutoTracing 阈值后，Dashboard 才会出现 profile。

如需使用现有 huatuo-apiserver 展示链路：

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

`full` profile 会启动 huatuo-bamai、Elasticsearch、Prometheus、Pyroscope、
Grafana 和 huatuo-apiserver，并在运行时配置中写入
`AutoTracing.Display.Backend = "apiserver"`。

> Docker Compose 安装方法请参阅 https://docs.docker.com/compose/install/linux/。
