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
interval overview and profiling label filters. Install this plugin when using an existing Grafana instance.

### Continuous profiling in the development stack

From the repository root, start the development stack with ES storage, API authentication,
and Grafana data sources configured:

```bash
make compose-dev-up
```

Fixed credentials are for development only. When changing the ES password in `build/docker/.env`,
update both service configurations under `build/docker/devconf/` and the Grafana ES data source.

The collection script requires Bash, `curl`, and `jq`. Container collection also requires `kubectl`,
`ip`, an available Kubernetes node on the local host, and access to the `gcc:14` image.
After startup, select a scope from another terminal at the repository root. Each task runs for 20 seconds:

```bash
# Profile existing host workloads
./build/docker/run_test_cpu_profiling.sh --with-host
# Create and profile a temporary container workload
./build/docker/run_test_cpu_profiling.sh --with-container
# Profile off-CPU waiting time on the host or in a container
./build/docker/run_test_cpu_profiling.sh --with-host --offcpu
./build/docker/run_test_cpu_profiling.sh --with-container --offcpu
```

Select scopes with `--with-host` and `--with-container`, and modes with `--oncpu` and `--offcpu`.
Flags can be combined in any order; repeated flags have no additional effect. The defaults are both scopes
and on-CPU. Passing all four flags runs four combinations sequentially, for 20 seconds each.
Use `--with-host` without Kubernetes. Off-CPU trials check for `offcpu_reason` sample labels and generate
Grafana queries and links for the off-CPU profile type.

After completion, open the Grafana URL in the output to view the flame graph. The initial login is `admin / admin`.
Results and diagnostics are saved to the reported `output_dir` under `_output/profiling/run.XXXXXX/`.
The temporary container workload is removed on exit; collected results remain queryable.
For label filtering, see [View the Flame Graph](/docs/best-practice/continuous-profiling_en.md#5-view-the-flame-graph).

The script targets the default development stack. For custom collection requests, see
[Profiling API](/docs/reference/profiling-api_en.md).

To deploy and benchmark Redis or PostgreSQL and collect a CPU profile of the service container:

```bash
./build/docker/run_test_redis_profiling.sh
./build/docker/run_test_postgres_profiling.sh
```

Run `make compose-dev-up` before these scripts. They require `curl`, `jq`, `kubectl`,
`ip`, `crictl`, a Kubernetes node on the local host, and access to its containerd runtime.
They preload missing workload images with `crictl`. The runtime endpoint defaults to
`unix:///run/containerd/containerd.sock`; override it with `CONTAINER_RUNTIME_ENDPOINT`.
They use `redis` with
`redislabs/memtier_benchmark`, or `postgres`; no benchmark tools need installing on the host.
Redis runs mixed reads and writes with a 30-second profile. PostgreSQL initializes temporary data
and runs read-only queries with a 60-second profile. Tasks are created through the apiserver;
the scripts stop the benchmark after task completion, then generate the flame graph link.
Success exits with code 0; the JSON output contains the flame graph link in `grafana_url` and the
results and benchmark log directory in `output_dir`. Failure exits with a nonzero code and
`grafana_url: null`. Temporary services, data, and benchmark containers are removed on exit;
the development stack and collected results remain available.

After the trial, run `make compose-dev-down` to stop and remove the development stack.
