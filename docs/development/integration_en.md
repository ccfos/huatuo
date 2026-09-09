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
#### On Failure

- The `huatuo-bamai` service metrics and logs are printed to stdout
- The temporary working directory is kept for debugging

#### On Success

- Output the list of successfully validated metrics

---

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

### Real cgroup CPU Capacity

`test_cpu_capacity.sh` checks ancestor and leaf quotas, different periods,
cpuset inheritance, resize configuration changes, and the CPU budget shared by
two sibling cgroups. It requires Linux cgroup v2, Go, and an explicitly delegated
writable directory with `cpu` and `cpuset` enabled in `cgroup.subtree_control`:

```bash
HUATUO_CPU_CAPACITY_CGROUP_ROOT=/path/to/delegated/test-cgroup \
  bash integration/run.sh test_cpu_capacity.sh
```

Use a dedicated test environment, not a production hierarchy. The test does not
enable controllers or change limits on the supplied directory. It creates an
exclusive subtree capped at one CPU, runs two two-second workers under a shared
half-CPU parent, then removes only its own processes and cgroups. Missing
prerequisites produce an explicit `SKIP`, not a verified result. A single-CPU
cpuset skips only the cpuset-resize assertion. Collector interval invalidation
is covered separately by the fake-clock CPU utilization unit tests.
