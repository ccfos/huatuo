---
title: Integration Tests
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

Integration tests run real programs to verify behavior across collectors, interfaces, and external dependencies. Metric tests use simulated `/proc` and `/sys` filesystems and check `/metrics` output. Other cases prepare processes, networking, or collection environments as needed.

Run `make build` before invoking test scripts directly. This guide covers entry points, result interpretation, and adding metric tests.

## Metric Test Flow

Metric tests run these steps in order:

1. Generate a temporary `bamai.conf` configuration.
2. Start `huatuo-bamai` with simulated `procfs` and `sysfs`.
3. Wait for `/metrics` to become accessible.
4. Fetch all metrics from `/metrics`.
5. Verify that every expected metric exists and matches.
6. Stop the service and clean up resources.

A missing or mismatched expected metric fails the test.

## Run Tests

Run integration tests from the repository root:
```bash
bash integration/run.sh
```

Pass a filename to run one integration test. The second argument sets the repetition count, which defaults to 1:

```bash
bash integration/run.sh test_metrics_exclude_filter.sh 10
```

Or use the Makefile:
```bash
make integration
```

### Parallel Integration Tests

Local runs default to one worker; use `bash integration/run.sh -j 2` or
`INTEGRATION_JOBS=2 make integration` to enable parallel execution. VM CI uses
three workers. Reviewed tests run in a bounded pool; network cases share a resource
group, including storage compatibility because it generates network events.
CPU-sensitive and unreviewed cases run alone after the pool drains.
A separate profiler batch runs after shared Elasticsearch stops, with at most
two concurrent cases. Java cases serialize because they share an agent library.
CPU-pinned, benchmark, hardware PMU and dropwatch resource-budget cases remain
exclusive.
The native physical-memory usage case also runs exclusively: concurrent memory
activity can trigger page migration that its allocation tracking does not preserve.
This scheduling restriction reduces interference; it does not fix migration accounting.
E2E stays serial even when `INTEGRATION_JOBS` is set.

Each parallel case has private Huatuo PID files and toolstream sockets and a
worker-specific port range. Host container runtime access is preserved. Run one
suite runner per host. Shared Elasticsearch remains alive until the batch drains.
On failure, scheduling stops and active cases finish their cleanup. Per-case
`test.log` files are printed on failure, and results include elapsed durations.
INT or TERM cancels each active case's process group with TERM, allowing five
seconds for EXIT cleanup before KILL. The runner preserves exit status 130 or 143.
New tests default to serial execution; review shared state before adding them
to `integration/lib_runner.sh`.

## CPU Metric and Autotracing E2E Tests

Run `make build`, then execute a single case as root on the test node:

```bash
sudo bash e2e/run.sh test_metrics_cpu_util.sh
sudo bash e2e/run.sh test_metrics_cpu_stat.sh
sudo bash e2e/run.sh test_metrics_cpu_runqlat_tracing.sh
sudo bash e2e/run.sh test_autotracing_cpuidle.sh
sudo bash e2e/run.sh test_autotracing_dload.sh
```

`make e2e` and the standard `make test` discover these cases automatically.
CPU cases run serially and target a Kubernetes VM with 4 CPUs and 8 GB of memory.
They require access to the local kubelet API; set `KUBELET_CERT` and `KUBELET_KEY`
to its client certificate and key.

`make build` precompiles the wait-time and taskstats helpers into
`_output/tests/`. Test execution does not need a Go compiler; missing build
artifacts fail the case. Shell owns orchestration, assertions and cleanup.

CPU references use a digest-pinned cAdvisor image and `pidstat` from `sysstat`.
The VM workflow installs `sysstat`; local runs must provide it. Prometheus
responses are decoded with the official prom2json 1.5.0 binary, downloaded
on demand into `_output/tools/` and verified with SHA256. cgroup v1 burst
counters are read from `cpu.stat` because cAdvisor does not populate them.
Use `CPU_REFERENCE_IMAGE` or `PROM2JSON_BIN` to supply preloaded tools.

