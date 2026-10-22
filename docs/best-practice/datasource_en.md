---
title: Data Sources
type: docs
description:
author: HUATUO Team
date: 2026-05-05
weight: 2
---

Prometheus scrapes metrics exposed by HUATUO, and Elasticsearch stores event data. This guide covers data sources and dashboards for Docker Compose and Kubernetes deployments.

> Do not run Docker Compose, systemd, and Kubernetes instances of huatuo-bamai on the same node at the same time. They compete for port `19704` and `/var/run/huatuo-toolstream.sock`.

## Docker Compose

`build/docker/` provides preconfigured Prometheus, Elasticsearch, Grafana, and HUATUO components.

```bash
export ELASTIC_PASSWORD=huatuo-bamai
docker compose --project-directory ./build/docker up -d
```

Services use host networking:

| Service | Address | Purpose |
|---|---|---|
| Elasticsearch | `http://localhost:9200` | Event storage |
| Prometheus | `http://localhost:9090` | Metric collection |
| Grafana | `http://localhost:3000` | Visualization |
| huatuo-apiserver | `http://localhost:12740` | Profiling/Tracing task APIs and flame graph queries |
| huatuo-bamai | `http://localhost:19704` | Metric and event collection |

Grafana's default credentials are `admin/admin`. Change the password immediately after the first login.

### Preconfigured Data Sources

Data sources are loaded automatically from `build/docker/grafana/datasources/`:

| Name | Type | UID |
|---|---|---|
| `huatuo-bamai-prom` | Prometheus | `huatuo-bamai-prom` |
| `huatuo-bamai-es` | Elasticsearch | `huatuo-bamai-es` |
| `huatuo-bamai-infinity` | Infinity | `huatuo-bamai-infinity-auto-flamegraph` |
| `huatuo-apiserver-pyroscope` | Pyroscope | `huatuo-apiserver-pyroscope` |

### Preconfigured Dashboards

Dashboards are loaded automatically from `build/docker/grafana/dashboards/`:

- Metric dashboard (host)
- Metric dashboard (container)
- Disk IO dashboard (continuous monitoring)
- HuaTuo root-cause analysis AutoTracing
- Continuous Profiling (host)
- Continuous Profiling (container)
- AutoTracing Flame Redirect

Docker Compose requires no manual data source configuration or dashboard import.

## Kubernetes

Prometheus can scrape HUATUO metrics through Pod annotations or a ServiceMonitor. Choose one method to avoid duplicate collection.

### Pod Annotations

`build/huatuo-daemonset.minimal.yaml` includes these annotations by default:

```yaml
template:
  metadata:
    annotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "19704"
      prometheus.io/path: "/metrics"
```

The Helm chart generates annotations from the following settings in `build/charts/values.yaml`:

```yaml
metrics:
  enabled: true
  port: 19704
  path: /metrics
```

Enable Kubernetes Pod service discovery in Prometheus and use relabeling to read the annotations:

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

ServiceMonitor requires a cluster with Prometheus Operator and the ServiceMonitor CRD installed. This example assumes both the Helm release and namespace are named `huatuo`. If your names differ, update the namespace and `app.kubernetes.io/instance` label accordingly.

Create `huatuo-service.yaml`:

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

Create `huatuo-servicemonitor.yaml`:

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

`release: prometheus` must match the Prometheus instance's `spec.serviceMonitorSelector`. Different Prometheus Operator installations may use different labels.

With `build/huatuo-daemonset.minimal.yaml`, Pods use the label `app: huatuo`. Change the Service's `spec.selector` to:

```yaml
selector:
  app: huatuo
```

ServiceMonitor selects the Service's `metadata.labels`, not Pod labels.

Apply the configuration:

```bash
kubectl apply -f huatuo-service.yaml
kubectl apply -f huatuo-servicemonitor.yaml
```

## Grafana Data Sources

Without Docker Compose, add the data sources manually in Grafana.

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
- Time field name：`uploaded_timestamp`
- UID：`huatuo-bamai-es`

See `build/docker/grafana/datasources/` for provisioning examples.

## Import Dashboards

Docker Compose loads the repository's dashboards automatically. For an external Grafana instance:

1. Open **Dashboards** -> **Import** in Grafana.
2. Upload or paste `build/docker/grafana/dashboards/*.json`.
3. Click **Load**.
4. Select the `huatuo-bamai-prom` and `huatuo-bamai-es` data sources.
5. Click **Import**.

You can also export dashboards from the HUATUO console:

1. Visit `http://console.huatuo.tech/dashboards`.
2. Log in and select a dashboard.
3. Click **Export** -> **Export as JSON**.
4. Select "Export the dashboard to use in another instance".
5. Copy the JSON and import it into the target Grafana instance.

Console credentials are provided by the deployment administrator and should not be stored in public documentation.
