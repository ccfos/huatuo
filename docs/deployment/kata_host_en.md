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

## Shared VM CPU attribution

Members of one Kata sandbox share the Host CPU CSS. The earliest discovered
live member represents that VM; the container ID breaks timestamp ties.
Refreshing metadata preserves this priority. When the member exits, the next
live member takes over. Run queue latency is reported only by that member.

CPU autotracing groups state by sandbox, applies its container filter before
selecting a representative, and includes `sandbox_id` in trace data. A member
handoff preserves usage history and the tracing cooldown. A changed VM CPU CSS
or path resets the usage baseline while retaining the cooldown. A new sandbox
starts with fresh state. Host Kata bindings are checked again during periodic
container reconciliation; read-time CPU cache refreshes do not trigger another
lifecycle reconciliation.

For authenticated Guest event and profiling ingress, see [Guest-to-Host transport](kata_transport_en.md). The receiver is disabled by default.

Host Kata records do not publish application memory snapshot targets because the hypervisor memory cgroup is shared by the VM. Capture application memory snapshots in the Guest. Ordinary Host containers retain their existing memory watches.
