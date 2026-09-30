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

Run from the repository root on Linux. The runner requires **root (EUID 0)** and uses `unshare --uts --mount` and mounts; many cases also load BPF programs. Root inside a restricted container may still lack the required capabilities, including `CAP_SYS_ADMIN` and the kernel's BPF/perf permissions. Individual tests also check prerequisites such as kernel tracepoints, PMU access, installed runtimes, and helper commands.

Build the binaries, BPF objects, and configuration artifacts before invoking the runner directly:

```bash
make build
sudo bash integration/run.sh
```

The runner checks only that `_output` exists, not individual binaries or BPF objects. An existing directory does not prove that the build is complete; missing artifacts fail when used.

Alternatively, `make integration` builds first and then invokes the same runner. Run it in a root environment with the build toolchain available; an unprivileged invocation can finish the build and then skip the entire test suite.

### Select and repeat tests

Without arguments, the runner executes all `integration/test_*.sh` files. Pass a file name (not a path) to select one test. The optional second argument is a positive repeat count, defaulting to 1:

```bash
sudo bash integration/run.sh test_metrics.sh
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
```

Both suites use this runner, which defaults to integration. `e2e/run.sh` forwards to `--suite e2e`; `make e2e` remains available:

```bash
sudo bash integration/run.sh --suite e2e
sudo bash integration/run.sh --suite e2e test_metrics.sh 2
sudo bash e2e/run.sh test_metrics.sh 2
```

Both suites run in separate UTS and mount namespaces with hostname `huatuo-dev` and private mount propagation. Integration cases start their own services; e2e starts the baseline bamai before each case, then stops it and checks its log.

### Interpret the result

- Without root or the required namespace commands (`unshare` and `mount`), the runner counts all selected executions as SKIP and exits **0 without running tests**.
- Individual tests use `skip` to print a reason and exit **77**; `EXIT` cleanup still runs. The runner classifies 0 as PASS, 77 as SKIP, and other statuses as FAIL. Cleanup failures also count as FAIL.
- A suite containing only passed and skipped cases returns 0. Inspect the summary and skip reasons: a zero exit status alone does not prove every test executed.
- A failing test stops subsequent cases. The runner stops its services, prints diagnostic text artifacts, retains the failing temporary workspace, and reports the results collected so far.
- Integration removes workspaces after passed or skipped cases; e2e retains its workspaces. The metrics fixture test prints the checked metric prefixes and matching metric lines.

Each execution counts separately, including repetitions. For example:

```text
summary: total=5 passed=3 skipped=2 failed=0
```

### Check case prerequisites

Use the same helper for commands on PATH and executable file paths. Missing commands skip the case:

```bash
require_commands jq curl ss
require_commands "${PROFILER_TOOL_DIR}/bin/asprof"
```

Use `require_readable` for input files such as certificates. Unreadable inputs skip the case and report the affected path:

```bash
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
```

### The metrics fixture test

`test_metrics.sh` uses the real `huatuo-bamai` binary with mocked `/proc` and `/sys` data. It generates a temporary configuration, starts the service, waits for `/metrics`, and compares the response with the expected metric fixtures. This limits host dependence for those metric inputs; it does not remove the runner's namespace and privilege requirements or the prerequisites of other tests.

To extend this test:

1. Add or update data under `integration/fixtures/`, preserving the kernel filesystem layout.
2. Add expected metric lines to `integration/fixtures/expected_metrics/*.txt`. Non-empty, non-comment lines must be present in the `/metrics` output. New `.txt` files are picked up automatically.
3. Rebuild after changing source code, then run `sudo bash integration/run.sh test_metrics.sh`. Missing or mismatched metrics fail the test.
