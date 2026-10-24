# Kernel mock integration testing

Tests run the production BPF program and Huatuo collector against an
ioctl-triggered kernel fixture, then verify Elasticsearch documents, metrics,
and native memory profiles.
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

## Fixture contract

Each feature owns its request types, kernel argument construction, scenarios,
and checker. Triggering uses a typed, input-only `*_RUN` ioctl; it returns
success or an errno, never an expected event. Shared helpers manage execution,
cleanup, storage access, and document metadata without feature dispatch.

Obtain expected values in userspace where possible. OOM uses `getpid()` on the
host. Inside a container, the trigger and its child read their host TGIDs from
`/host-proc/self/status`, using a read-only mount of the host's procfs. The child
reports its TGID through the readiness pipe; the kernel request still uses its
container-local PID. Synthetic victim PIDs are fixed fixture inputs.

Container memory CSS comes from Huatuo's container API, passed to the trigger
with `--memory-css`. Only host/synthetic CSS addresses require the separate
`OOM_MOCK_GET_CSS` ioctl. It returns CSS addresses only and does not invoke BPF.
`OOM_MOCK_RUN` triggers the event and returns no fixture data. Keep actors alive
and in the same cgroups between observation and triggering. The synthetic chain
is immutable until module unload; per-call task references and allocations are
released within each ioctl.

The checker builds expectations from scenario inputs and prepared identities,
then compares them with production output. Fixture observations are independent
of BPF output. Normal task/container scenarios and synthetic structure tests are
identified separately.

## Prerequisites

- Root on Linux 4.18 or newer, x86_64 or aarch64.
- Prepared build headers for the running kernel, GCC, make, insmod, and rmmod.
- Target BTF for production CO-RE programs; memory cgroups for OOM.
- Event fixtures: Docker, curl, jq, timeout, and access to the Elasticsearch image.
- Huatuo build output including the checkers in `_output/tests/`; Go is not
  required to run the mock scripts.
- Hung task detection and event tracing enabled, tracefs mounted, and the
  build tree containing `include/trace/events/sched.h`. Compaction also needs
  `include/trace/events/vmscan.h` and `CONFIG_COMPACTION`.

The container case also needs Kubernetes on the test node, working kubelet
credentials, kubectl, and static libc development libraries. Preload
`BUSINESS_POD_IMAGE` (default `busybox:1.36.1`) in the node runtime; the pod uses
`imagePullPolicy: Never`. Idle fixture pods use a one-second termination grace
period; cleanup still waits for namespace deletion.

## Run tests

`make build` prepares all eight checkers as static executables in `_output/tests/`.
`make integration` also runs the tagged checker/helper Go tests before the shell
cases, so that build/test target still requires Go. VM CI runs it through
`make test`. To run only the Go tests:

```sh
go test -tags integration ./integration/bpf_mock/...
```

Within one runner invocation, BPF mock cases and the ES8 compatibility case
reuse an authenticated ES8 instance with separate indices. The runner owns
shutdown and preserves logs on failure; standalone scripts own their instances.

Use `bash integration/run.sh -j 3` to overlap reviewed mock cases. Host and
container variants serialize, as do physical allocation/usage mocks sharing
rmap symbols. New cases default to exclusive execution until reviewed. Shared
storage stays alive until the parallel batch finishes; serial runs stop it
after the last consumer. All scenarios and final assertions still execute.

Build once, then run the host mock tests:

```sh
make build
for test in memory_oom_kill system_softlockup system_hungtask; do
    sudo bash integration/run.sh "test_events_${test}_mock.sh"
done
```

Run a single test by its script name. Run container OOM and reclaim separately:

```sh
sudo bash integration/run.sh test_events_memory_oom_kill_mock_container.sh
sudo bash integration/run.sh test_events_memory_reclaim_events_mock_container.sh
```

Run the map-backed compaction metrics fixture and native memory profiles:

```sh
sudo bash integration/run.sh test_metrics_memory_free_compact_mock.sh
for feature in native_physical_alloc native_physical_usage native_virtual_alloc; do
    sudo bash integration/run.sh "test_profiler_${feature}_mock.sh"
    sudo bash integration/run.sh "test_profiler_${feature}_mock_container.sh"
done
```

