# Kernel mock integration testing

A kernel mock test runs the normal `huatuo-bamai` daemon and its production BPF
object against a controlled kernel hook. A test module exposes a mock function
with the entry ABI expected by the BPF program. An ioctl constructs the input
objects and calls that function. The BPF program, perf event reader, Go
collector, local storage, and metrics code are the same ones used in
production. The test changes only the function to which the BPF program
attaches.

The daemon selects a hook for one program in one BPF object with:

```text
--bpf-attach-override object.o:program_name=mock_kernel_symbol
```

Load the module before starting the daemon, since collectors attach their BPF
programs during startup. Without an override, each program uses the hook in
its BPF section as usual. No second BPF entry point is needed when the real
and mock functions have the same entry ABI.

## Adding a fixture

1. Check the exact target kernel sources for the real function's arguments,
   call context, pointer lifetimes, and the fields read by the BPF program.
2. Build the module against the running kernel's prepared build directory.
   Construct valid kernel objects for each scenario and keep them alive until
   the mock function returns. The mock function must not perform the real
   operation being observed.
3. Select the production BPF program explicitly with
   `--bpf-attach-override`. Start the normal daemon with the event collector
   and local storage enabled.
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
--bpf-attach-override memory_oom_kill.o:oom_kill_process=oom_kill_process_mock
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
