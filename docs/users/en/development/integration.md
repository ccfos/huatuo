---
title: Add Integration Test
type: docs
description:
author: HUATUO Team
date: 2026-01-30
weight: 5
---

This integration test validates that **huatuo-bamai** can start correctly with mocked `/proc` and `/sys` filesystems and expose the expected **Prometheus metrics**.

The test runs the real `huatuo-bamai` binary and verifies the `/metrics`endpoint output without relying on the host kernel or hardware.

## What the Script Does
The integration test performs the following steps:

1. Generates a temporary `bamai.conf`
2. Starts `huatuo-bamai` with mocked `procfs` and `sysfs`
3. Waits for the Prometheus `/metrics` endpoint to become available
4. Fetches all metrics from `/metrics`
5. Verifies that all expected metrics exist
6. Stops the service and cleans up resources

If any expected metric is missing, the test fails.

## How to Run
Run the integration test from the project root:

```bash
bash integration/integration.sh
```
or
```bash
make integration
```
### On Failure

- The `huatuo-bamai` service metrics and logs are printed to stdout
- The temporary working directory is kept for debugging

### On Success

- Output the list of successfully validated metrics

---

## How to Add New Metrics Tests
### Step 1: Add or Update Fixture Data

If the metric depends on /proc or /sys, add or update mock data under:
```bash
integration/fixtures/
```

The directory structure should match the real kernel filesystem layout.
### Step 2: Add Expected Metrics

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

### Step 3: Run the Test
```bash
bash integration/integration.sh
```
The test fails if any expected metric is missing or mismatched.