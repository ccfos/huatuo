---
title: 数据源配置
type: docs
description:
author: admin-dong
date: 2026-05-05
weight: 2
---

HUATUO 支持与 Prometheus 集成进行指标采集，并使用 Elasticsearch 存储日志。本文档介绍如何在 Grafana 中配置数据源和导入仪表盘。

### 指标采集

#### 1. 端口转发测试

```bash
$ kubectl port-forward -n default --address=0.0.0.0 pod/huatuo-XXXX 19704:19704
```

#### 2. 验证指标端点

访问指标端点以验证服务是否正常运行：

```
http://172.16.20.113:19704/metrics
```

如果显示指标数据，说明服务运行正常。

#### 3. 配置 Prometheus 采集

有两种方式配置 Prometheus 采集 HUATUO 指标：

**方案一：使用注解**

在 Pod 模板元数据中添加注解：

```yaml
template:
    metadata:
      annotations:                     
        prometheus.io/scrape: "true"
        prometheus.io/port: "19704"
        prometheus.io/path: "/metrics"
```

**方案二：使用 ServiceMonitor**

创建 `huatuo-service.yaml`：

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

创建 `huatuo-servicemonitor.yaml`：

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

#### 4. 在 Prometheus 中查询指标

使用以下模式查询 HUATUO 指标：

```bash
huatuo_*
```

如果返回结果，说明指标采集配置成功。

### 日志采集

从 Elasticsearch 查询日志：

```bash
$ curl -u elastic:123456 "http://172.16.15.118:9200/huatuo_bamai/_search?pretty"
```

### Grafana 数据源配置

#### 1. 配置 Prometheus 数据源

详细配置文件请参考 `build/docker/datasource/` 目录。

#### 2. 配置 Elasticsearch 数据源

在 Grafana 中添加新的 Elasticsearch 数据源，配置如下：

- **URL**：`http://172.16.15.118:9200`
- **认证方式**：Basic Authentication
- **用户名**：`elastic`
- **密码**：`123456`
- **索引名称**：`huatuo_bamai`
- **时间字段名**：`uploaded_time`

### 仪表盘导入

#### 1. 从控制台导出仪表盘

1. 访问 `http://console.huatuo.tech/dashboards`（用户名：`huatuo`，密码：`huatuo1024`）
2. 选择所需的仪表盘
3. 点击 **Export** -> **Export as JSON**
4. 勾选 "Export the dashboard to use in another instance"
5. 点击 **Copy to clipboard**

#### 2. 导入仪表盘到本地 Grafana

1. 在本地 Grafana 中，导航到 **Dashboards** -> **Import**
2. 粘贴复制的 JSON 内容
3. 点击 **Load**
4. 配置数据源并点击 **Import**

### 故障排除

**问题**：导入 "HuaTuo 根因定位 AutoTracing" 仪表盘时出现 "datasource not found" 错误。

**解决方案**：
1. 手动替换仪表盘 JSON 中的数据源 UID
2. 从 URL 中查找 Elasticsearch 数据源 UID（例如从 `http://172.16.15.118:3000/connections/datasources/edit/dflcs0w2ghybka` 中获取 `dflcs0w2ghybka`）
3. 将所有 `"uid": "${DS_HUATUO-BAMAI-ES}"` 替换为实际的数据源 UID
4. 重新导入仪表盘