Runqlat aggregation runs with the ordinary CPU unit tests. E2E verifies live
kernel behavior through public metrics, disabling, deletion and restarts,
without depending on private BPF map names or layouts.

Install `stress-ng` for host workloads; the VM workflow installs the package.
Pods use a digest-pinned `ghcr.io/colinianking/stress-ng` image. Each case checks
and pulls it through `crictl`, so the Kubernetes runtime must reach the registry.
Set `CPU_LOAD_IMAGE` to use another workload image.
Image preparation runs only in cases that use it. A pull failure fails that
case; it does not prevent unrelated tests from starting at the VM entry point.

cpu_util, cpu_stat and runqlat support cgroup v1/v2. cpu_stat temporarily changes
`kernel.sched_schedstats` and restores its original value. It checks
`wait_sum_percent` and burst counters only when the kernel exports them.
Runqlat requires the scheduling tracepoints and `free_fair_sched_group` kprobe;
cpuidle requires permission to load the perf BPF program.

cpuidle and dload start test Elasticsearch containers and require Docker.
Dload functional cases require Legacy cgroups and create D-state workers using
taskstats and dm-delay. Other cgroup modes verify unsupported behavior, then
report SKIP. Functional cases require `dmsetup`, `losetup`, `mkfs.ext4`,
`modprobe`, `nsenter`, mount privileges and an available loop device.
The disk image stays inside the case workspace. Cleanup deletes Pods, unmounts
the filesystem, removes the mapper, releases the loop device and deletes the
image. If device cleanup fails, the image is retained and the case fails.

Missing external commands, kernel hooks, device capabilities or kubelet
certificates produce SKIP with a reason. Missing build artifacts, incorrect
metrics, unexpected process exits and cleanup failures produce FAIL.
Logs and raw reference data are retained under
`/tmp/huatuo-test.XXXXXX/<case>.XXXXXX/`, including successful E2E runs.

## Prerequisites and Results

Both suites share `integration/run.sh`, which runs integration tests by default; `e2e/run.sh` forwards to `--suite e2e`. The `make integration`, `make e2e`, and single-case entry points remain available:

```bash
bash integration/run.sh --suite e2e
bash integration/run.sh --suite e2e test_metrics.sh 2
bash e2e/run.sh test_metrics.sh 2
```

Both suites run in isolated UTS and mount namespaces, with hostname `huatuo-dev` and private mount propagation. Integration cases start their own services. E2E starts the default bamai before each case, then stops it and checks logs. Integration removes the working directory after a pass or skip and retains it on failure; E2E retains it for all outcomes.

Apiserver tests follow the same flat layout:

- `integration/test_apiserver_profile_disabled.sh`: capabilities, authentication,
  and APIs without result storage.
- `integration/test_apiserver_profiling.sh`: seven functional checks covering
  host/global quotas, Node capacity, validation, missing containers, unreachable
  hosts, and offline Nodes.
- `e2e/test_apiserver_profiling.sh`: completion, exact list/raw pagination,
  flamegraphs, and agent metrics.
- `e2e/test_apiserver_profiling_{stop,concurrent,recovery,node_recovery}.sh`:
  stopping, concurrency, apiserver restart, and recovery after Node failure.
- `e2e/test_apiserver_profiling_matrix.sh`: 19 language/mode combinations.

`integration/lib_apiserver.sh` shares service configuration, requests, assertions,
and cleanup across both suites, reusing the existing storage and continuous
profiling helpers. The protocol client and workloads live in
`integration/testdata/`. `make build` prepares `_output/tests/apiserver-probe`
and the static `apiserver-go-workload` binary before tests run.

```bash
make build
sudo bash integration/run.sh test_apiserver_profile_disabled.sh
sudo bash integration/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling_matrix.sh
# Standard targets discover these scripts; a script name can take a repeat count.
sudo make integration
sudo make e2e
```

