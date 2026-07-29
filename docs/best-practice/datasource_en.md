---
title: Data Source
type: docs
description:
author: HUATUO Team
date: 2026-05-05
weight: 2
---

HUATUO integrates with Prometheus for metrics collection and Elasticsearch for log storage. This document covers data source configuration and dashboard provisioning in Grafana.

Two deployment paths are supported:

- **Docker Compose** — recommended for development and testing; all components are pre-configured.
- **Kubernetes** — for production clusters; requires manual data source configuration.

## Quick Start (Docker Compose)

The `build/docker/` directory contains a complete stack. All default credentials and ports listed below match this setup.

```bash
cd build/docker
docker compose up -d
```

This starts four services on the host network:

| Service | Port | Purpose |
|---|---|---|
| Elasticsearch | 9200 | Log storage |
| Prometheus | 9090 | Metrics collection |
| Grafana | 3000 | Visualization |
| huatuo-bamai | 19704 | Agent (metrics + tracing) |

**Default credentials:**

| Service | Username | Password |
|---|---|---|
| Elasticsearch | `elastic` | `huatuo-bamai` |
| Grafana | `admin` | `admin` |

Data sources and dashboards are auto-provisioned. Access Grafana at `http://<host>:3000`.

### Verify the Stack

```bash
# Elasticsearch
curl -s -u elastic:huatuo-bamai http://localhost:9200/_cluster/health?pretty

# Prometheus — should show huatuo target as "up"
curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | {job: .labels.job, health: .health}'

# Grafana
curl -s http://localhost:3000/api/health | jq .version

# HuaTuo metrics
curl -s http://localhost:19704/metrics | head -5
```

### Provisioned Data Sources

The following data sources are created automatically via `build/docker/grafana/datasources/`:

| Name | Type | UID |
|---|---|---|
| huatuo-bamai-prom | Prometheus | `huatuo-bamai-prom` |
| huatuo-bamai-es | Elasticsearch | `huatuo-bamai-es` |
| huatuo-bamai-infinity | Infinity | `huatuo-bamai-infinity-auto-flamegraph` |

### Provisioned Dashboards

Six dashboards are loaded from `build/docker/grafana/dashboards/`:

- Metric Dashboard — Host View
- Metric Dashboard — Container View
- HuaTuo Root Cause Analysis AutoTracing
- Continuous Profiling (Host)
- Continuous Profiling (Container)
- AutoTracing Flame Redirect

No manual import is needed when using Docker Compose.

## Metrics Collection (Kubernetes)

### 1. Verify Metrics Endpoint

After deploying huatuo-bamai to Kubernetes, expose the metrics endpoint:

```bash
kubectl port-forward -n default --address=0.0.0.0 pod/huatuo-XXXX 19704:19704
```

Verify:

```bash
curl http://localhost:19704/metrics
```

Metrics output confirms the agent is running correctly.

### 2. Configure Prometheus Scraping

**Option A: Pod Annotations**

Add annotations to the Pod template metadata. This requires a Prometheus setup with Kubernetes pod service discovery enabled (e.g., `kubernetes_sd_configs` with `role: pod`).

```yaml
template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "19704"
        prometheus.io/path: "/metrics"
```

**Option B: ServiceMonitor**

Requires [Prometheus Operator](https://prometheus-operator.dev/). Create two resources:

`huatuo-service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: huatuo
  labels:
    app: huatuo
spec:
  clusterIP: None
  ports:
    - name: metrics
      port: 19704
      targetPort: 19704
      protocol: TCP
  selector:
    app: huatuo
```

`huatuo-servicemonitor.yaml`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: huatuo
  namespace: default
  labels:
    release: prometheus
spec:
  namespaceSelector:
    matchNames:
      - default
  selector:
    matchLabels:
      app: huatuo
  endpoints:
    - port: metrics
      path: /metrics
      interval: 30s
      scrapeTimeout: 10s
```

### 3. Query Metrics in Prometheus

```promql
huatuo_*
```

If results are returned, metrics collection is working properly.

## Log Collection (Kubernetes)

Query logs from Elasticsearch:

```bash
curl -u elastic:<password> "http://<es-host>:9200/huatuo_bamai/_search?pretty"
```

Replace `<password>` and `<es-host>` with your Elasticsearch credentials and address.

## Manual Grafana Data Source Configuration

When not using Docker Compose (e.g., external Grafana), configure data sources manually.

### Prometheus Data Source

Refer to `build/docker/grafana/datasources/prometheus.yaml` for the provisioning file, or configure via Grafana UI:

- **URL**: `http://<prometheus-host>:9090`
- **Access**: Server (proxy)

### Elasticsearch Data Source

Configure via Grafana UI or provisioning:

- **URL**: `http://<es-host>:9200`
- **Authentication**: Basic Authentication
- **Username**: `elastic`
- **Password**: `<your-elasticsearch-password>`
- **Index name**: `huatuo_bamai`
- **Time field name**: `uploaded_time`

## Dashboard Import

When using Docker Compose, dashboards are provisioned automatically. To import additional dashboards from the HUAUO console:

1. Access `http://console.huatuo.tech/dashboards` (Username: `huatuo`, Password: `huatuo1024`)
2. Select the desired dashboard
3. Click **Export** -> **Export as JSON**
4. Check "Export the dashboard to use in another instance"
5. Click **Copy to clipboard**

Then in your Grafana instance:

1. Navigate to **Dashboards** -> **Import**
2. Paste the JSON content
3. Click **Load**
4. Select the correct data sources and click **Import**

## Troubleshooting

### "datasource not found" when importing dashboard

This occurs when the dashboard JSON references a datasource UID that does not exist in your Grafana instance.

**Solution:**

1. Find your Elasticsearch datasource UID from the Grafana UI URL: `http://<grafana-host>:3000/connections/datasources/edit/<uid>`
2. In the dashboard JSON, replace all occurrences of `"uid": "${DS_HUATUO-BAMAI-ES}"` with your actual UID
3. Re-import the dashboard

### Prometheus target shows "down"

- Verify huatuo-bamai is running: `curl http://<host>:19704/metrics`
- Check Prometheus configuration matches the agent's address and port
- For Kubernetes: ensure pod annotations are correct or ServiceMonitor selector matches

### Elasticsearch index is empty

- Verify Elasticsearch is reachable: `curl -u elastic:<password> http://<host>:9200/_cat/indices`
- Check huatuo-bamai config `[Storage.ES]` section has correct `Address`, `Username`, `Password`
- Default index name is `huatuo_bamai`

### "socket path already exists" on startup

This occurs when a previous huatuo-bamai process was not cleanly stopped.

**Solution:**

```bash
rm -f /var/run/huatuo-toolstream.sock
```