The native fixtures use the production `profiler --bpf-mock` CLI and verify its
final folded profiles. The flag selects only the requested native memory object;
container CSS discovery remains real. These programs publish profiles rather
than event documents; compaction publishes Prometheus metrics rather than ES
events. The trigger waits for SIGUSR1 before issuing its input-only ioctl and
stays alive until SIGUSR2, after the profiler has finished symbolizing stacks.
Container triggers need static pthread linking in the build environment.
Only physical modes set `--physical-memory-probability 100`. Each scenario saves
`kernel-symbols.log` with fixture addresses for stack-symbolization diagnostics.

Set `sudo env BPF_MOCK_NODE=<local-node-name>` for a node without existing pods;
otherwise the container test discovers the local node from kubelet's pods.
The selected node must be the machine running the test: its module and trigger
are mounted into the pod.

Modules use `/lib/modules/$(uname -r)/build`. For an alternative prepared build
directory matching the running kernel, pass `sudo env KERNEL_BUILD_DIR=...`.

### Machines without Go

Run `make build` on a build machine targeting the test machine's architecture.
Transfer `_output/tests/` along with the daemon and BPF objects from the same
revision. Run the shell cases with `bash integration/run.sh`; they use
`_output/tests/<feature>-mock-checker` without compiling or copying checkers into
each workspace. A missing or non-executable checker fails the case.

An explicit checker location is still supported for individual tests:

```sh
sudo env BPF_MOCK_CHECKER_BIN=/path/to/memory_oom_kill-mock-checker \
    bash integration/run.sh test_events_memory_oom_kill_mock.sh
```

Set the variable to the corresponding checker for each test.

## Coverage

Each event test writes to a separate index in authenticated Elasticsearch 8 and
disables local-file storage. Its checker compares published documents with
identities recorded by the trigger. Missing, duplicate, unexpected, or
mismatched documents fail.

OOM, softlockup, hungtask, and reclaim triggers set a fresh random task name for
each invocation. Their checkers select documents by that name, then verify
PID/TID and the remaining fields. This avoids ambiguity when task IDs are reused.
Rejected scenarios require no matching document; producing scenarios require
exactly one. A final document-count check rejects extra events.

### OOM

The host test runs six real-task scenarios:

| Scenario | Expected ES documents |
| --- | ---: |
| Same task as victim | 1 |
| Separate live child as victim | 1 |
| Same task with non-null mock memcg | 1 |
| Separate victim with non-null mock memcg | 1 |
| Null control | 0 |
| Null victim | 1 |

An additional synthetic `pointer-chain` case constructs a victim → cgroups →
CSS chain with a fixed victim PID and a CSS distinct from the trigger. It checks
field traversal; it does not represent a real container identity.

The checker decodes ES payloads as `esOOMData`, with trigger and victim records
of type `esTask`. `FindDocumentByTriggerComm` selects the document; `actual()`
extracts its task identities for comparison with the event built by `expected()`.
Document metadata, memory snapshots, and container enrichment are verified
afterward.

Together they verify actor PIDs, command names, CSS addresses, event metadata, host
snapshots, and six host-counter increments. Host events must have no container
attribution or cgroup snapshots. The mock never charges a real cgroup. Manual trigger syntax is
`trigger <scenario> <total-pages> [usage-pages]`. Container runs additionally use
`--host-proc /host-proc --memory-css <API-memory-CSS>`; this assumes real actors
share that container's memory cgroup and excludes the synthetic pointer chain.

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
inputs from main and worker threads. The hook runs in hardirq context via
`irq_work`, with preemption disabled in the caller so its identity stays current.
The callback checks its context; kernels lacking hardirq delivery fail explicitly.
The test verifies two ES documents with
exact TGID, CPU, task name, metadata, and a stack string. Rejected
flags produce no event; the kernel softlockup taint bit remains unchanged.
Each positive case uses a fresh daemon and increments its counter once,
respecting production backoff.

### Hung task

