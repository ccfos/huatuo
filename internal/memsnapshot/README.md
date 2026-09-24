# Process memory snapshots

`collector.Capture(ctx, identity, options)` captures one already selected process.
The caller supplies `ProcessIdentity` (TGID and start-time ticks); a bare PID is
not sufficient. Selection, container ownership and persistence belong to the
caller. The collector owns identity validation, process memory readings, runtime
detection, provider dispatch and output limits.

```go
result, err := collector.Capture(ctx, identity, collector.Options{
    TopK:           10,
    CaptureTimeout: 2 * time.Second,
    CheckTarget:    checkTarget,
})
if err != nil {
    return err
}
// Revalidate caller-owned bindings before persisting result.
```

Zero-valued options use TopK 10, a one-second detection timeout and a two-second
capture timeout shared by Go, Java and Python. TopK must be in [1, 100]; negative
timeouts are rejected. Deadlines are cooperative and cannot interrupt an
in-flight syscall. Parent cancellation or deadline expiry discards the result.

`CheckTarget` is optional, read-only and synchronous. The collector calls it
before detection, before provider dispatch and before returning a result, with a
one-second deadline bounded by the parent context. Callers can check cgroup and
container bindings here; the collector always checks process identity itself.
A successful check does not guarantee that the process stays valid afterwards.

`Result` contains the selected identity, language, capture time, sampling seed,
generic process memory and a bounded runtime snapshot. Unsupported runtimes
return an `unavailable` snapshot; detection or provider failures return a `failed`
snapshot. Both preserve available generic process memory. Invalid identities,
failed binding checks, invalid options and cancellation return an error without
a result. The collector does not save data or attach event/container metadata.

Production readers use `/proc` and the PID namespace used by remote memory reads.
Memory values are in bytes. Reads are approximate and do not stop the process;
the runtime snapshot is not an atomic or complete census of the process heap.

The memory-threshold action uses `processSelector.Select(ctx, group, limitBytes)`
to enumerate direct cgroup members within PID, byte and time limits. Its
`selectedProcess` contains identity, process name and `oom_score_adj`; ranking
fields remain private to `processCandidate`. It rejects incomplete enumeration,
revalidates the cgroup instance and selected identity, then passes the identity
to `captureProcessMemory`, which is bound to `collector.Capture`. The action
revalidates container/process bindings again before saving the returned result.
