---
title: Integration Tests
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

Integration and E2E tests verify Huatuo collection, service interfaces, and external dependencies. This guide covers running tests, configuring the test environment, and reading results.

## Run Tests

Run commands from the repository root. Tests require Linux and root privileges. Use a dedicated test node for cases involving containers, Pods, or block devices.

Build and run through the Makefile:

```bash
sudo make integration
sudo make e2e
sudo make test
```

`make test` includes unit, integration, and E2E tests. Alternatively, build first, then run scripts as root:

```bash
make build test-build
sudo bash integration/run.sh
sudo bash e2e/run.sh
```

### Single Cases and Repetitions

An optional count after the script name sets repetitions. It defaults to 1 and accepts 1–1,000. The number of selected tests multiplied by repetitions must not exceed 10,000.

```bash
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
sudo bash e2e/run.sh test_metrics.sh 2
```

You can also select E2E with `--suite e2e`:

```bash
sudo bash integration/run.sh --suite e2e test_metrics.sh 2
```

Invalid arguments return exit code 2 without creating a run directory or report.

### Parallel Runs

Integration tests default to one worker. `-j` accepts 1–8; `INTEGRATION_JOBS` also sets the worker count:

```bash
sudo bash integration/run.sh -j 2
sudo env INTEGRATION_JOBS=2 make integration
```

Some cases require exclusive execution, so actual concurrency may be lower. Run only one suite runner per host. E2E always runs serially and does not accept `-j` greater than 1.

## Environment Requirements

Run `make build test-build` before invoking scripts; missing build artifacts cause failures. Reports require `jq`. The test environment also needs `unshare`, `mount`, `flock`, `ss`, and `stat`. Individual cases check their required commands, kernel capabilities, services, and input files.

Missing root privileges or required namespace commands cause selected cases to report SKIP. Cases also report specific reasons for missing external dependencies. SKIP does not mean validation passed.

Common environment variables:

| Variable | Purpose |
| --- | --- |
| `INTEGRATION_JOBS` | Integration worker count; default 1 |
| `KUBELET_CERT`, `KUBELET_KEY` | Client certificate and private key paths for the local kubelet |
| `PROFILER_TOOL_DIR` | Java/Python collection tool directory; default `_output/tools` |
| `APISERVER_TEST_NODE` | Local Kubernetes node name for the apiserver matrix |
| `APISERVER_TEST_NATIVE_IMAGE`, `APISERVER_TEST_GO_IMAGE`, `APISERVER_TEST_JAVA_IMAGE`, `APISERVER_TEST_PYTHON_IMAGE` | Apiserver matrix images |
| `TEST_IOCOST_MODE` | IOCOST test scope; default `all` |
| `TEST_NVME_DEVICE`, `TEST_SCSI_DEVICE` | Dedicated devices for IO health evidence |
| `TEST_IO_CONFIRM_NON_SYSTEM` | Set to `1` when explicitly selecting an IO evidence device to confirm it is not a system device |

Pass overrides with `sudo env NAME=value ...` so sudo environment filtering does not discard them.

## Results and Logs

| Result | Meaning |
| --- | --- |
| PASS | Test and cleanup succeeded |
| SKIP | Missing dependencies, unstarted execution, or cancellation; see the report reason |
| FAIL | Test, service preparation, or cleanup failed |

After a failure, no new cases start. Unstarted and cancelled executions are SKIP. INT/TERM interruption returns 130/143. A run with only skipped cases can return success, so also inspect counts and skip reasons.

Each valid invocation creates `/tmp/huatuo-test.XXXXXX/`. Logs and working files are retained for all outcomes and can be archived or deleted together. The run directory contains `test_run_results.json`. Each execution directory contains `stdout.log`, `stderr.log`, and service logs, configuration, and response data.

### Console Summary

The console shows individual results and ends with a JSON summary:

