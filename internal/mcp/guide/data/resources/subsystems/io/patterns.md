# I/O root-cause patterns

## iotracing
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Excessive log writes | One process continuously saturates write bandwidth | Verbose logging or failed rotation |
| Bulk import | A burst of high-bandwidth writes | Data import or synchronization job |
| Random-I/O overload | High utilization, modest bandwidth, very high latency | Large numbers of random reads |
| Swap storm | Heavy reads/writes to swap | Memory shortage causes frequent swapping |
| Disk failure | Persistently extreme, irregular latency | Hardware failure |
