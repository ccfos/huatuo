# Native profiler process-tree tracking

Native CPU and memory profiling can follow processes and threads created by a
target after profiling starts:

```bash
profiler --type cpu --language c --pid 1234 --duration 60 \
  --follow-forks --fork-max-procs 4096 --fork-rate 1000 --fork-burst 2000
```

`--follow-forks` is opt-in and requires a PID target. It applies to native CPU,
virtual allocation, physical allocation, and physical usage profiling. The
profiler remains active for the configured duration when the original process
exits, so surviving children continue to contribute samples.

Tracking is event-driven. The eBPF programs subscribe to
`sched_process_fork`, `sched_process_exec`, and `sched_process_exit`; they do
not repeatedly scan `/proc`. Threads that already belong to the root process
when profiling starts are recognized through their thread-group ID. Other
descendants that existed before attachment are not backfilled. A non-leader
thread that calls `exec` is migrated to its new leader PID without losing its
process-tree lineage.

## Protection and coverage

- `--fork-max-procs` is a hard bound on simultaneously tracked descendant
  PIDs/TIDs. The root does not consume an entry. Default: `4096`; maximum:
  `65536`.
- `--fork-rate` is the number of descendant creation events allowed in a shared
  one-second time bucket. Default: `1000`. Set it to `0` to disable this rate
  check.
- `--fork-burst` adds capacity to each time bucket. Default: `2000`.

The PID map uses non-preallocated storage, and its CollectionSpec capacity is
rewritten at load time: one entry while tracking is disabled, or exactly
`--fork-max-procs` while enabled. Exits delete their entries immediately.
Parent exit never deletes a surviving child's independent entry.

The profiler logs a final summary containing active, accepted, exited,
duplicate, map-update-failure, capacity-rejected, rate-rejected,
exec-migration, and
deepest-generation counters.
A warning with `health=limited` means protection rejected an event or a map update failed,
so the resulting profile may have incomplete process-tree coverage.