| Field | Meaning |
| --- | --- |
| `status` | `completed`, `aborted`, or `interrupted` |
| `output_dir`, `results_path` | Run directory and full report path |
| `runs_per_test` | Planned repetitions per test |
| `started_at`, `finished_at`, `duration_seconds` | Start time, finish time, and elapsed seconds |
| `execution_counts` | `total`, `passed`, `failed`, `skipped`, including repeated and unstarted executions |
| `skipped_tests` | Names of skipped tests |
| `failed_tests` | Failed test names, execution indexes, reasons, and logs |
| `reason` | Reason for early termination; omitted on normal completion |

Each `failed_tests` entry contains `name`, `run_index`, `reason`, and `logs`. Each log contains a `path` relative to `output_dir`, text `content`, and a `truncated` flag.

Logs include nonempty `.log`, `.err`, `.out`, `.json`, and `.conf` text files under the failed execution directory. Binary files, `test_run_results.json`, and `reason.*.log` are excluded. At most the last 64 KiB of each file is shown; full content remains in the original file. Start diagnosis with `reason` and `stderr.log`, then inspect related service logs and responses.

### Full Report

`test_run_results.json` uses `schema_version: 1`:

| Field | Meaning |
| --- | --- |
| `suite`, `runs_per_test` | Suite and planned repetitions per test |
| `status` | `running`, `completed`, `aborted`, `interrupted` |
| `exit_code`, `reason` | Run exit code and reason for early termination |
| `started_at`, `finished_at`, `duration_seconds` | Start time, finish time, and elapsed seconds |
| `execution_counts` | Execution counts |
| `test_executions` | One record for each planned execution of each test |

Each execution contains `test_name`, `run_index`, `result`, `exit_code`, `reason`, timestamps and duration, and `logs.stdout` / `logs.stderr` paths relative to the report directory. Results are `passed`, `failed`, or `skipped`; unfinished executions use `null`. Unstarted executions have no timestamps or logs. Unknown process exit codes are `null`. Cleanup failure can make an execution `failed` even if its process exited with code 0.

A report with `status: running` is incomplete and must not be treated as final. Forced termination or report write failures can leave this state.

Inspect failed or skipped executions:

```bash
jq '.test_executions[] | select(.result == "failed" or .result == "skipped") | {test_name, run_index, result, reason, logs}' /tmp/huatuo-test.XXXXXX/test_run_results.json
```

## Common Test Cases

### CPU Metrics and Autotracing

```bash
sudo bash e2e/run.sh test_metrics_cpu_util.sh
sudo bash e2e/run.sh test_metrics_cpu_stat.sh
sudo bash e2e/run.sh test_metrics_cpu_runqlat_tracing.sh
sudo bash e2e/run.sh test_autotracing_cpuidle.sh
sudo bash e2e/run.sh test_autotracing_dload.sh
```

These cases need local Kubernetes/kubelet access. The reference environment is a VM with 4 CPUs and 8 GB of memory. Install `stress-ng`; CPU util/stat also require `sysstat`. The container runtime must be able to obtain workload images. CPU reference collection, cpuidle, and dload also require Docker.

CPU metrics support cgroup v1/v2. Runqlat and cpuidle require the relevant kernel sampling capabilities. Dload functional checks require Legacy cgroups, taskstats, dm-delay, `dmsetup`, `losetup`, `mkfs.ext4`, `modprobe`, `nsenter`, mount privileges, and an available loop device. Other cgroup modes report unsupported behavior and a skip reason.

### Apiserver

```bash
sudo bash integration/run.sh test_apiserver_profile_disabled.sh
sudo bash integration/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling.sh
sudo bash e2e/run.sh test_apiserver_profiling_matrix.sh
```

Integration cases cover authentication, capabilities, parameters, and invalid requests. E2E covers profile results, pagination, flamegraphs, and the language/sampling matrix. Run `test_apiserver_profiling_{stop,concurrent,recovery,node_recovery}.sh` separately for stopping, concurrency, and recovery scenarios.