The module generates `sched/sched_process_hang_mock` from the target kernel's
tracepoint declaration and compares real/mock field layouts. Main and worker
cases verify two ES documents with exact TID, task name, kernel
timeout, metadata, and both stack strings. Each uses a fresh daemon and
increments its counter once. A kernel worker emits the tracepoint for the ioctl
caller, so the reported task differs from `current` and a current-task regression
is detected. No task is blocked. Validate fixed-array and
dynamic-string comm layouts on kernels providing each layout.

### Memory reclaim events

`try_to_free_pages_mock` keeps the target function's signature and sleeps for
20 ms without reclaiming memory. Both production entry and return probes attach
to it. The module derives the signature from the target kernel declaration in
`include/linux/swap.h` or `mm/internal.h`, preserving nodemask constness without
version guards. If the declaration is private, the prepared kernel source tree
must provide `mm/internal.h`.

The collector publishes only container events and uses CPU CSS for attribution.
The test runs real container main/worker tasks with a reporting threshold of
1 ns, then a fresh daemon with the maximum signed TOML threshold. It expects
two `memory_reclaim` ES documents and no filtered documents. A host call also
produces no published document. Host PID/TID come from read-only
`/host-proc/thread-self/status`; duration must be at least 20 ms and no greater
than the independently timed userspace ioctl. The test validates container ID,
command names, PID/TID, metadata, and that the pod remains running.

CPU CSS is verified through attribution, since the ES schema omits its address.
The filter phase observes a reader/storage flush window before shutdown. These
scenarios exercise normal paired hooks; map-allocation failures and orphaned
return probes are not injected.

### Memory free/compaction metrics

`memory_free_compact` clones both direct-reclaim tracepoint layouts from the
target kernel's `vmscan.h` and derives the compaction hook signature from
`compaction.h`. Runtime checks compare the real and mock tracepoint fields.
No pages are reclaimed or compacted: paired hooks sleep for 20 ms.

Two fresh collectors start with reclaim first or compaction first. Each receives
two calls per path, exercising initial map insertion and later accumulation.
Unmatched reclaim ends before and after the pairs must not add latency. Both
published millisecond counters start at zero and finish between 40 ms and the
sum of independently measured userspace ioctl durations. The production BPF
has no cgroup behavior, so this fixture has no container variant.

### Native physical allocation

The fixture allocates temporary order-0 and order-2 kernel pages and calls the
mock rmap hooks without changing real reverse mappings. Mock signatures follow
the target kernel's `rmap.h`. Page hooks receive one call per base page; folio
hooks receive the compound folio. The production loader chooses its normal hook
and counting layout. The expected profile is exactly five host page sizes.

### Native physical usage

Scenarios cover five pages retained, balanced allocations/frees, two pages left
after partial removal, and an untracked free. Balanced calls include repeated
frees, which must not produce extra negative samples. Another allocation reuses
the event buffer before the first allocation is freed, checking that saved
allocation stacks survive reuse. Folio hooks remove one compound folio in
batches; page hooks remove the equivalent base pages separately.

Temporary backing pages are released after each ioctl. "Retained" refers to
mock profiler accounting; it does not leave allocated kernel memory behind.
Each scenario has a fresh profiler, so retained map identities cannot outlive
that profile or collide with a later scenario's allocations.

### Native virtual allocation

The mixed scenario calls `do_mmap_mock` with anonymous lengths 4096 and 8192,
a zero length, and a non-null file with length 16384. The expected total is
12288 bytes. A file-only scenario must produce no samples. The mock derives
the target `do_mmap` prototype, including removed flags and aggregate flag types;
it creates no real mappings.

All three native fixtures run host main-thread and worker cases. Main-only PID
filtering must reject a worker; thread-group filtering must capture it. Container
variants select a real memory CSS through Huatuo's API, run main/worker triggers
in that container, and reject a simultaneous host trigger. They check exact
published byte totals and process identities, require a mock hook in the
symbolized stacks, reject lost samples, and leave the pod running without
restarts. Sampling uses 100 percent for deterministic results. Random sampling
rates, internal allocation failures, and every possible stack-map failure are
not integration scenarios.

## Cleanup and debugging

