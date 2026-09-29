---
title: Integration Test
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

`integration/run.sh` runs the repository's `integration/test_*.sh` suite. It includes fixture-based metrics checks, API tests, and tests that exercise real BPF programs, process runtimes, cgroups, and network devices. The suite is not independent of the host kernel or hardware.

### Prerequisites and build

Run from the repository root on Linux. The runner requires **root (EUID 0)** and uses `unshare --uts --mount`, mounts, and BPF loading. Root inside a restricted container may still lack the required capabilities, including `CAP_SYS_ADMIN` and the kernel's BPF/perf permissions. Individual tests also check prerequisites such as kernel tracepoints, PMU access, installed runtimes, and helper commands.

Build the binaries, BPF objects, and configuration artifacts before invoking the runner directly:

```bash
make build
sudo bash integration/run.sh
```

Alternatively, `make integration` builds first and then invokes the same runner. Run it in a root environment with the build toolchain available; an unprivileged invocation can finish the build and then skip the entire test suite.

### Select and repeat tests

Without arguments, the runner executes all `integration/test_*.sh` files. Pass a file name (not a path) to select one test. The optional second argument is a positive repeat count, defaulting to 1:

```bash
sudo bash integration/run.sh test_metrics.sh
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
```

### Interpret the result

- Without root, the runner prints `[INTEGRATION][SKIP] ... requires root` and exits **0 without running tests**.
- Individual tests can also print a skip reason and exit 0 when a prerequisite is missing. The runner may subsequently print `passed` for that script. Inspect the test output for `SKIP`; a zero exit status or final success message alone does not prove every test executed.
- A failing test stops the suite. The runner stops its services, prints diagnostic text artifacts, and retains the failing temporary workspace. Its path is included in the failure log.
- Successful test workspaces are removed. The metrics fixture test prints the checked metric prefixes and matching metric lines.

### The metrics fixture test

`test_metrics.sh` uses the real `huatuo-bamai` binary with mocked `/proc` and `/sys` data. It generates a temporary configuration, starts the service, waits for `/metrics`, and compares the response with the expected metric fixtures. This limits host dependence for those metric inputs; it does not remove the runner's namespace and privilege requirements or the prerequisites of other tests.

To extend this test:

1. Add or update data under `integration/fixtures/`, preserving the kernel filesystem layout.
2. Add expected metric lines to `integration/fixtures/expected_metrics/*.txt`. Non-empty, non-comment lines must be present in the `/metrics` output. New `.txt` files are picked up automatically.
3. Rebuild after changing source code, then run `sudo bash integration/run.sh test_metrics.sh`. Missing or mismatched metrics fail the test.
