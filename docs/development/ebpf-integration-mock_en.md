# Kernel mock integration testing

Tests run the production BPF program and Huatuo collector against an
ioctl-triggered kernel fixture, then verify Elasticsearch documents and metrics.
The mock hook supplies controlled inputs without causing an OOM, lockup, or
blocked task.

## Mock attachment

Start Huatuo with `--bpf-mock`. Kprobe and kretprobe targets become
`<symbol>_mock`; tracepoints keep their category and append `_mock` to the event
name. The mock must preserve the production function ABI or tracepoint layout.
Load its module before starting Huatuo. Missing hooks fail attachment.
Function offsets, raw tracepoints, and perf-event attachments are unsupported.

For container tests, keep discovery hooks real and select only the object under
test:

```sh
--bpf-mock --bpf-mock-object memory_oom_kill.o
```

Object selection is repeatable and requires `--bpf-mock`. Without selection,
mock mode applies to all loaded objects; enable only collectors with fixtures.

## Prerequisites

- Root on Linux 4.18 or newer, x86_64 or aarch64.
- Prepared build headers for the running kernel, GCC, make, insmod, and rmmod.
- Target BTF for production CO-RE programs; memory cgroups for OOM.
- Docker, curl, jq, timeout, and access to the Elasticsearch image.
- Go to build checkers, or a transferred checker executable (see below).

The container case also needs Kubernetes on the test node, working kubelet
credentials, kubectl, and static libc development libraries. Preload
`BUSINESS_POD_IMAGE` (default `busybox:1.36.1`) in the node runtime; the pod uses
`imagePullPolicy: Never`.

## Run tests

`make integration` runs the tagged checker/helper Go tests before the shell
cases. VM CI runs this target through `make test`. To run only the Go tests:

```sh
go test -tags integration ./integration/bpf_mock/...
```

Within one runner invocation, the BPF mock cases reuse an authenticated ES 8
instance, started when the first case needs storage. Each case writes to a
separate index. The runner removes the instance after the last mock case or
on failure, preserving ES logs on failure. Separate runner invocations and
direct script execution use their own instances.

Build once, then run the host mock tests:

```sh
make build
for test in memory_oom_kill softlockup; do
    sudo bash integration/run.sh "test_events_${test}_mock.sh"
done
```

Run a single test by its script name. Run container OOM separately:

```sh
sudo bash integration/run.sh test_events_memory_oom_kill_mock_container.sh
```

Set `sudo env BPF_MOCK_NODE=<local-node-name>` for a node without existing pods;
otherwise the container test discovers the local node from kubelet's pods.
The selected node must be the machine running the test: its module and trigger
are mounted into the pod.

Modules use `/lib/modules/$(uname -r)/build`. For an alternative prepared build
directory matching the running kernel, pass `sudo env KERNEL_BUILD_DIR=...`.

### Machines without Go

Build and transfer checkers from the same revision as the daemon and BPF objects:

```sh
for feature in oom softlockup; do
    go build -tags integration -o "_output/bin/${feature}-mock-checker" \
        "./integration/bpf_mock/${feature}/checker"
done

sudo env BPF_MOCK_CHECKER_BIN="$PWD/_output/bin/oom-mock-checker" \
    bash integration/run.sh test_events_memory_oom_kill_mock.sh
```

Set the variable to the corresponding checker for each test.

## Coverage

Each test writes to a separate index in authenticated Elasticsearch 8 and
disables local-file storage. Its checker compares published documents with
identities recorded by the trigger. Missing, duplicate, unexpected, or
mismatched documents fail.

### OOM

The host test runs seven scenarios:

| Scenario | Expected ES documents |
| --- | ---: |
| Same task as victim | 1 |
| Separate live child as victim | 1 |
| Same task with non-null mock memcg | 1 |
| Separate victim with non-null mock memcg | 1 |
| Constructed victim → cgroups → CSS pointer chain | 1 |
| Null control | 0 |
| Null victim | 1 |

