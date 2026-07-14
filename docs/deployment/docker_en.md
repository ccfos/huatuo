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

[Docker Compose](https://docs.docker.com/compose/) allows you to quickly set up a complete local environment where you manage the collector, Elasticsearch, Prometheus, Pyroscope, Grafana, and other components yourself.

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

For Docker Compose installation instructions, see https://docs.docker.com/compose/install/linux/.

To display AutoTracing CPU flame graphs without running `huatuo-apiserver`, see [AutoTracing flame graphs with Pyroscope](../development/autotracing_pyroscope_en.md).
