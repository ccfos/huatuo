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

Pass a filename to run one integration test. The second argument sets the repetition
count, which defaults to 1 and must be between 1 and 1,000. The number of selected
tests multiplied by the repetition count must not exceed 10,000. Exceeding either
limit returns 2 without creating a run directory or report:

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
`stdout.log` and `stderr.log` files are printed in full on failure.
INT or TERM cancels each active case's process group with TERM, allowing five
seconds for EXIT cleanup before KILL. The runner preserves exit status 130 or 143.
New tests default to serial execution; review shared state before adding them
to `integration/lib_runner.sh`.

## Prerequisites and Results

Both suites share `integration/run.sh`, which runs integration tests by default; `e2e/run.sh` forwards to `--suite e2e`. The `make integration`, `make e2e`, and single-case entry points remain available:

```bash
bash integration/run.sh --suite e2e
bash integration/run.sh --suite e2e test_metrics.sh 2
bash e2e/run.sh test_metrics.sh 2
```

Both suites run in isolated UTS and mount namespaces, with hostname `huatuo-dev` and private mount propagation. Integration cases start their own services. E2E starts the default bamai before each case, then stops it and checks logs. Both suites retain execution logs and working files for all outcomes. Cleanup still stops processes, removes containers, and releases mounts.

Every case execution, including serial, parallel, exclusive, E2E and repeated
runs, also gets a private mount namespace and `/var/run`. Host runtime sockets
remain accessible while Huatuo PID locks and its Toolstream socket are private.
Service preparation, test execution and cleanup run in the same case environment.

When adding tests, maintain `runner_test_groups` (resource groups),
`runner_test_namespaces` (isolation), and `runner_shared_storage_tests` (shared
storage consumers) in `lib_runner.sh`. All use `suite/filename` keys and validate
explicit declarations before scheduling. Undeclared tests run exclusively with
mount isolation. `runner_worker.sh` owns each execution; `lib_runner_report.sh`
manages reports. Tests using profiler helpers load `lib.sh` followed explicitly
by `lib_profiler.sh`.

Declare additional isolation in `runner_test_namespaces` in
`integration/lib_runner.sh`, keyed by `integration/test_*.sh` or `e2e/test_*.sh`:

| Declaration | Execution environment |
| --- | --- |
| Unspecified or `mount` | Private mount; host PID and network visibility retained |
| `mount net` | Private network with loopback enabled; host services and proxies may be unreachable |
| `mount pid` | Private PID namespace and matching procfs; the entry point waits for its child, and the kernel terminates remaining processes when namespace init exits |

Isolation declarations are independent of concurrency groups. Unknown combinations
and invalid case names fail before scheduling. Namespace creation errors are FAIL;
execution never falls back to the host environment. Explicitly missing commands,
such as `ip` for a private network, produce SKIP. Cases no longer restart themselves
with `unshare`. Client/server network topologies built with `lib_namespace.sh`
remain scenario resources. Validate service reachability, exit codes and cancellation
cleanup when adding or changing an isolation declaration.

`test_tcpshark_output_cancellation_{blocked,healthy,timeout}.sh` share
`lib_tcpshark_output_cancellation.sh`; each receiver scenario gets a fresh
`mount net` environment. The case-local `execution.json` is an internal cleanup
result passed to the runner; consumers use `result.json`. If the entry point is
forcibly terminated before submitting its result, an unknown test exit code is null.

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

`skip` prints a reason and exits with 77; `EXIT` cleanup still runs. Normal completion is PASS, missing dependencies are SKIP, and test or cleanup errors are FAIL. On failure, scheduling stops while active cases finish. Unstarted executions and executions cancelled by the runner are SKIP, with a reason distinguishing them. INT/TERM still makes the runner return 130/143; classifying cancelled cases as SKIP does not turn an interrupted run into success.

The main case shell publishes reasons from `skip` / `fatal`. Use `run_test_step`
when a subprocess failure or skip should terminate the whole case. It runs an
external command and propagates its reason and original nonzero exit code.
Step scripts should source `lib.sh` in their main shell before calling `skip` /
`fatal`; nested steps use the same helper.

```bash
run_test_step bash -euc 'source "$ROOT_DIR/integration/lib.sh"; require_commands some_tool'
```

Use ordinary `if`, `wait`, or `||` to inspect expected failures, rather than
`run_test_step`. Reasons from ordinary children or bare subshells are not promoted
automatically. A nonzero exit without an explicit reason retains its exit code
and refers readers to the logs, avoiding attribution to an already handled error.

Every valid invocation, including a single case, creates `/tmp/huatuo-test.XXXXXX/`.
The terminal shows each completed result and reason, then the final counts and
`result.json` path. Reports require `jq`; invalid arguments do not create a plan
or report.

```text
/tmp/huatuo-test.XXXXXX/
├── result.json
├── test_metrics.1.ABCDEF/
│   ├── stdout.log
│   ├── stderr.log
│   └── configuration, responses, service logs, ...
├── shared-elasticsearch.1.GHIJKL/
└── ports/
```

Case directories include the test name, execution index starting at 1, and a
random suffix. Shared storage and port directories are created when needed.
Cases sharing storage use separate indexes. Retained directories can be archived
or deleted as a whole. Stdout and stderr are separate; their cross-stream order
is not preserved.

The report currently uses `schema_version: 1`:

| Field | Meaning |
| --- | --- |
| `suite`, `runs_per_test` | Suite and planned executions per test |
| `status` | `running`, `completed`, `aborted`, or `interrupted` |
| `exit_code`, `reason` | Runner exit code and reason for early termination |
| `started_at`, `finished_at` | RFC 3339 timestamps with timezone |
| `duration_seconds` | Elapsed seconds measured using a monotonic clock |
| `execution_counts` | `total`, `passed`, `failed`, `skipped`; total includes unstarted executions |
| `test_executions` | One record per planned test execution |

Each record has `test_name`, `run_index`, `result`, `exit_code`, `reason`, the three
time fields, and `logs.stdout` / `logs.stderr` paths relative to the report.
Final results are `passed`, `failed`, or `skipped`; unfinished records use `null`.
Unstarted records have null timestamps, duration, exit code, and logs. Preparation
or cleanup errors produce `failed` while preserving the actual test-process exit
code, or null if the process never started. Final result counts sum to `total`.

Updates replace the report atomically. A report write failure stops scheduling,
cancels active cases, and continues resource cleanup. The runner exits nonzero
and identifies the incomplete report without fabricating results. Write failures,
SIGKILL, or host shutdown can leave a partial report with `status: running`; its
counts are not final. INT/TERM during finalization preserves completed case results
while updating the overall interruption status and exit code. To inspect skipped executions:

```bash
jq '.test_executions[] | select(.result == "skipped") | {test_name, run_index, reason}' /tmp/huatuo-test.XXXXXX/result.json
```

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

The framework isolates each execution’s PID locks and Toolstream socket before service startup. The configuration, rotation, and backend consistency cases call `assert_huatuo_bamai_shutdown` before checking final data and logs to confirm a clean shutdown; `EXIT` traps handle best-effort cleanup.

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
