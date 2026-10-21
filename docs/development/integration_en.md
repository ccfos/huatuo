---
title: Integration Test
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

This integration test validates that **huatuo-bamai** can start correctly with mocked `/proc` and `/sys` filesystems and expose the expected **Prometheus metrics**.

The test runs the real `huatuo-bamai` binary and verifies the `/metrics`endpoint output without relying on the host kernel or hardware.

### What the Script Does
The integration test performs the following steps:

1. Generates a temporary `bamai.conf`
2. Starts `huatuo-bamai` with mocked `procfs` and `sysfs`
3. Waits for the Prometheus `/metrics` endpoint to become available
4. Fetches all metrics from `/metrics`
5. Verifies that all expected metrics exist
6. Stops the service and cleans up resources

If any expected metric is missing, the test fails.

### How to Run
Run the integration test from the project root:

```bash
bash integration/run.sh
```

Pass a file name to run one integration test. The optional second argument is
the repeat count and defaults to 1:

```bash
bash integration/run.sh test_metrics_exclude_filter.sh 10
```

or
```bash
make integration
```
### Prerequisites and Results

Both suites use `integration/run.sh`, which defaults to integration.
`e2e/run.sh` forwards to `--suite e2e`. Existing `make integration`, `make e2e`,
and single-case entry points remain available:

```bash
bash integration/run.sh --suite e2e
bash integration/run.sh --suite e2e test_metrics.sh 2
bash e2e/run.sh test_metrics.sh 2
```

Both suites run in separate UTS and mount namespaces with hostname `huatuo-dev`
and private mount propagation. Integration cases start their own services;
e2e starts the baseline bamai before each case, then stops it and checks its log.
Integration removes workspaces after passed or skipped cases and preserves them
on failure. E2E retains its workspaces.

The runner checks only that `_output` exists, without checking individual
project binaries or BPF objects. Run
`make build` before invoking a runner directly. An existing directory does not
prove that the build is complete; missing artifacts fail when used.

Use the same helper for commands on PATH and executable file paths. Missing
commands skip the case:

```bash
require_commands jq curl ss
require_commands "${PROFILER_TOOL_DIR}/bin/asprof"
```

Use `require_readable` for input files such as certificates. Unreadable inputs
skip the case and report the affected path:

```bash
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
```

`skip` prints its reason and exits with 77; `EXIT` cleanup still runs. The runner
classifies 0 as PASS, 77 as SKIP, and other statuses as FAIL. Cleanup failures also
count as FAIL. A suite containing only passed and skipped cases returns 0.
A failure stops subsequent cases and prints the results collected so far.

Each execution counts separately, including repetitions. For example:

```text
summary: total=5 passed=3 skipped=2 failed=0
```

When root privileges or namespace commands are unavailable, both suites count
all selected executions as SKIP.

#### On Failure

- The `huatuo-bamai` service metrics and logs are printed to stdout
- The temporary working directory is kept for debugging

#### On Success

- Output the list of successfully validated metrics

---

### Storage Tests

`test_basic_storage_elasticsearch_config.sh` covers all eight combinations of
Elasticsearch Address, Username, and Password. Partial configurations must exit
with the configuration error; empty and complete configurations must reach readiness.
The complete case uses `--disable-storage` to isolate configuration validation
from Elasticsearch availability.

`test_basic_storage_localfile_rotation.sh` reuses the storage consistency test's UDP
sender and configuration helper with only LocalFile enabled. It sends packets to
a closed UDP port until Dropwatch events rotate the local file and evict the first
observed backup, then checks file sizes, valid JSON, and the limit of two backups
plus one active file. It requires kernel BTF, the `skb/kfree_skb` tracepoint,
and the Dropwatch binary and BPF object. Neither storage test requires
Elasticsearch.

Both cases call `integration_isolate_run_directory` before starting the daemon
to isolate its PID lock and Toolstream socket from existing instances. These
cases and the backend-consistency case use `assert_huatuo_bamai_shutdown` before
final data and log checks; EXIT traps retain best-effort cleanup.
Callers of `write_storage_events_config` must
set `STORAGE_EVENT_RATE`, `STORAGE_ROTATION_MIB`, and `STORAGE_MAX_ROTATED_FILES`.

### Background Process Cleanup

`stop_and_wait_by_pid PID [TIMEOUT]` stops and waits for a child process,
returning its exit status. An omitted or empty PID returns success immediately,
so callers can pass an initialized PID variable without checking it first.
Clear the variable after waiting; use `|| true` in best-effort cleanup and
assert the exit status when it is part of the test contract.

### Network Namespace Helpers

Network tests can source `integration/lib_namespace.sh` after `lib.sh`:

- `namespace_setup NAME` creates one network namespace and exposes its name as
  `NETNS`. The caller brings up `lo` and configures any addresses it needs.
- `namespace_setup_with_pair PREFIX SERVER_ADDR CLIENT_ADDR [PREFIX_LENGTH]`
  creates two namespaces connected by a veth pair, configures their addresses,
  and brings up the interfaces. The prefix length defaults to 24. Names and
  addresses are exposed through `NETNS_SERVER`, `NETNS_CLIENT`,
  `NETNS_VETH_SERVER`, `NETNS_VETH_CLIENT`, `NETNS_SERVER_ADDR`, and
  `NETNS_CLIENT_ADDR`.
- `namespace_cleanup` deletes the namespaces created by either helper and
  clears their state. Call it from the test's `EXIT` cleanup after stopping
  processes in those namespaces. Repeated cleanup calls are supported.

### Shared Packet Sender

Tests that construct raw packets should reuse
`integration/testdata/test_packet_sender.py` and extend it for new workloads.
It requires Python 3 and raw-socket privileges, and supports `ipv4-options`
(TCP/UDP EOL options, including maximum IHL) and `icmpv6-echo`.
Run a subcommand with `--help` for its address, port, or Echo identity arguments.
The sender repeats traffic every 100 ms until SIGTERM and exits successfully.
Shell tests own namespaces, drop rules, assertions, logs, and process cleanup.
Workloads requiring real TCP connections continue to use `testdata/tcp_server.py`.

### How to Add New Metrics Tests
#### 1: Add or Update Fixture Data

If the metric depends on /proc or /sys, add or update mock data under:
```bash
integration/fixtures/
```

The directory structure should match the real kernel filesystem layout.
#### 2: Add Expected Metrics

Create a new file under:
```bash
integration/fixtures/expected_metrics/
├── cpu.txt
├── memory.txt
└── ...
```

Each non-empty, non-comment line represents one expected Prometheus metric line
and must match the /metrics output exactly.

New *.txt files are automatically picked up by the test.

#### 3: Run the Test
```bash
bash integration/run.sh
```
The test fails if any expected metric is missing or mismatched.