Tests stop their daemon and unload their module. The runner removes shared ES;
direct script execution removes its own instance. The container case also
deletes its namespace. Successful runs remove
the temporary workspace; failures preserve prepared identities, ES responses, and
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
The hungtask mock uses a tracepoint and passed on this kernel, so it continues
to run. Other kernel releases still run all mock tests; attachment failures
remain fatal.

## Adding a fixture

Use the production BPF source basename for the fixture directory and every
feature source file. For example, `bpf/memory_reclaim_events.c` uses
`integration/bpf_mock/memory_reclaim_events/`. Role suffixes identify the kernel
module (`_mock_kmod.c`), ioctl header (`_mock.h`), and optional supporting sources;
the userspace trigger is `<basename>_trigger.c` and the checker is `<basename>.go`.
`Kbuild` keeps its conventional name. Source filenames follow this rule even
when the collector, published tracer, or kernel hook has a different name.

Use this layout, with one integration script per host/container test:

```text
integration/bpf_mock/common/Makefile
integration/bpf_mock/<feature>/
  Kbuild
  <feature>_mock.h
  <feature>_mock_kmod.c
  <feature>_trigger.c
  checker/<feature>.go
  checker/<feature>_test.go
integration/test_<output>_<feature>_mock.sh
integration/test_<output>_<feature>_mock_container.sh  # When cgroup behavior exists
```

Use `events`, `metrics`, or `profiler` for `<output>`, matching the published
output being verified.

The shared Makefile handles `all`, `clean`, and the kernel build directory.
Each fixture's Kbuild declares its module objects and generated-header rules.
`build_bpf_mock_module` copies both into an isolated temporary build directory,
so each feature builds independently. The runner caches successful modules
under its temporary workspace for the same checkout and kernel. Host, container,
and repeated cases reuse them; successful runs remove the cache. Direct scripts
build modules privately. All cases use the packaged checkers from `_output/tests/`,
with `BPF_MOCK_CHECKER_BIN` available as an explicit override.

The runner discovers `test_*_mock.sh` and `test_*_mock_container.sh` without
feature registration. Scripts source `integration/bpf_mock/common/lib.sh`, the
public entry point. It defines helpers without starting services. The runner
supplies `ROOT_DIR` and `HUATUO_BAMAI_TEST_TMPDIR`.

`core.sh` shares platform checks, module builds, daemon cleanup, and checker
execution without depending on ES. `elasticsearch.sh` handles ES setup,
document retrieval, and `cleanup_bpf_mock_es`, which combines core and storage
cleanup. Shared ES and HTTP/kubelet configuration writers live in
`integration/config.sh`.
Non-ES tests use `cleanup_bpf_mock` and verify their feature's output directly.
`require_bpf_mock_platform` checks the shared kernel/architecture requirements;
call `skip_known_mock_kprobe_kernel` separately for fixtures using kprobes.
`task_identity.h` reads userspace or mounted-procfs PID/TID identities, while
`ReadProfileInput` parses native checker inputs. Each module declares its ioctl
handler and misc device explicitly; ordinary fixtures use the kernel's
`module_misc_device()` for registration, while OOM keeps its custom initialization.
Modules and devices use `<feature>_mock`. Each device defines named ioctl commands
in its feature header: `RUN` uses number 1 and OOM's `GET_CSS` uses number 2.
Command numbers are local to each device. Rebuild the module and trigger together
when changing their shared request ABI.
`task.h` shares thread execution, optional CPU pinning, timing, host IDs, and
the profiler signal gate.
Ordinary triggers emit scenario, PID, TID, CPU, comm, and elapsed time in one
identity format; OOM adds victim/CSS observations and names its command field
`trigger_comm`. The four event checkers decode these records as `triggerOutput`
into `expectedRaw`, with the recorded task name in `TriggerComm`. ES payload types
use an `es` prefix and expose a `TriggerComm()` method for the shared
`FindDocumentByTriggerComm` lookup. It selects at most one document and rejects
duplicates. `expected()` builds the scenario's expected values from `expectedRaw`;
`actual()` extracts comparable values from the ES document. A nil event represents
an expected or actual absence. Metadata, stack presence, container data, and
duration bounds remain explicit in `verifyScenario()`.
Metric and profiler checkers keep their own output formats: trigger
durations bound actual metric values, while each native feature computes its
expected total bytes and shares folded-profile verification.
`verify_bpf_mock_es_events` polls ES with the
checker, passing the ES document file as its first argument and the remaining
feature arguments afterward. The checker owns expected document counts. Readiness checks share
`await_bpf_mock_attached`; they still depend on production attachment log messages.
Keep scenario construction and assertions in the feature directory.

