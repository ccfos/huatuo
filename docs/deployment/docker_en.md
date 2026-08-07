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
docker run --detach \
  --name huatuo-bamai \
  --restart unless-stopped \
  --privileged \
  --pid=host \
  --cgroupns=host \
  --network=host \
  --cpus=2 \
  --memory=4g \
  --volume /sys:/sys \
  --volume /proc:/proc \
  --volume /run:/run \
  huatuo/huatuo-bamai:latest
```

> Note: The built-in default configuration does not connect to kubelet or Elasticsearch.

Limit CPU and memory in production to isolate abnormal collection workloads.
The `4 GiB` container limit leaves runtime headroom above the default `2048 MiB`
process limit, reducing OOM risk.

Verify that the limits are active and observe actual usage:

```bash
docker inspect huatuo-bamai \
  --format 'NanoCPUs={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
docker stats huatuo-bamai
```

These values are an initial baseline. Adjust them based on node capacity,
collection jobs, and observed resource peaks.

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
