# Runtime snapshot collector

`collector.Run` is the Linux entry point for collecting a managed runtime
snapshot from a PID. It has no dependency on event tracing, cgroups or storage.

```go
result, err := collector.Run(ctx, pid, collector.Options{TopK: 10})
if err != nil {
    return err
}
// Inspect result.Language and result.Snapshot, including Status and Reason.
```

Zero-valued options default to TopK 10, a 1 s detection budget, a 100 ms Go
capture budget and 2 s Java/Python capture budgets. Negative budgets and invalid
TopK values are rejected. Deadlines are cooperative and cannot interrupt a
blocked syscall. No background capture goroutine is left running after return.

The collector reads and revalidates the process start time, detects the runtime,
selects its provider, recovers provider panics, records duration and bounds the
result. `ExpectedIdentity` optionally binds a previously selected process to this
request, preventing an already reused PID from being accepted as a new target.
Checks reduce PID-reuse races; they do not make the live heap an atomic snapshot.

`CheckTarget` optionally verifies caller-specific membership before detection,
before provider dispatch and before returning or saving the result. Each check
has a cooperative 1 s budget, constrained by the parent context. Identity
validation always runs even when no callback is provided.

`Save` optionally persists the bounded result synchronously:

```go
result, err := collector.Run(ctx, pid, collector.Options{
    Save: func(ctx context.Context, result *collector.Result) error {
        return saveSnapshot(ctx, result) // Caller-owned storage adapter.
    },
})
```

Detection and provider failures produce a snapshot with `StatusFailed`;
unsupported runtimes produce `StatusUnavailable`. Invalid options, invalid
targets and parent cancellation return an error without calling `Save`. A
persistence error returns both the collected result and the callback error.
Callers must inspect snapshot status even when `err` is nil.

The before-OOM event retains pressure monitoring, victim selection, cooldown,
cgroup membership validation and its existing storage envelope. Its adapter
passes `ExpectedIdentity` and `CheckTarget`, then revalidates the container path
and adds container/pressure metadata inside `Save`. Providers and shared tracing
storage are unchanged.
