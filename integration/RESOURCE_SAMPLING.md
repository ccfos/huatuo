# Process resource sampling

`lib_resources.sh` measures a live process during an integration-test window.
It builds a small Go helper using the repository's vendored `prometheus/procfs`.
Sourcing the library does not build anything, install traps, or change shell
options. Build once before starting a short-lived target.

The caller owns the target process, feature readiness checks, workload,
resource budgets, and EXIT trap. The library owns only its sampler. Use the
same absolute output path for start, stop, and cleanup; it must be a new
directory below `HUATUO_BAMAI_TEST_TMPDIR`.

| Function | Responsibility |
| --- | --- |
| `resource_build` | Build into the test workspace using normal Go caches. |
| `resource_start PID DIRECTORY` | Start sampling; wait for the first valid sample. |
| `resource_stop DIRECTORY` | Request a final sample, wait, and check sampler exit status. |
| `resource_cleanup DIRECTORY` | Abort and reap an owned sampler; safe to repeat. |

For a complete example, see `test_dropwatch_ratelimit.sh`: it retains the
10-second flood and observes a 5-second subwindow before the CLI exits.
Compilation and feature setup are outside the measured window. The feature
script prints the result but does not invent resource limits.

## Result and failure behavior

Each capture directory holds `ready.json`, `result.json`, and `sampler.log`
when available. A failed start may have no readiness file; SIGKILL or an output
failure may prevent result publication. A missing result is always a failure.
JSON is written atomically only at readiness and completion, not every sample.
Keep results and logs when a test fails. Cleanup does not delete them, terminate
the target, or remove the helper binary.

`result.json` uses `format_version: 2`. Normal completion sets `complete: true`;
abort, timeout, read failure, or target exit sets it to false. Unknown resources
are null, not zero. The sampler exits successfully only after normal completion,
usable sampling quality, and successful result publication. Quality failure is
a measurement failure, not proof that a feature exceeded its budget.

| Field | Meaning |
| --- | --- |
| `cpu_seconds` | Difference in process user + system CPU time. |
| `cpu_percent.mean` | CPU seconds / actual CPU window seconds × 100. One core is 100%; multiple cores can exceed it. |
| `cpu_percent.peak` | Highest sampled interval average with a window of at least 100 ms; not an instantaneous peak. |
| `rss_bytes`, `fds` | Before, after, delta, and maximum observed value, including the final sample. |
| `io.*.delta`, `io.*.per_second` | Counter difference and difference / actual I/O window seconds. |
| `process_start_time_ticks` | Kernel process identity paired with PID; not a timestamp to display as wall time. |
| `cpu_window_seconds`, `io_window_seconds` | Actual monotonic first-to-last reading intervals for each counter group. |
| `cpu_peak_window_seconds`, `cpu_peak_tail_ms` | Winning CPU peak window and final short tail excluded from peak calculations. |
| `skipped_samples`, `max_sampling_delay_ms` | Missed scheduled slots and largest delay before starting a scheduled read. |
| `max_sample_gap_ms`, `max_sample_span_ms` | Largest actual sample interval and longest multi-file reading duration. |

Physical storage accounting uses `read_bytes`/`write_bytes`; logical syscall
traffic uses `rchar`/`wchar`, including cached and non-storage I/O. A busy writer
to `/dev/null` can have substantial `wchar` and zero `write_bytes`.

## Scope and limitations

- Sampling is every 100 ms, scheduled against Go's monotonic clock. Missed slots
  are counted instead of sampled in a catch-up burst. The default 30-second
  deadline is a failure guard, not an automatic successful finish.
- CPU includes the target's threads, not recursively collected children. Linux
  process I/O accounting can include waited-for children;
  `io_includes_waited_children: true` makes that scope explicit.
- RSS is the kernel's approximate process RSS from `/proc/PID/stat`, not the Go
  heap, exact physical allocation, or BPF map/program memory. Peaks between
  samples can be missed. FD reads and counters are not one atomic snapshot.
- At least one periodic sample and one CPU peak window are required. Skipped
  slots, a gap over 200 ms, or a sample read over 100 ms make peaks unusable.
  A short final interval still contributes to CPU totals and the window mean.
- The target must remain alive until `resource_stop`. This existing-PID API
  does not measure startup, process exit, goroutines, or feature-exclusive cost.
  Keep goroutine endpoint checks and profiler investigations separate.

`test_basic_resources.sh` verifies public lifecycle and failure paths without
BPF. The helper's ordinary Go tests cover calculations; integration-tagged
tests compare live reads with independent `/proc`, `getconf`, and `ps` reads.
These comparisons use matching counter boundaries rather than unrelated
`top` or `pidstat` averaging windows. The benchmark measures reading overhead
only; it does not establish zero disturbance to a feature workload.
