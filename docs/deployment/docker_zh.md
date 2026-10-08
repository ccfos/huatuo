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

通过 [Docker Compose](https://docs.docker.com/compose/) 可在本地快速搭建一套完整环境，自行管理采集器、Elasticsearch、Prometheus、Grafana 等组件。

```bash
$ docker compose --project-directory ./build/docker up
```

> Docker Compose 安装方法请参阅 https://docs.docker.com/compose/install/linux/。

Compose 使用命名卷 `elasticsearch-data`、`prometheus-data` 和 `grafana-data`
保存 Elasticsearch 数据、Prometheus 历史和 Grafana 状态。保持相同的 Compose 项目名，
以继续使用这些卷。重建容器或执行普通的 `docker compose down` 会保留数据卷；
`docker compose down --volumes` 会删除数据卷，开发清理命令 `make compose-dev-down`
也会删除它们。

将此 Compose 文件用于尚未配置命名数据卷的已有部署前，请先备份各服务数据，并规划如何
恢复到新卷。旧容器可写层或匿名卷中的数据不会自动迁移。请使用各服务支持的备份和恢复
流程，确认数据恢复成功后再删除旧容器或旧卷。
