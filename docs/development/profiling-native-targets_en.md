# Native profiling target selectors

The Profiles API accepts the following optional native profiling selectors:

| Field | Meaning |
| --- | --- |
| `container_id` | Select a container through its resolved cgroup metadata |
| `pid` | Select one process or thread ID |
| `thread_group` | Treat `pid` as a thread-group member and profile its TGID |
| `cpu_ids` | Restrict native CPU profiling to the listed CPU IDs |

`pid` and `container_id` are mutually exclusive. Native memory profiling
requires exactly one of them. Native CPU profiling may omit both for
host-wide collection.

Java and Python profiling also require exactly one `pid` or `container_id`.
CPU selection and thread-group expansion remain native-only options.

`thread_group` requires `pid`. The Agent reads `/proc/<pid>/status` and uses
its `Tgid` value, so callers may provide a non-leader thread ID. There is no
separate process-group selector.

CPU IDs must be unique and non-negative. The API sorts them for a stable
request, while the Agent validates them against the target host's CPU count.

The control plane forwards `container_id` to the profiler. The profiler
resolves the container's cgroup subsystem metadata; callers do not provide a
cgroup filesystem path.
