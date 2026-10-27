# Network root-cause patterns

## dropwatch
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Softirq overload | softirq CPU share > 30% | Excessive small-packet traffic |
| NIC queue overflow | Queue-full counters increase | Hardware-level congestion |
| Physical-link fault | Error-packet counters increase | Link or NIC hardware failure |
| Container PPS throttling | PPS exceeds the configured limit | Platform packet-rate limit |
| Virtual-bridge queue overflow | Bridge queue-full counters increase | Kernel bridge congestion |
| qdisc drops | netdev_qdisc_drops_total increases | Drops imposed by traffic-control queueing rules |

## net_rx_latency
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Latency spikes | latency_ms exceeds threshold | Slow userspace reads or CPU contention |
| Persistent high latency | Sustained elevated latency | NIC/RSS configuration or slow kernel network-stack processing |

## netdev_txqueue_timeout
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Driver stall | One queue times out | NIC driver bug or hang |
| All queues time out | Timeouts across all queues | Hardware failure or firmware bug |
