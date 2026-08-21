---
title: 数据源配置
type: docs
description:
author: HUATUO Team
date: 2026-05-05
weight: 2
---

HUATUO 通过 Prometheus 采集指标，并将事件数据写入 Elasticsearch。本文介绍 Docker Compose 和 Kubernetes 环境下的数据源与仪表盘配置方法。

> 不要在同一节点同时运行 Docker Compose、systemd 和 Kubernetes 版本的
> huatuo-bamai。它们会争用 `19704` 端口和
> `/var/run/huatuo-toolstream.sock`。

## Docker Compose

`build/docker/` 提供已配置完成的 Prometheus、Elasticsearch、Grafana 和 HUATUO 组件。

```bash
export ELASTIC_PASSWORD=huatuo-bamai
docker compose --project-directory ./build/docker up -d
```

服务使用宿主机网络：

| 服务 | 地址 | 用途 |
|---|---|---|
| Elasticsearch | `http://localhost:9200` | 事件存储 |
| Prometheus | `http://localhost:9090` | 指标采集 |
| Grafana | `http://localhost:3000` | 可视化 |
| huatuo-bamai | `http://localhost:19704` | 指标和事件采集 |

Grafana 默认账号为 `admin/admin`。首次登录后应立即修改密码。

### 已预配置的数据源

数据源由 `build/docker/grafana/datasources/` 自动加载：

| 名称 | 类型 | UID |
|---|---|---|
| `huatuo-bamai-prom` | Prometheus | `huatuo-bamai-prom` |
| `huatuo-bamai-es` | Elasticsearch | `huatuo-bamai-es` |
| `huatuo-bamai-infinity` | Infinity | `huatuo-bamai-infinity-auto-flamegraph` |

### 已预配置的仪表盘

仪表盘由 `build/docker/grafana/dashboards/` 自动加载：

- Metric 大盘（宿主机）
- Metric 大盘（容器）
- HuaTuo 根因定位 AutoTracing
- Continuous Profiling（宿主机）
- Continuous Profiling（容器）
- AutoTracing Flame Redirect

使用 Docker Compose 时不需要手动配置数据源或导入仪表盘。

## Kubernetes

Prometheus 可以通过 Pod 注解或 ServiceMonitor 采集 HUATUO 指标。两种方式选择一种即可，避免重复采集。

### Pod 注解

`build/huatuo-daemonset.minimal.yaml` 默认包含以下注解：

```yaml
template:
  metadata:
    annotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "19704"
      prometheus.io/path: "/metrics"
```

Helm Chart 根据 `charts/huatuo/values.yaml` 中的以下配置生成注解：

```yaml
metrics:
  enabled: true
  port: 19704
  path: /metrics
```

Prometheus 需要启用 Kubernetes Pod 服务发现，并通过 relabel 读取注解：

```yaml
scrape_configs:
  - job_name: k8s-pods
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
        action: keep
        regex: "true"
      - source_labels:
          [__address__, __meta_kubernetes_pod_annotation_prometheus_io_port]
        action: replace
        regex: ([^:]+)(?::\d+)?;(\d+)
        replacement: $1:$2
        target_label: __address__
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
        action: replace
        regex: (.+)
        target_label: __metrics_path__
      - source_labels: [__meta_kubernetes_namespace]
        action: replace
        target_label: kubernetes_namespace
      - source_labels: [__meta_kubernetes_pod_name]
        action: replace
        target_label: kubernetes_pod_name
```

### ServiceMonitor

ServiceMonitor 仅适用于安装了 Prometheus Operator 和 ServiceMonitor CRD 的集群。以下示例假设 Helm release 名和命名空间均为 `huatuo`。如果实际名称不同，需要同步修改命名空间和 `app.kubernetes.io/instance` 标签。

创建 `huatuo-service.yaml`：

```yaml
apiVersion: v1
kind: Service
metadata:
  name: huatuo
  namespace: huatuo
  labels:
    app.kubernetes.io/name: huatuo
    app.kubernetes.io/instance: huatuo
spec:
  clusterIP: None
  selector:
    app.kubernetes.io/name: huatuo
    app.kubernetes.io/instance: huatuo
  ports:
    - name: metrics
      port: 19704
      targetPort: 19704
      protocol: TCP
```

创建 `huatuo-servicemonitor.yaml`：

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: huatuo
  namespace: huatuo
  labels:
    release: prometheus
spec:
  namespaceSelector:
    matchNames:
      - huatuo
  selector:
    matchLabels:
      app.kubernetes.io/name: huatuo
      app.kubernetes.io/instance: huatuo
  endpoints:
    - port: metrics
      path: /metrics
      interval: 30s
      scrapeTimeout: 10s
```

`release: prometheus` 必须匹配 Prometheus 实例的 `spec.serviceMonitorSelector`。不同 Prometheus Operator 安装方式可能使用不同标签。

如果使用 `build/huatuo-daemonset.minimal.yaml`，Pod 标签是 `app: huatuo`，需要将 Service 的 `spec.selector` 改为：

```yaml
selector:
  app: huatuo
```

ServiceMonitor 选择的是 Service 的 `metadata.labels`，不是 Pod 标签。

应用配置：

```bash
kubectl apply -f huatuo-service.yaml
kubectl apply -f huatuo-servicemonitor.yaml
```

## Grafana 数据源

不使用 Docker Compose 时，在 Grafana 中手动添加数据源。

### Prometheus

- URL：`http://<prometheus-host>:9090`
- Access：Server (proxy)
- UID：`huatuo-bamai-prom`

### Elasticsearch

- URL：`http://<elasticsearch-host>:9200`
- Authentication：Basic Authentication
- Username：`elastic`
- Password：`<password>`
- Index name：`huatuo_bamai*`
- Time field name：`uploaded_time`
- UID：`huatuo-bamai-es`

Provisioning 配置示例见 `build/docker/grafana/datasources/`。

## 导入仪表盘

Docker Compose 会自动加载仓库内置仪表盘。外部 Grafana 按以下步骤导入：

1. 打开 Grafana 的 **Dashboards** -> **Import**
2. 上传或粘贴 `build/docker/grafana/dashboards/*.json`
3. 点击 **Load**
4. 选择 `huatuo-bamai-prom` 和 `huatuo-bamai-es` 数据源
5. 点击 **Import**

也可以从 HUATUO 控制台导出仪表盘：

1. 访问 `http://console.huatuo.tech/dashboards`
2. 登录并选择仪表盘
3. 点击 **Export** -> **Export as JSON**
4. 勾选 "Export the dashboard to use in another instance"
5. 复制 JSON 并导入目标 Grafana

控制台登录凭证由部署管理员提供，不应保存在公开文档中。
