---
title: Docker
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
$ docker run --privileged --pid=host --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️ When this method is used, the container relies on the built-in default configuration file. That configuration does not connect to the kubelet or Elasticsearch.

### Start containers with Docker

[Docker Compose](https://docs.docker.com/compose/) allows you to run either
the complete stack or a profile-only stack.

```bash
$ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
```

The `full` profile starts huatuo-bamai, Elasticsearch, Prometheus, Pyroscope,
Grafana, and huatuo-apiserver.

To collect profiles into Pyroscope without Elasticsearch, Prometheus, or
huatuo-apiserver:

```bash
$ COMPOSE_PROFILES=profiling docker compose --project-directory ./build/docker up
```

The profiling profile starts only huatuo-bamai, Pyroscope, and Grafana.
huatuo-bamai does not wait for Elasticsearch and starts with kubelet discovery
disabled, so kubelet client certificates are not required. Open Grafana at
http://localhost:3000 and use the `huatuo-bamai-pyroscope` data source.

For Docker Compose installation instructions, see https://docs.docker.com/compose/install/linux/.
