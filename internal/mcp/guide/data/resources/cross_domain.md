# Cross-subsystem correlations

Investigation leads for cross-subsystem analysis. Co-occurring events do not
establish causation. Validate targets, timelines, metrics, and call stacks;
never infer a root cause from a combination of tracer names alone.

## CPU ↔ Memory
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| cpusys + memburst | Reclaim may increase system CPU time | Check reclaim metrics and kernel stacks |
| cpuidle + memburst | Container memory pressure may increase CPU overhead | Check memory limits, reclaim, and CPU timelines |

## CPU ↔ I/O
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| cpusys + iotracing | I/O paths may increase kernel CPU overhead | Check I/O latency and CPU hotspots |
| dload + iotracing | I/O blocking may put tasks into D state | Correlate blocked stacks, devices, and tasks |

## CPU ↔ Network
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| cpusys + dropwatch | Network processing pressure may increase system CPU time and drops | Check softirq metrics, CPU hotspots, and drop reasons |
| dropwatch + net_rx_latency | Drops and receive latency may share a bottleneck | Check interfaces, traffic, and timelines |

## Memory ↔ I/O
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| memburst + iotracing | Memory pressure may affect I/O through swapping or writeback | Check swap, writeback, and I/O metrics |

## CPU ↔ Memory ↔ I/O
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| dload + memburst | Direct reclaim may block tasks | Check reclaim paths and D-state task stacks |
| hungtask + dload | Persistent D state may trigger hung-task detection | Check blocking duration and stacks for the same task |

## Network ↔ Hardware
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| netdev_txqueue_timeout + netdev_events | Link changes may accompany transmit timeouts | Check NIC, driver, and link timelines |
| lacp + netdev_events | A bond slave may have changed link state | Check bond state and member interfaces |
| netdev_events + dropwatch | Link flapping may cause drops | Check interfaces and drop reasons |

## CPU ↔ Memory (OOM)
| Correlation | Possible mechanism | Verification |
|-------------|--------------------|--------------|
| oom + memory_reclaim | Some OOM scenarios involve direct reclaim | Check OOM type, limits, and reclaim evidence; missing reclaim events do not rule out OOM |
| memburst + oom | Memory growth may lead to OOM | Check the killed process, memory limits, and timeline |
