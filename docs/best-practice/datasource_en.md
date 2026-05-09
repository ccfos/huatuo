---
title: Data Source Configuration
type: docs
description:
author: HUATUO Team
date: 2026-05-05
weight: 2
---

HUATUO supports integrating with Prometheus for metrics collection and Elasticsearch for log storage. This document describes how to configure data sources and import dashboards in Grafana.

### Metrics Collection

#### 1. Port Forwarding for Testing

```bash
$ kubectl port-forward -n default --address=0.0.0.0 pod/huatuo-XXXX 19704:19704
```

#### 2. Verify Metrics Endpoint

Access the metrics endpoint to verify it's working:

```
http://172.16.20.113:19704/metrics
```

If metrics are displayed, the service is running correctly.

#### 3. Configure Prometheus Scraping

There are two approaches to configure Prometheus for scraping HUATUO metrics:

**Option 1: Using Annotations**

Add annotations to the Pod template metadata:

```yaml
template:
    metadata:
      annotations:                     
        prometheus.io/scrape: "true"
        prometheus.io/port: "19704"
        prometheus.io/path: "/metrics"
```

**Option 2: Using ServiceMonitor**

Create `huatuo-service.yaml`:

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

Create `huatuo-servicemonitor.yaml`:

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

#### 4. Query Metrics in Prometheus

Use the following pattern to query HUATUO metrics:

```bash
huatuo_*
```

If results are returned, metrics collection is working properly.

### Log Collection

Query logs from Elasticsearch:

```bash
$ curl -u elastic:123456 "http://172.16.15.118:9200/huatuo_bamai/_search?pretty"
```

### Grafana Data Source Configuration

#### 1. Configure Prometheus Data Source

Refer to `build/docker/datasource/` for detailed configuration files.

#### 2. Configure Elasticsearch Data Source

In Grafana, add a new Elasticsearch data source with the following settings:

- **URL**: `http://172.16.15.118:9200`
- **Authentication**: Basic Authentication
- **Username**: `elastic`
- **Password**: `123456`
- **Index name**: `huatuo_bamai`
- **Time field name**: `uploaded_time`

### Dashboard Import

#### 1. Export Dashboard from Console

1. Access `http://console.huatuo.tech/dashboards` (Username: `huatuo`, Password: `huatuo1024`)
2. Select the desired dashboard
3. Click **Export** -> **Export as JSON**
4. Check "Export the dashboard to use in another instance"
5. Click **Copy to clipboard**

#### 2. Import Dashboard to Local Grafana

1. In your local Grafana, navigate to **Dashboards** -> **Import**
2. Paste the copied JSON content
3. Click **Load**
4. Configure data sources and click **Import**

### Troubleshooting

**Issue**: "datasource not found" error when importing the "HuaTuo Root Cause Analysis AutoTracing" dashboard.

**Solution**: 
1. Manually replace the datasource UID in the dashboard JSON
2. Find your Elasticsearch datasource UID from the URL (e.g., `dflcs0w2ghybka` from `http://172.16.15.118:3000/connections/datasources/edit/dflcs0w2ghybka`)
3. Replace all occurrences of `"uid": "${DS_HUATUO-BAMAI-ES}"` with your actual datasource UID
4. Re-import the dashboard