`container.sh` shares pod setup, container metadata/CSS discovery, health checks,
failure diagnostics, and namespace deletion. Scripts
use `create_bpf_mock_namespace` to record ownership before calling
`create_bpf_mock_pod namespace pod trigger_dir device`. This keeps cleanup valid
when pod setup fails. `read_bpf_mock_container namespace pod controller output`
performs one discovery attempt for `memory` or `cpu`; scripts set the wait policy.
`verify_bpf_mock_container_alive namespace pod expected_id` checks the named
trigger container. Scripts retain their scenario order and cleanup sequence.

### Minimal host event test

This template assumes an `example` collector, `example.o`, and its fixture and
checker are implemented. Replace the names, event map, trigger arguments, and
checker arguments to match the feature. Add feature prerequisites and counter
assertions as demonstrated by the existing tests.

```bash
#!/usr/bin/env bash
set -euo pipefail
source "${ROOT_DIR}/integration/bpf_mock/common/lib.sh"

require_bpf_mock_platform
require_commands make gcc insmod rmmod jq
fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
trigger="${HUATUO_BAMAI_TEST_TMPDIR}/example-trigger"
events="${HUATUO_BAMAI_TEST_TMPDIR}/events.json"
build_bpf_mock_module example "${fixture_dir}"
compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/example/example_trigger.c" "${trigger}" -pthread

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
load_bpf_mock_module example "${fixture_dir}"

write_example_config() { write_bpf_mock_es_config example; }
integration_huatuo_bamai_start write_example_config \
    --region dev --disable-kubelet --log-debug \
    --bpf-mock --bpf-mock-object example.o
await_bpf_mock_attached example_perf_events kprobe:example_hook_mock

"${trigger}" same-task > "${HUATUO_BAMAI_TEST_TMPDIR}/same-task.json"
stop_bpf_mock_daemon
verify_bpf_mock_es_events example "${events}" "${HUATUO_BAMAI_TEST_TMPDIR}" same-task
```

### Fixture and ownership rules

The original fixture module implementations, shared device header, and
tracepoint wrappers are dual-licensed under `GPL-2.0 OR Apache-2.0`; Kbuild
files use Apache-2.0. Generated tracepoint declarations copied from the target
kernel remain under that kernel source's license and are not relicensed by
the fixture headers. Modules keep `MODULE_LICENSE("GPL")` for kernel loading.

- Define typed requests in `<feature>_mock.h`. `*_MOCK_RUN` is input-only and
  returns success or an errno. Obtain userspace information before triggering;
  use separate preparation queries only for kernel-only observations.
- The module preserves the production hook ABI or tracepoint layout. Construct
  kernel arguments and keep referenced objects alive through the hook call.
- The trigger records independent expected identities. The checker compares
  scenario expectations with production output; never derive expectations from
  the BPF event being verified.
- Use `load_bpf_mock_module` to record module ownership after loading.
  Cleanup unloads only the owned module and preserves failure/skip status. The runner owns shared ES;
  direct execution owns its private ES instance.
- Use `create_bpf_mock_namespace namespace pod` before pod setup. Shared
  cleanup prints failure diagnostics, stops the daemon, deletes the namespace,
  and unloads the module.

Check target kernel sources for the hook signature, fields, semantics, and
pointer lifetimes. Add compatibility guards only when these differ, preferring
feature/field checks.
Add real container coverage when the BPF program has cgroup-related behavior.
Each feature gets its own tests, with this shared guide for instructions.

The current fixtures verify event collectors through ES. Other BPF programs may
need ordered hook sequences or map verification. Perf-event and raw-tracepoint
programs need appropriate attachment support before using mock mode; do not
change their program type or helpers just to fit an ioctl fixture.
