---
title: 集群部署
type: docs
description:
author: HUATUO Team, hao022
date: 2026-01-11
weight: 2
---

本文介绍如何通过 Kubernetes DaemonSet 将华佗采集器部署到集群。

## 1. Kubernetes 清单部署

### 1.1 获取配置文件

```bash
curl -L -o huatuo-bamai.conf https://github.com/ccfos/huatuo/raw/main/huatuo-bamai.conf
```

### 1.2 修改配置文件

根据实际部署环境修改配置文件，例如调整存储后端、Pod 信息获取方式等配置项，详见[配置指南](/docs/configuration/huatuo-bamai-configuration_zh.md)。

### 1.3 创建 ConfigMap

```bash
kubectl create configmap huatuo-bamai-config \
  --namespace default \
  --from-file=./huatuo-bamai.conf \
  --dry-run=client -o yaml |
kubectl apply -f -
```

### 1.4 部署采集器

下载 DaemonSet 清单：

```bash
curl -L -o huatuo-daemonset.yaml \
  https://raw.githubusercontent.com/ccfos/huatuo/main/build/huatuo-daemonset.minimal.yaml
```

部署到生产环境前，将 `huatuo` 容器的 `resources` 调整为以下初始基线：

```yaml
resources:
  limits:
    cpu: "2"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 1Gi
```

应用修改后的清单：

```bash
kubectl apply -f ./huatuo-daemonset.yaml
```

`requests` 提供调度保障，`limits` 限制异常任务对节点的影响。`4 GiB` 容器内存
上限为默认的 `2048 MiB` 进程上限预留运行时空间，降低 OOM 风险。

示例值为初始基线，应根据节点规格、采集任务和资源峰值调整。容器上限不得低于
`huatuo-bamai.conf` 中的 `[Runtime]` 进程上限。

### 1.5 验证部署

```bash
kubectl rollout status daemonset/huatuo \
  --namespace default \
  --timeout=10m

kubectl get pods \
  --namespace default \
  --selector app=huatuo \
  --output wide

kubectl get daemonset huatuo \
  --namespace default \
  --output jsonpath='{.spec.template.spec.containers[?(@.name=="huatuo")].resources}'
```

更新 `huatuo-bamai.conf` 后，重新执行 1.3 节更新 ConfigMap，并手工触发滚动更新：

```bash
kubectl rollout restart daemonset/huatuo --namespace default
```

## 2. Helm 部署

Helm Chart 位于 `build/charts/`。

### 2.1 检查部署环境

管理机需要安装 Helm 和 kubectl，并能够访问目标 Kubernetes 集群。

```bash
(command -v helm || curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash) && helm version

TARGET_CONTEXT="$(kubectl config get-contexts -o name | sed -n '1p')"
kubectl --context "${TARGET_CONTEXT}" get nodes
```

确认目标 Node 状态为 `Ready`。

### 2.2 准备配置文件

按照 1.1 和 1.2 节获取并修改 `huatuo-bamai.conf`。

### 2.3 配置部署参数

创建 `values-production.yaml`：

```yaml
image:
  repository: <所有Node均可访问的镜像仓库>/huatuo-bamai
  tag: "<固定发行版本>"
  pullPolicy: IfNotPresent

resources:
  limits:
    cpu: "2"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 1Gi

nodeSelector:
  kubernetes.io/os: linux

tolerations:
  - operator: Exists

hostPaths:
  proc: /proc
  sys: /sys
  run: /run
  var: /var
  etc: /etc
  data: /var/log/huatuo/huatuo-local
```

### 2.4 检查 Helm Chart

```bash
helm lint ./build/charts \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf

helm template huatuo ./build/charts \
  --namespace huatuo \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf \
  >/dev/null
```

### 2.5 部署采集器

```bash
helm upgrade --install huatuo ./build/charts \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  --create-namespace \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf \
  --atomic \
  --timeout 10m
```

### 2.6 验证部署

```bash
helm status huatuo \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo

kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  get daemonset,configmap,pod --output wide

kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  rollout status daemonset/huatuo \
  --timeout=10m
```

查看采集器日志：

```bash
kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  logs \
  --selector app.kubernetes.io/name=huatuo \
  --prefix \
  --tail=100
```

### 2.7 更新和回滚

修改镜像版本或 `huatuo-bamai.conf` 后，重新执行 2.5 节的命令。

查看发布历史并回滚到指定版本：

```bash
helm history huatuo \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo

helm rollback huatuo <Revision> \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  --wait \
  --timeout 10m
```