Static API checks do not require Docker, perf, or a compiler. Sampling requires
Linux, root, Docker, curl, jq, ss, timeout, sampling-enabled perf settings, and
built BPF/profiler tools. Host workload cases also require gcc; the matrix
compiles C/C++ inside Pods and does not require a host compiler. ES data is bound
to `elasticsearch-data/` in the case workspace. Reports include requests,
responses, profiles, logs, and `summary.tsv`. Generated configurations contain
test tokens and belong only in test environments.

The matrix also requires Python 3, ip, kubectl administration, crictl, local
kubelet credentials, and packaged asprof/py-spy tools. It matches Node InternalIP
against local addresses. `APISERVER_TEST_NODE` can select a Node explicitly but
must still identify this machine; an absent or ambiguous match fails. Override credential
paths with `KUBELET_CERT` and `KUBELET_KEY`. Images are prepared together through
the local CRI before Pods start with `imagePullPolicy: Never`; failed pulls retain
separate logs. Defaults are gcc:14, golang:1.24, eclipse-temurin:17-jdk, and
python:3.11-slim. Override them with
`APISERVER_TEST_{NATIVE,GO,JAVA,PYTHON}_IMAGE`.

Workload Pods prepare together while storage starts, then wait idle for an
explicit start signal. Only one workload and profiling task run at a time,
covering C/C++/Go oncpu, offcpu, and three memory modes; Java CPU and two memory
modes; and Python CPU. Go runs a read-only mounted, prebuilt static binary.
C/C++ and Java prepare workloads in their runtime images. Native workloads retain
sixteen 64 KiB mappings for final physical-usage snapshots and exit on SIGTERM.
The matrix uses two-second aggregation windows with six-second native and
eight-second Java tasks, requiring at least three result windows. Python runs
for five seconds and publishes one result window. Other profiling cases retain
their five-second aggregation interval. The matrix summary records each of the
19 combinations separately.

Assertions verify profile types, container ownership, nonempty results, and
positive flamegraph totals. Python has one collection window for the task duration.
Temporary namespaces, ConfigMaps, and Pods are cleaned on exit. Node recovery
kills the test Node and verifies both the old task's failure and a new task after
restart. Expected fault logs are allowed; runtime panics are rejected. Grafana
page rendering is outside this coverage.

Pods created by `k8s_create_pod` use a one-second termination grace period. Cleanup still
waits for pod deletion, and container discovery tests still verify that Huatuo
removes their metadata.

The runner checks only that `_output` exists, not individual binaries or BPF artifacts. Run `make build` before invoking scripts directly. An existing directory does not imply a complete build; missing artifacts fail when used.

Cases use one helper to check commands on PATH or executable paths and skip if they are missing:

```bash
require_commands jq curl ss
require_commands "${PROFILER_TOOL_DIR}/bin/asprof"
```

Check input files such as certificates with `require_readable`. An unreadable file causes a skip with the specific path reported:

```bash
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
```

`skip` prints a reason and exits with 77; `EXIT` cleanup still runs. The runner records 0 as PASS, 77 as SKIP, and other statuses as FAIL. Cleanup failure is also FAIL. The runner returns 0 only for passes and skips. On failure, it stops subsequent cases and reports the counts so far.

Each execution is counted separately, including repetitions, for example:

```text
summary: total=5 passed=3 skipped=2 failed=0
```

If root privileges or namespace commands are unavailable, both suites mark every selected case as SKIP.

### Failure Handling

- `huatuo-bamai` metrics and logs are printed to standard output for diagnosis.
- The temporary working directory is retained for debugging.

### Successful Results

- The list of successfully verified metrics is displayed.

Go fixture builds reuse the inherited `GOCACHE` or Go's default build cache.
Temporary build files and test outputs stay in each case workspace. Live Go
tests retain `-count=1` so cached dependencies never skip test execution.

## Profiling

Run `make build` first and configure Java or Python tool paths according to `integration/env.sh`.

Repository integration tests provide executable end-to-end examples. They create target processes, run profiler, and check expected stacks in the output:

