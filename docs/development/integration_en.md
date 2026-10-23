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

## Prerequisites and Results

Both suites share `integration/run.sh`, which runs integration tests by default; `e2e/run.sh` forwards to `--suite e2e`. The `make integration`, `make e2e`, and single-case entry points remain available:

```bash
bash integration/run.sh --suite e2e
bash integration/run.sh --suite e2e test_metrics.sh 2
bash e2e/run.sh test_metrics.sh 2
```

Both suites run in isolated UTS and mount namespaces, with hostname `huatuo-dev` and private mount propagation. Integration cases start their own services. E2E starts the default bamai before each case, then stops it and checks logs. Integration removes the working directory after a pass or skip and retains it on failure; E2E retains it for all outcomes.

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

## Storage Tests

Neither of these cases requires an Elasticsearch service:

| Test | Checks |
| --- | --- |
| `test_basic_storage_elasticsearch_config.sh` | All eight combinations of `Address`, `Username`, and `Password`: partial configurations must exit with a configuration error; empty and complete configurations must reach readiness. The complete case uses `--disable-storage` to isolate backend availability. |
| `test_basic_storage_localfile_rotation.sh` | Enables only LocalFile and sends packets to a closed UDP port to trigger rotation through Dropwatch events. Checks that the first backup is evicted, two backups and one active file remain, and each file is at most 1 MiB and contains valid JSON for the target events. |

The rotation case requires kernel BTF, the `skb/kfree_skb` tracepoint, and the Dropwatch binary and BPF object. It shares UDP sending and configuration helpers with the backend consistency test. Callers of `write_storage_events_config` must set `STORAGE_EVENT_RATE`, `STORAGE_ROTATION_MIB`, and `STORAGE_MAX_ROTATED_FILES`.

Both cases call `integration_isolate_run_directory` before starting the service to isolate its PID lock and Toolstream socket. The configuration, rotation, and backend consistency cases call `assert_huatuo_bamai_shutdown` before checking final data and logs to confirm a clean shutdown; `EXIT` traps handle best-effort cleanup.

## Background Process Cleanup

`stop_and_wait_by_pid PID [TIMEOUT]` stops and waits for a child process, returning its exit status. A missing or empty PID returns success immediately, so callers can pass initialized PID variables directly. Clear the variable after waiting. Use `|| true` for best-effort cleanup; assert the exit status when it is part of the test contract.

## Network Namespace Helpers

Network tests can source `integration/lib_namespace.sh` after `lib.sh`:

- `namespace_setup NAME` creates a network namespace and exposes its name through `NETNS`. The caller enables `lo` and configures addresses.
- `namespace_setup_with_pair PREFIX SERVER_ADDR CLIENT_ADDR [PREFIX_LENGTH]` creates two namespaces connected by veth, configures addresses, and enables interfaces. The prefix length defaults to 24. Namespaces, interfaces, and addresses are exposed through `NETNS_SERVER`, `NETNS_CLIENT`, `NETNS_VETH_SERVER`, `NETNS_VETH_CLIENT`, `NETNS_SERVER_ADDR`, and `NETNS_CLIENT_ADDR`.
- `namespace_cleanup` deletes namespaces created by these helpers and clears state. Repeated calls are supported. In `EXIT` cleanup, stop processes inside the namespaces before calling it.

## Shared Packet Sender

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