It verifies actor PIDs, command names, CSS addresses, event metadata, host
snapshots, and six host-counter increments. Host events must have no container
attribution or cgroup snapshots. The mock never charges a real cgroup. Manual trigger syntax is
`trigger <scenario> <total-pages> [usage-pages]`.

The container test runs the same/separate-victim cases with null/non-null memcg,
plus null control and null victim. A trigger inside a real pod gives `current`
real container context; discovery probes remain on production hooks. The OOM
checker receives the discovered container ID and memory CSS, plus the expected
pod hostname, using `-container expected-container.json`. It verifies
container attribution, cgroup snapshots with the pod's 64 MiB limit, and five
ES documents. Counters follow the victim: four container increments and one
host increment for null victim. The pod must remain running without restarting.
Cross-container victims are not covered.

### Softlockup

`add_taint_mock` receives accepted `TAINT_SOFTLOCKUP` and rejected `TAINT_USER`
inputs from main and worker threads. The test verifies two ES documents with
exact TGID, CPU, truncated command name, metadata, and a stack string. Rejected
flags produce no event; the kernel softlockup taint bit remains unchanged.
Each positive case uses a fresh daemon and increments its counter once,
respecting production backoff.

## Cleanup and debugging

Tests stop their daemon and unload their module. The runner removes shared ES;
direct script execution removes its own instance. The container case also
deletes its namespace. Successful runs remove
the temporary workspace; failures preserve trigger replies, ES responses, and
logs. Cleanup failures fail the test. Missing environment prerequisites (tools
or the running kernel's build directory) skip the case; module compilation and
attachment failures fail it once prerequisites are present.

## Limitations

- Production ES documents omit OOM `mem_limit_pages` and `mem_usage_pages`, so
  those raw values are not asserted. Cgroup snapshots do not validate them.
- Tests cover the exercised paths on the tested kernel builds. Source inspection
  alone does not establish runtime compatibility with every vendor kernel.
- Grafana rendering is outside these tests.
- Softlockup/hungtask use real SysRq and `/dev/kmsg` collection. Stack fields
  may be empty or contain diagnostics; tests check their types, not completeness.

### Known Ubuntu kernel attachment limitation

On Ubuntu kernel `5.4.0-216-generic`, the OOM and softlockup modules and BPF
objects load, but attachment to `oom_kill_process_mock` and `add_taint_mock`
fails with `EADDRNOTAVAIL` ("cannot assign requested address"). See the
[OOM failure](https://github.com/ccfos/huatuo/actions/runs/38233705136/job/114764618198)
and [softlockup failure](https://github.com/ccfos/huatuo/actions/runs/38375800134/job/115223763730).

The suspected cause is duplicate module-symbol counting, as seen in the
[5.4 stable symbol iterator](https://github.com/gregkh/linux/blob/v5.4.293/kernel/kallsyms.c#L168)
and [kprobe symbol counting](https://github.com/gregkh/linux/blob/v5.4.293/kernel/trace/trace_kprobe.c#L692).
The exact Ubuntu source and live symbol counts remain unverified. This is a
known limitation of that kernel release. Host and container OOM mock tests
and the softlockup mock test skip `5.4.0-216-generic` before allocating resources.
Other kernel releases still run all mock tests; attachment failures
remain fatal.

## Adding a fixture

Keep the module (`<feature>_mock_kmod.c`), trigger, and checker under
`integration/bpf_mock/<feature>/`. Scripts handle daemon startup, ioctl calls,
and ES queries; checkers compare documents with trigger replies. Shared helpers
handle ES setup, JSON decoding, and event metadata.

Check target kernel sources for the hook signature, fields, semantics, and
pointer lifetimes. Keep mock objects alive through the synchronous hook call.
Add compatibility guards only when these differ, preferring feature/field checks.
Add real container coverage when the BPF program has cgroup-related behavior.
Each feature gets its own tests, with this shared guide for instructions.
