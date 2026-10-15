# Kata containers on the Host

Host discovery uses the configured containerd CRI endpoint. Verbose running
container status must include `runtimeType`, a positive Host hypervisor `pid`,
and `sandboxID`. Supported names are `io.containerd.kata.v2` and
`io.containerd.kata-*.v2`, including QEMU and Firecracker variants.

The Host must expose its proc and cgroup trees to the agent. The hypervisor's
actual membership determines the CPU path; the directory inode selects the
CPU CSS reported by BPF. Missing or conflicting CPU CSS keeps discovery pending
for the existing controller retry flow. The VM network namespace is not
published as a Guest container network namespace. Host Kata records expose
`runtime_type` and `sandbox_id`; ordinary container PID-file discovery remains
in use after CRI runtime identification.

For cgroup v1/hybrid, CPU, CPU accounting and memory membership must have the
same path because the shared container metadata currently has one resource
path. Missing CPU accounting or quota files are rejected. For v2, `cpu.stat`
and `cpu.max` must exist. The optional `cpu_weight_nice_read_s64` hook refreshes
CPU CSS for CPU-only cgroups; if it is unavailable, existing lifecycle discovery
continues. An unresolved CPU CSS is not replaced with the overhead cgroup CSS.

This describes Host metadata and CPU attribution. Guest data transport is a
separate feature. See [Guest deployment](kata_guest_en.md) for Guest startup.

## Read-only verification on an existing Host

Build with `make build`, then select an already running Kata container:

```sh
HUATUO_BPF_OBJECT_DIR="$PWD/bpf" \
HUATUO_KATA_CONTAINER_ID=<full-container-id> \
go test -tags integration ./internal/pod \
  -run TestKataRuntimeIntegration -v -count=1
```

This attaches temporary BPF probes, reads CRI/proc/cgroup data, and releases the
probes on exit. It does not restart or delete the selected container. Run QEMU
and Firecracker checks separately; passing one does not validate the other.
