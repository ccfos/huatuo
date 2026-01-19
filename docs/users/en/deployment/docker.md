---
title: Docker
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 1
---

## Run Only the Collector

#### Start the Container

```bash
docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /proc:/proc -v /run:/run huatuo/huatuo-bamai:latest
```

> ⚠️ This uses the **default configuration file inside the container**. The internal default configuration does **not** connect to Elasticsearch. For a complete setup, mount your own `huatuo-bamai.conf` using `-v`, and update the config according to your environment (kubelet access, Elasticsearch settings, local log storage path, etc.).

## Deploy All Components (Docker Compose)

For local development and validation, using [Docker Compose](https://docs.docker.com/compose/) is the most convenient approach.  
You can quickly launch a full environment containing the collector, Elasticsearch, Prometheus, Grafana, and other components.

```bash
docker compose --project-directory ./build/docker up
```

> It is recommended to install Docker Compose using the **plugin** method: https://docs.docker.com/compose/install/linux/