API checks without storage do not need Docker, perf, or a compiler. Sampling tests need Docker, curl, jq, ss, timeout, perf settings that allow sampling, and built collection tools. Native host workloads also need gcc.

The matrix additionally requires Python 3, ip, kubectl administration access, crictl, local kubelet credentials, and asprof/py-spy. `APISERVER_TEST_NODE` can select the local node. Otherwise, the test matches it automatically and fails if the match is ambiguous.

Default images are `gcc:14`, `golang:1.24`, `eclipse-temurin:17-jdk`, and `python:3.11-slim`. Override them with the corresponding `APISERVER_TEST_*_IMAGE` variables. Image access is required at runtime. The workspace `summary.tsv` records each matrix combination's result.

### Profiling

```bash
sudo bash integration/run.sh test_profiler_native_cpu.sh
sudo bash integration/run.sh test_profiler_native_cpu_offcpu.sh
sudo bash integration/run.sh test_profiler_native_memory_virtual_alloc.sh
sudo bash integration/run.sh test_profiler_native_memory_physical_usage.sh
sudo bash integration/run.sh test_profiler_java_cpu_multi_pid.sh
sudo bash integration/run.sh test_profiler_java_memory_usage_alloc.sh
sudo bash integration/run.sh test_profiler_python_cpu_multi_pid.sh
```

Java/Python cases need the corresponding collection tools in `PROFILER_TOOL_DIR`. Container, thread-group, and CPU-specific tests are `test_profiler_native_cpu_container.sh`, `test_profiler_native_cpu_thread_group.sh`, and `test_profiler_native_cpu_cpuid.sh`.

### Storage and Resource Limits

These storage tests do not require Elasticsearch:

| Test | Checks |
| --- | --- |
| `test_basic_storage_elasticsearch_config.sh` | Elasticsearch configuration completeness and startup behavior |
| `test_basic_storage_localfile_rotation.sh` | LocalFile rotation, retention count, and event data |

LocalFile rotation needs kernel BTF, the `skb/kfree_skb` tracepoint, and built Dropwatch tools.

```bash
sudo bash integration/run.sh test_node_runtime_limits.sh
```

The resource-limit test checks CPU and memory configuration during startup and normal operation. It needs writable CPU and memory controllers; cgroup v2 also needs accessible systemd. Run on both v1/hybrid and v2 environments to establish coverage of both hierarchies. A busy environment or missing capabilities produces SKIP. This case does not test CPU throughput or OOM behavior.

### IO

Run on a dedicated Linux test node with BPF/BTF support. IO kernel tests require Go. IOCOST also needs the BPF compiler toolchain and writable `io.cost` or `blkio.cost` controls:

```bash
sudo bash integration/run.sh test_iocost.sh
sudo env TEST_IOCOST_MODE=pressure bash integration/run.sh test_iocost.sh
```

`TEST_IOCOST_MODE` accepts `functional`, `lifecycle`, `faults`, `pressure`, and `compat`; the default is `all`. IO tests prefer temporary test disks. Without one, they perform only read-only checks on a suitable existing disk, or skip if none is available.

Real NVMe/SCSI health evidence requires an explicitly selected, dedicated non-system device:

```bash
sudo env TEST_IO_CONFIRM_NON_SYSTEM=1 TEST_NVME_DEVICE=nvme0 \
  bash integration/run.sh test_io_kernel.sh
```

For SCSI, use `TEST_SCSI_DEVICE=sdb`. NVMe and SCSI require `nvme-cli` and `smartmontools`, respectively.

### SchedBlame

```bash
sudo bash integration/run.sh test_sched_blame.sh
```

Use an isolated Linux 4.18–6.5 test environment with BTF, scheduler and cgroup hooks, a local Kubernetes node, kubelet authentication, and a cached `BUSINESS_POD_IMAGE`. Unsupported kernels or missing capabilities produce SKIP.

Success prints `sched-blame live collection verified`. Failed runs retain CSV data, command logs, and Pod diagnostics in the workspace. This case verifies live collection and contention scenarios, not exact resource accounting.
