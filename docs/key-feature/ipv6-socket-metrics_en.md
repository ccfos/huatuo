---
title: IPv6 Socket Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

The `sockstat` collector reads `/proc/<init-pid>/net/sockstat6` alongside
`sockstat` for the host and each container. This exposes IPv6 socket growth
that is invisible in the IPv4 per-protocol `inuse` gauges.

| Metric suffix | Meaning |
| --- | --- |
| `TCP6_inuse` | IPv6 TCP sockets in use |
| `UDP6_inuse` | IPv6 UDP sockets in use |
| `UDPLITE6_inuse` | IPv6 UDP-Lite sockets in use |
| `RAW6_inuse` | IPv6 raw sockets in use |
| `FRAG6_inuse` | IPv6 fragment queues in use |
| `FRAG6_memory` | Memory used by IPv6 fragment queues, bytes |

Host metric names start with `huatuo_bamai_sockstat_`; container metrics
start with `huatuo_bamai_sockstat_container_` and retain the existing container
labels. All are gauges. Only fields actually reported by the kernel are emitted.

For example, chart IPv6 TCP socket growth for a container:

```promql
huatuo_bamai_sockstat_container_TCP6_inuse{container_host="checkout-api"}
```

The collector is enabled unless `sockstat` is in `BlackList`. Kernels without
`sockstat6` retain IPv4 metrics. Each namespace adds one small file read per
scrape. IPv6 protocols do not expose separate TCP/UDP memory-page counters;
existing host `TCP_mem_bytes` and `UDP_mem_bytes` continue to represent the
kernel-wide accounting. See the [kernel socket statistics implementation](https://github.com/torvalds/linux/blob/master/net/ipv6/proc.c).
