---
title: Quick Start
description: 
date: 2026-07-27
type: docs
weight: 1
---

Choose one of three paths: [view the demo](#view-the-demo), [run containers](#run-containers), or [build from source](#build-from-source). Running the collector locally requires Linux. Container deployment also requires Docker and the permissions used in the examples.

## View the Demo

Log in to the [demo site](http://console.huatuo.tech) to view kernel metrics, abnormal events, and flame graphs. The demo username is `huatuo` and the password is `huatuo1024`.

- [Events and AutoTracing](http://console.huatuo.tech/d/autotracing-event)

  ![Events and AutoTracing dashboard](/docs/img/quickstart-autotracing-event.png)

- [Host Metrics](http://console.huatuo.tech/d/metrics-host)

  ![Host metrics dashboard](/docs/img/quickstart-metrics-host.png)

- [Container Metrics](http://console.huatuo.tech/d/metrics-container)

  ![Container metrics dashboard](/docs/img/quickstart-metrics-container.png)

## Run Containers

![HUATUO component data flow](/docs/img/quickstart-data-flow.png)

### Docker

The following image disables container information retrieval and Elasticsearch storage by default. Start the collector:

```bash
$ docker run --privileged --pid=host --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

Fetch metrics from another terminal:

```bash
$ curl -s localhost:19704/metrics
```

A response with Prometheus-format metrics confirms that the metric endpoint is accessible. Event and AutoTracing results are written to `huatuo-local` inside the container. This directory may have no records until collection is triggered. In a test environment, generate the target activity or adjust the corresponding threshold, then check the records.

### Docker Compose

From the repository root, start [Elasticsearch](https://www.elastic.co), [Prometheus](https://prometheus.io), [Grafana](https://grafana.com), and the HUATUO components:

```bash
$ docker compose --project-directory ./build/docker up
```

Once the services start, open [Grafana](http://localhost:3000). Both the default administrator username and password are `admin`. Event and AutoTracing panels show data only when corresponding results have been collected. See [Data Sources](/docs/best-practice/datasource_en.md) for data source and dashboard configuration.

![huatuo-bamai processing flow](/docs/img/quickstart-components.png)

## Build from Source

### Build the Image

From the repository root, build an image containing `huatuo-bamai`, BPF objects, and tools:

```bash
$ docker build --network host -t huatuo/huatuo-bamai:latest .
```

### Run the Collector

Run the image:

```bash
$ docker run --privileged --pid=host --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

Alternatively, copy the files from `/home/huatuo-bamai` in the container and run locally:

```bash
$ ./huatuo-bamai --region example --config huatuo-bamai.conf
```

For long-running deployments, use [systemd](/docs/deployment/systemd_en.md) or [Kubernetes](/docs/deployment/kubernetes_en.md) as appropriate for your environment.

## Configuration

See [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md) for all fields, defaults, and conditions for settings to take effect.

### Container Information

HUATUO retrieves Pod and container information through kubelet. Set ports and certificates for your environment. Setting both `KubeletAuthorizedPort` and `KubeletReadOnlyPort` to `0` disables this feature.

```toml
  [Pod]
    KubeletClientCertPath = "/etc/kubernetes/pki/apiserver-kubelet-client.crt,/etc/kubernetes/pki/apiserver-kubelet-client.key"
```

### Storage

Metrics are exposed at `:19704/metrics`; configure Prometheus to scrape and store them. Events and AutoTracing results are written to the enabled storage backends. The following examples configure Elasticsearch and local files:

```toml
[Storage.Elasticsearch]
    Address = "http://127.0.0.1:9200"
    Username = "elastic"
    Password = "huatuo-bamai"
    Index = "huatuo_bamai"
```

```toml
# tracer's record data
# Path: all but the last element of path for per tracer
# RotationSizeMiB: Maximum size of a record file before rotation
# MaxRotatedFiles: Maximum number of rotated files to retain
[Storage.LocalFile]
    Path = "huatuo-local"
    RotationSizeMiB = 100
    MaxRotatedFiles = 10
```

### Event Thresholds

Adjust the configurable event and AutoTracing thresholds in `huatuo-bamai.conf` for your workload. Trigger conditions, scope, and defaults vary by collector and should be verified in the target environment.

### Resource Limits

In Kubernetes and systemd deployments, kubelet or systemd manages HUATUO's cgroup by default; HUATUO does not move its own PID. Use `--enable-cgroup` and the following configuration only when running directly without an external manager:

```toml
[Runtime]
    StartupCPULimitCores = 0.5
    CPULimitCores = 2.0
    MemoryLimitMiB = 2048
```
