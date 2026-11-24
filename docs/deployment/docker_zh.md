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

Compose 自动安装 IO 周期总览和性能剖析标签过滤使用的 Business Charts（`volkovlabs-echarts-panel`）；
使用已有 Grafana 实例时，需自行安装该插件。

### 开发栈持续性能剖析

在仓库根目录启动已配置 ES 存储、API 鉴权和 Grafana 数据源的开发栈：

```bash
make compose-dev-up
```

固定凭据仅用于开发环境；修改 `build/docker/.env` 的 ES 密码时，同步修改
`build/docker/devconf/` 下两份服务配置和 Grafana ES 数据源。

采集脚本需要 Bash、`curl`、`jq`。容器采集还需要 `kubectl`、`ip`、本机可用的 Kubernetes
节点和 `gcc:14` 镜像访问权限。启动后，在另一个终端从仓库根目录选择采集范围，每个任务持续 20 秒：

```bash
# 采集现有宿主机负载
./build/docker/run_test_profiling.sh host
# 创建临时容器负载并采集
./build/docker/run_test_profiling.sh container
```

省略参数时依次采集宿主机和容器；不具备 Kubernetes 条件时使用 `host`。

完成后打开输出中的 Grafana 地址查看火焰图，首次登录为 `admin / admin`。
结果和诊断文件保存在输出的 `output_dir`（位于 `_output/profiling/run.XXXXXX/`）。
容器临时负载在退出时清理，已采集结果仍可查询。标签过滤操作见
[查看火焰图](/docs/best-practice/continuous-profiling_zh.md#5-查看火焰图)。

脚本用于默认开发栈；自定义采集请求见 [Profiling API](/docs/reference/profiling-api_zh.md)。
试用结束后运行 `make compose-dev-down` 停止并移除开发栈。