```bash
# Native CPU
sudo ./integration/run.sh test_profiler_native_cpu.sh

# Native off-CPU blocking and scheduling delay
sudo ./integration/run.sh test_profiler_native_cpu_offcpu.sh

# Native virtual and physical memory
sudo ./integration/run.sh test_profiler_native_memory_virtual_alloc.sh
sudo ./integration/run.sh test_profiler_native_memory_physical_usage.sh

# Java CPU and memory
sudo ./integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo ./integration/run.sh test_profiler_java_memory_usage_alloc.sh

# Python multiprocess CPU
sudo ./integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

Container, thread-group, and CPU-specific examples are in `test_profiler_native_cpu_container.sh`, `test_profiler_native_cpu_thread_group.sh`, and `test_profiler_native_cpu_cpuid.sh`.

The native CPU thread-group test keeps both five-second profiling windows and
stops its worker normally after capture, avoiding the remaining workload tail.

## Storage Tests

Storage phase timings separate image pulls, startup lock waits, container start,
readiness, verification and cleanup. The runner prints `timings.log` files before
removing successful workspaces.

Continuous-profiling API, profile-filter, IRQ autotracing, and storage-values
cases share one default Elasticsearch instance per suite repetition. Each case
uses workspace-scoped indices. The runner owns shutdown, including on failure;
standalone scripts and compatibility profiles retain their own instances.

Neither of these cases requires an Elasticsearch service:

| Test | Checks |
| --- | --- |
| `test_basic_storage_elasticsearch_config.sh` | All eight combinations of `Address`, `Username`, and `Password`: partial configurations must exit with a configuration error; empty and complete configurations must reach readiness. The complete case uses `--disable-storage` to isolate backend availability. |
| `test_basic_storage_localfile_rotation.sh` | Enables only LocalFile and sends packets to a closed UDP port to trigger rotation through Dropwatch events. Checks that the first backup is evicted, two backups and one active file remain, and each file is at most 1 MiB and contains valid JSON for the target events. |

The rotation case requires kernel BTF, the `skb/kfree_skb` tracepoint, and the Dropwatch binary and BPF object. It shares UDP sending and configuration helpers with the backend consistency test. Callers of `write_config_bamai_storage_events` must set `STORAGE_EVENT_RATE`, `STORAGE_ROTATION_MIB`, and `STORAGE_MAX_ROTATED_FILES`.

Both cases call `integration_isolate_run_directory` before starting the service to isolate its PID lock and Toolstream socket. The configuration, rotation, and backend consistency cases call `assert_huatuo_bamai_shutdown` before checking final data and logs to confirm a clean shutdown; `EXIT` traps handle best-effort cleanup.

## Background Process Cleanup

Daemon readiness is checked immediately and polled every 100 ms, retaining the
120-second startup deadlines. Java streaming checks stop normally with SIGINT
after two nonempty windows; separate captures still test duration expiry.

`stop_and_wait_by_pid PID [TIMEOUT] [SIGNAL]` stops and waits for a child
process, returning its exit status. The signal defaults to TERM; the helper
polls every 100 ms and uses KILL after the timeout if needed. A missing or empty
PID returns success immediately. Clear the variable after waiting. Use
`|| true` for best-effort cleanup; assert the exit status when it is part of
the test contract.

`test_basic_test_helpers.sh` checks graceful/forced shutdown, custom signals,
Java streaming completion, shutdown failures, missing windows and complete
network capture predicates using local process and packet fixtures. Shell owns
the orchestration and assertions; Python supplies only process workloads. Capture
predicates live in `lib_network_capture.sh`, shared with the live tests. The helper
test also checks build/cache contracts, storage ownership, scheduling and bounded
serial/parallel cancellation. It requires Python 3 and jq, without a JVM or BPF attachment.

## IO tests

`make integration` includes live-kernel IO qualification for latency and size
statistics, blk-throttle and IOCOST waits, device and cgroup lifecycles, and
health evidence. These component tests do not replace black-box checks of
service startup, configuration, and public output.

Run as root on a dedicated Linux test host with BPF/BTF support. Cases create
and clean up temporary virtual devices and cgroups. Run IOCOST alone with
`sudo bash integration/run.sh test_iocost.sh`; `TEST_IOCOST_MODE` selects
`functional`, `lifecycle`, `faults`, `pressure`, or `compat` (default: `all`).

IO tests prefer disks created with `scsi_debug`. If it is unavailable,
they use an eligible existing SCSI/NVMe/virtio disk for read-only checks, or skip
those checks when none is available. Configuration changes and device deletion use
only test-owned disks. Metric checks start their own Huatuo daemon.

IO health verifies health events and evidence on a live Linux kernel.
Real NVMe/SCSI evidence commands require an explicit dedicated test device:

```bash
sudo env TEST_IO_CONFIRM_NON_SYSTEM=1 TEST_NVME_DEVICE=nvme0 \
  bash integration/run.sh test_io_kernel.sh
