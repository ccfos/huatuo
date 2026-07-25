---
title: Docker Compose
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 1
---

### Image Download

Image repository: https://hub.docker.com/r/huatuo/huatuo-bamai/tags

### Start a container with Docker

```bash
$ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️ When this method is used, the container relies on the built-in default configuration file. That configuration does not connect to the kubelet or Elasticsearch.

### Start containers with Docker Compose

[Docker Compose](https://docs.docker.com/compose/) allows you to run either
the complete stack or a profile-only stack.

```bash
$ docker compose --project-directory ./build/docker up
```

The default `profiling` profile starts only huatuo-bamai, Pyroscope, and Grafana.
huatuo-bamai does not wait for Elasticsearch and starts with kubelet discovery
disabled, so kubelet client certificates are not required. It renders
`AutoTracing.Display.Backend = "pyroscope"` without rebuilding the image.
Open Grafana at
http://localhost:3000 and open the
`HuaTuo AutoTracing Pyroscope Flamegraph` dashboard. CPUIdle and CPUSys
profiles appear after their configured AutoTracing thresholds are triggered.

To use the existing huatuo-apiserver display path:

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

The `full` profile starts huatuo-bamai, Elasticsearch, Prometheus, Pyroscope,
Grafana, and huatuo-apiserver. It renders
`AutoTracing.Display.Backend = "apiserver"` into the runtime configuration.

For Docker Compose installation instructions, see https://docs.docker.com/compose/install/linux/.
