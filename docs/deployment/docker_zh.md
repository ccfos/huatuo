---
title: 容器部署
type: docs
description: 
author: HUATUO Team, hao022
date: 2026-01-11
weight: 1
---

本文说明使用 Docker 运行采集器，或通过 Compose 启动完整环境。镜像版本见[镜像仓库](https://hub.docker.com/r/huatuo/huatuo-bamai/tags)。

## Docker

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

## Docker Compose

通过 [Docker Compose](https://docs.docker.com/compose/) 可在本地快速搭建一套完整环境，自行管理采集器、Elasticsearch、Prometheus、Grafana 等组件。

```bash
$ docker compose --project-directory ./build/docker up
```

Docker Compose 安装方法见[安装说明](https://docs.docker.com/compose/install/linux/)。

Compose 自动安装 IO 周期总览使用的 Business Charts（`volkovlabs-echarts-panel`）；
使用已有 Grafana 实例时，需自行安装该插件。

### 开发栈持续性能剖析

`make compose-dev-up` 使用 `build/docker/devconf/`，已配置 ES 存储、API 鉴权和
Grafana 数据源。固定凭据仅用于开发环境；修改 `.env` 的 ES 密码时，同步修改
两份开发配置和 Grafana ES 数据源。

启动开发栈后，在另一个终端从仓库根目录运行（需要 Bash、`curl`、`jq`）：

```bash
./build/docker/run_test_profiling.sh
```

脚本自动创建 60 秒宿主机 CPU 采集任务，等待完成，查询原始结果和火焰图，
保存到 `_output/profiling/run.XXXXXX/`，并输出已选定任务及时间范围的 Grafana 地址。
采集期间运行待分析的业务负载；Grafana 首次登录为 `admin / admin`。

脚本固定使用本地开发栈配置；若修改服务地址、节点名或凭据，同步调整脚本顶部常量。
更多参数见 [Profiling API](/docs/reference/profiling-api_zh.md)。
使用 `make compose-dev-down` 停止开发栈。
