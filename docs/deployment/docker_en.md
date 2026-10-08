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
  --memory=2g \
  --volume /sys:/sys \
  --volume /proc:/proc \
  --volume /run:/run \
  huatuo/huatuo-bamai:latest
```

> Note: The built-in default configuration does not connect to kubelet or Elasticsearch.

Limit CPU and memory in production to isolate abnormal collection workloads.
Docker manages the container cgroup; Huatuo does not create its own cgroup by default,
so do not pass `--enable-cgroup` in a Docker deployment.

Verify that the limits are active and observe actual usage:

```bash
docker inspect huatuo-bamai \
  --format 'NanoCPUs={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
docker stats huatuo-bamai
```

These values are an initial baseline. Adjust them based on node capacity,
collection jobs, and observed resource peaks.

### Start containers with Docker

The `docker compose` command allows you to quickly set up a complete local environment where you manage the collector, Elasticsearch, Prometheus, Grafana, and other components yourself.

```bash
$ docker compose --project-directory ./build/docker up
```

For installation instructions, see https://docs.docker.com/compose/install/linux/.

Compose stores Elasticsearch data, Prometheus history, and Grafana state in the
named volumes `elasticsearch-data`, `prometheus-data`, and `grafana-data`. Keep the
same Compose project name to reuse them. Container recreation and ordinary
`docker compose down` preserve these volumes. `docker compose down --volumes`
deletes them, as does the development cleanup command `make compose-dev-down`.

Before applying this Compose file to an existing installation that has no named
data volumes, back up the services' data and plan its restoration into the new
volumes. Data in old container layers or anonymous volumes is not migrated
automatically. Use each service's supported backup/restore procedure and verify
the restored data before removing the old containers or volumes.