```

For SCSI, use `TEST_SCSI_DEVICE=sdb`. These cases require `nvme-cli` and
`smartmontools`, respectively.

## Network Namespace Helpers

Network tests can source `integration/lib_namespace.sh` after `lib.sh`:

- `namespace_setup NAME` creates a network namespace and exposes its name through `NETNS`. The caller enables `lo` and configures addresses.
- `namespace_setup_with_pair PREFIX SERVER_ADDR CLIENT_ADDR [PREFIX_LENGTH]` creates two namespaces connected by veth, configures addresses, and enables interfaces. The prefix length defaults to 24. Namespaces, interfaces, and addresses are exposed through `NETNS_SERVER`, `NETNS_CLIENT`, `NETNS_VETH_SERVER`, `NETNS_VETH_CLIENT`, `NETNS_SERVER_ADDR`, and `NETNS_CLIENT_ADDR`.
- `namespace_cleanup` deletes namespaces created by these helpers and clears state. Repeated calls are supported. In `EXIT` cleanup, stop processes inside the namespaces before calling it.

## Shared Packet Sender

Positive capture tests wait for their complete matching event predicates, then
stop and drain collectors and traffic generators. They recheck final output and
exit status; negative-result and rate-limit observation windows stay unchanged.

Tests requiring raw packets reuse `integration/testdata/test_packet_sender.py`; extend it for new scenarios. It requires Python 3 and raw socket privileges and supports `ipv4-options` (TCP/UDP EOL options, including maximum IHL) and `icmpv6-echo`. Use each subcommand's `--help` for address, port, or Echo identifier parameters. The program sends packets every 100 ms and exits normally on SIGTERM. Shell cases manage namespaces, drop rules, assertions, logs, and process cleanup. Workloads needing real TCP connections continue to use `testdata/tcp_server.py`.

## Add a Metric Test

### 1. Prepare Simulated Data

If the new metric depends on `/proc` or `/sys`, add or update simulated data in:
```bash
integration/fixtures/
```
This directory contains only kernel filesystem snapshots (`proc/`, `sys/`, `dev/`) with original paths and filenames. Put tool fixtures in `integration/testdata/tools/`, using filenames that start with `test_` or `Test`. For example, copy `test_perf.sh` to a temporary tool directory as `perf` before starting the test.

### 2. Add Expected Metrics

Create a file in:
```bash
integration/testdata/expected_metrics/
├── test_cpu.txt
├── test_memory.txt
└── ...
```
Each nonempty, non-comment line defines one expected Prometheus metric and must exactly match a line returned by `/metrics`. Tests load `test_*.txt` automatically and derive the metric prefix by removing `test_` from the filename.

### 3. Run the Test

```bash
bash integration/run.sh
```
The test fails if any expected metric is missing or does not match.

### Daemon cgroup resource limits

`test_node_runtime_limits.sh` checks that default startup inherits the test process's
cgroup. With `--enable-cgroup`, it verifies membership through `/proc/PID/cgroup`
and `cgroup.procs`, reads the startup and steady CPU limits, and checks the
MiB-to-byte conversion for `MemoryLimitMiB`. It then changes all three Runtime
settings and repeats after a graceful stop and restart. It also checks exit
status, removal of the daemon's cgroups, and absence of error/panic/fatal logs.
This verifies configured kernel limits, not CPU throughput or OOM behavior.

The test copies the fixtures and replaces only the copied XFS device with a
FIFO. Its observer waits for XFS to open that device, verifies startup limits,
then supplies the fixture bytes to release initialization. Startup and observer
completion have deadlines; a missing gate is a failure. This depends on the
synchronous XFS constructor running before the steady CPU quota is applied.
The complete 512-byte fixture is written in one block: XFS closes the FIFO after
reading its superblock, so a later write could fail after initialization succeeds.
No production hooks or real device files are changed.

Run on an isolated Linux test VM with writable CPU and memory controllers.
The same assertions read quota/period and memory files on v1/hybrid hosts, or
`cpu.max`/`memory.max` on pure v2 hosts. V2 also requires a reachable systemd
manager. A host-wide lock prevents concurrent runs of this case; a busy lock,
missing capabilities, or an existing bamai runtime cgroup produces a reported
SKIP. The daemon owns cgroup cleanup. Any remaining group is preserved and fails
the test, because its name alone does not prove ownership. Run separately on
both hierarchies to establish v1 and v2 coverage. Storage and kubelet are disabled.

After building, run this case from the integration directory:

```bash
cd integration
```

```bash
bash run.sh test_node_runtime_limits.sh
```

### VM Build Reuse

CI generates architecture-independent OpenAPI, mock, and Cap’n Proto sources
once per workflow and transfers them to each VM. The artifact must match the
checkout’s commit and contain every required source; tracked files must be clean.
BPF objects and their Go ABI types are still generated inside each VM.
Local builds continue to run all generators.

After guest setup expands the root filesystem, each VM restores a Go build
cache scoped to its image and toolchain. Cache misses
or transfer failures fall back to compilation. Unit tests use
`-count=1` so cached test results never replace execution.

Measure both a cold run and a warm run when evaluating CI time: the first run
populates the persistent cache, and cache download/upload time also counts.

VM CI preloads ES7 and OpenSearch images during compilation and unit tests.
Compatibility tests still check pullability. Prefetch logs are saved under
`_output/storage-image-prefetch`; prefetch failures do not replace normal checks.

## SchedBlame Live Collection

`test_sched_blame.sh` runs the built `_output/bin/sched-blame` with real BPF
objects, scheduler hooks, perf transport, and kubelet container discovery.
It does not wrap a Go component test or inject synthetic perf records.

The case creates two uniquely named pods on the local node and pins their
workloads to the same CPU. It records target-only, competing, and
competitor-removed phases, then deletes and recreates the highlighted target.
Each phase must produce at least five valid runtime samples after a two-second
settling interval. The checker validates target identities, dense-slot ownership,
ratio arithmetic, history reset on replacement, and absence of transport loss.
The competing phase must exceed both quiet phases by five percentage points;
the test does not require an exact contention ratio. SIGTERM must stop the
command successfully, and namespace deletion must complete.

Run in an isolated Linux 4.18 through 6.5 environment with BTF, the required
scheduler and cgroup hooks, a local Kubernetes node, authenticated kubelet
access, and the preloaded `BUSINESS_POD_IMAGE`. The test reports SKIP for
unavailable prerequisites. Once setup succeeds, tracer startup, collection,
validation, and cleanup failures fail the case. Unsupported newer kernels are skipped
without loading SchedBlame BPF. Container identity and root filesystem type
do not determine eligibility; capable containers and btrfs hosts are not
excluded by those properties.

After CI has built the checkout, run:

```bash
sudo bash integration/run.sh test_sched_blame.sh
```

Success prints `sched-blame live collection verified`. Failed runs retain the
CSV, phase windows, command log, and pod diagnostics in the case workspace.
The case stays in the runner's default exclusive group because it controls CPU
load. CSV assertions do not wait for the 60-sample anomaly baseline. Ordinary
Go component tests retain deterministic coverage for exact attribution, P99,
anomaly payloads, upload, loss counters, and final queue draining. Live coverage
does not establish exact accounting accuracy or generation isolation across
dense-slot reuse, which remains an accepted approximation.
