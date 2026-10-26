---
title: Container Deployment
type: docs
description: 
author: HUATUO Team, hao022
date: 2026-01-11
weight: 1
---

Use Docker to run the collector or Compose to start the complete environment. Available image versions are listed in the [image registry](https://hub.docker.com/r/huatuo/huatuo-bamai/tags).

## Docker

```bash
docker run --detach \
  --name huatuo-bamai \
  --restart unless-stopped \
  --privileged \
  --pid=host \
  --cgroupns=host \
  --network=host \
  --cpus=2 \
  --memory=2g \
  --volume /sys:/sys \
  --volume /proc:/proc \
  --volume /run:/run \
  huatuo/huatuo-bamai:latest
```

> Note: The image's default configuration does not connect to kubelet or Elasticsearch.

Set CPU and memory limits in production to prevent abnormal collection tasks from affecting host workloads. Docker enforces these limits through the container cgroup. Huatuo does not create its own cgroup by default; do not pass `--enable-cgroup` in Docker deployments.

Use the following commands to verify the limits and inspect actual usage:

```bash
docker inspect huatuo-bamai \
  --format 'NanoCPUs={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
docker stats huatuo-bamai
```

These example values are a starting point. Adjust them for node capacity, collection tasks, and peak resource usage.

## Docker Compose

[Docker Compose](https://docs.docker.com/compose/) provides a complete local environment in which you manage the collector, Elasticsearch, Prometheus, Grafana, and other components.

```bash
$ docker compose --project-directory ./build/docker up
```

See the [installation guide](https://docs.docker.com/compose/install/linux/) to install Docker Compose.

Compose installs Business Charts (`volkovlabs-echarts-panel`) for the IO
interval overview. Install this plugin when using an existing Grafana instance.
