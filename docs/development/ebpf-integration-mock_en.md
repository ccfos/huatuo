# Kernel mock integration testing

A kernel mock test runs the normal `huatuo-bamai` daemon and its production BPF
object against a controlled kernel hook. A test module exposes a mock function
or tracepoint with the ABI expected by the BPF program. An ioctl constructs
the inputs and triggers that hook. The BPF program, perf event reader, Go
collector, local storage, and metrics code are the same ones used in
production. The test changes only the hook to which the BPF program
attaches.

Name each mock function `<production_symbol>_mock` and start the daemon with:

```text
--bpf-mock
```

Mock mode derives the target from each kprobe or kretprobe symbol. For example,
`oom_kill_process` attaches to `oom_kill_process_mock`. Load the module before
starting the daemon, since collectors attach their BPF programs during startup.
A missing mock symbol causes attachment to fail; there is no fallback to the
production symbol. Enable only collectors for which you have supplied fixtures.
For tracepoints, append `_mock` to the event name and keep the same category:
`sched/sched_process_hang` attaches to `sched/sched_process_hang_mock`.
The mock event must have the production event's field layout. Mock mode
rejects other attachment types, including raw tracepoints and perf events.

Mock mode rejects probes with function offsets because an offset in the
production function may refer to different code in its mock. The convention
applies to both default attachment and attachment with Go options. Without
`--bpf-mock`, each program uses its usual production target. No second BPF
entry point is needed when the real and mock functions have the same entry ABI.

## Adding a fixture

1. Check the exact target kernel sources for the real function's arguments,
   call context, pointer lifetimes, and the fields read by the BPF program.
2. Build the module against the running kernel's prepared build directory.
   Construct valid kernel objects for each scenario and keep them alive until
   the mock function returns. The mock function must not perform the real
   operation being observed.
3. Name the mock function or tracepoint event `<production_symbol>_mock`.
   Keep a tracepoint's production category. Start the normal daemon
   with `--bpf-mock`, the event collector, and local storage enabled.
4. Invoke the module from a test process. Assert the attached symbol, the
   emitted event, persisted data, and affected metrics. Add scenarios for
   distinct input or pointer paths.
5. Run the test on each supported kernel build. Add version-aware code only
   when the hook ABI, a read field, or its semantics actually differ. Prefer
   feature or field checks over a kernel version comparison where possible.

Keep the shared workflow in this guide. Each BPF program gets its own fixture
and test script as needed; it does not need a separate guide.

## OOM example

`integration/test_events_memory_oom_kill_mock.sh` is the first fixture. The
module in `integration/testdata/oom_mock` provides `oom_kill_process_mock` with
the same entry signature as `oom_kill_process`. Its ioctl handler creates an
`oom_control` with `chosen = current` and `totalpages` from the ioctl argument.
The task's real `task_struct -> cgroups -> subsys` chain remains available to
the BPF program. `memcg` is null, and the mock function does not trigger an
OOM kill.

```text
--bpf-mock
```

The test checks that the mock symbol and perf event pipe attach, then issues
one ioctl. It checks the saved event's trigger and victim, their matching
nonzero memory cgroup CSS addresses, a host memory snapshot, and an increase
in the host OOM counter.

Run it as root on Linux 4.18 or newer, on x86_64 or aarch64:

```sh
make build
sudo bash integration/run.sh test_events_memory_oom_kill_mock.sh
```

The module is built against `/lib/modules/$(uname -r)/build`. Set
`KERNEL_BUILD_DIR` to another prepared build directory for the **running**
kernel if needed. On success the runner stops the daemon, unloads the module,
and removes its temporary workspace. On failure it retains the workspace for
debugging.

The supplied upstream Linux git history has the same OOM entry signature and
BPF-read fields in release tags from v4.18 through v7.2 and in v7.3-rc5. No
version branch was needed for this fixture. The OOM BPF program requires memory
cgroups and target BTF for its CO-RE relocations. The mock path has been run
on 4.18; other kernels still need runtime results, including vendor kernels
with backports. The current test checks local storage and metrics; checking
external ES or Grafana services requires a separate deployment test.
