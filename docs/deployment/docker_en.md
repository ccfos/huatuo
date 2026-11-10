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

### Continuous profiling in the development stack

`make compose-dev-up` uses `build/docker/devconf/` with ES storage, API credentials,
and Grafana data sources configured. Fixed credentials are for development only.
When changing the ES password in `.env`, update both development configuration
files and the Grafana ES data source.

After startup, run from the repository root in another terminal with Bash,
`curl`, and `jq` installed:

```bash
./build/docker/run_test_profiling.sh
```

The script creates a 60-second host CPU profiling task, waits for completion,
queries raw results and the flame graph, saves them to `_output/profiling/run.XXXXXX/`,
and prints a Grafana URL with the task and time range selected. Run the target
workload during collection. The shared dashboard filters by Region, node, scope,
container, profile type, and task, and displays task status. The initial Grafana login is `admin / admin`.

The script uses fixed local development settings. If addresses, the node name, or
credentials change, update the constants at the top of the script. See
[Profiling API](/docs/reference/profiling-api_en.md) for other parameters. Stop the stack with `make compose-dev-down`.
