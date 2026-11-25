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

The collection script requires Bash, `curl`, and `jq`. Container modes also require `kubectl`,
`ip`, `crictl`, an available Kubernetes node on the local host, and access to the `gcc:14` image.
IRQ requires `python3`, `taskset`, and `timeout`, without Kubernetes. After startup,
select modes from another terminal at the repository root. Each task runs for 20 seconds:

```bash
# Default: profile on-CPU execution in a temporary container
./build/docker/run_test_cpu_profiling.sh
# Profile off-CPU waiting time in a container
./build/docker/run_test_cpu_profiling.sh --offcpu
# Profile host softirq counts
./build/docker/run_test_cpu_profiling.sh --irq
# Run all six modes sequentially
./build/docker/run_test_cpu_profiling.sh --oncpu --offcpu \
  --virtual-alloc --physical-alloc --physical-usage --irq
```

IRQ profiles the host; the other five modes profile containers. The script does not accept
`--with-host` or `--with-container`.
Mode flags can be combined in any order; repeated flags have no additional effect. The default is on-CPU.
`--virtual-alloc` and `--physical-alloc` observe virtual address space and physical page allocation,
respectively. `--physical-usage` observes live physical pages tracked during capture, not total process RSS.
Container modes use separate workload Pods. IRQ generates loopback UDP traffic on one allowed CPU
and profiles that CPU, producing one count window with `irq_context`, `irq_type`, `irq_vector`, and
other labels. Node-side sampling limits apply: `dropped_samples` in `raw.json` records discarded
samples, so flame graph counts do not represent the total number of interrupts.
Both paths validate raw results and the Grafana flame graph.

Grafana lists the two allocation modes separately under **Profile type** as
`memory:virtual_alloc:bytes:space:bytes` and `memory:physical_alloc:bytes:space:bytes`.

After completion, open the Grafana URL in the output to view the flame graph. The initial login is `admin / admin`.
Results and diagnostics are saved to the reported `output_dir` under `_output/profiling/run.XXXXXX/`.
The temporary container workload is removed on exit; collected results remain queryable.
For label filtering, see [View the Flame Graph](/docs/best-practice/continuous-profiling_en.md#5-view-the-flame-graph).

The script targets the default development stack. For custom collection requests, see
[Profiling API](/docs/reference/profiling-api_en.md).

To deploy and benchmark Redis or PostgreSQL and profile the service container:

```bash
./build/docker/run_test_redis_profiling.sh
./build/docker/run_test_postgres_profiling.sh
./build/docker/run_test_postgres_profiling.sh --offcpu
./build/docker/run_test_postgres_profiling.sh --oncpu --offcpu
./build/docker/run_test_postgres_profiling.sh --virtual-alloc --physical-alloc --physical-usage
./build/docker/run_test_postgres_profiling.sh --irq
```

Run `make compose-dev-up` before these scripts. They require `curl`, `jq`, `kubectl`,
`ip`, `crictl`, a Kubernetes node on the local host, and access to its containerd runtime.
They preload missing workload images with `crictl`. The runtime endpoint defaults to
`unix:///run/containerd/containerd.sock`; override it with `CONTAINER_RUNTIME_ENDPOINT`.
They use `redis` with
`redislabs/memtier_benchmark`, or `postgres`; no benchmark tools need installing on the host.
Redis runs mixed reads and writes with a 30-second profile. Each PostgreSQL mode profiles for
60 seconds. CPU modes initialize temporary data and run read-only queries; memory modes reconnect
and materialize queries to allocate, touch, and briefly retain memory. Tasks are created through the apiserver;
the scripts stop the benchmark after task completion, then generate the flame graph link.
Success exits with code 0; the JSON output contains the flame graph link in `grafana_url` and the
results and benchmark log directory in `output_dir`. Failure exits with a nonzero code and
`grafana_url: null`. Temporary services, data, and benchmark containers are removed on exit;
the development stack and collected results remain available.
Selecting multiple PostgreSQL modes returns a `results` array with a separate `output_dir` and
`grafana_url` for each trial; any mode failing produces a nonzero exit code.
PostgreSQL `--irq` keeps pgbench running and generates loopback UDP traffic while profiling host
softirq activity. It requires `python3`, `taskset`, and `timeout`; results include host activity
beyond PostgreSQL on the selected CPU.
Off-CPU uses the same read-only pgbench workload and does not guarantee a particular wait reason.

After the trial, run `make compose-dev-down` to stop and remove the development stack.
