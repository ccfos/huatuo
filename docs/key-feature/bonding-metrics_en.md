---
title: Bonding Redundancy Metrics
type: docs
author: HUATUO Team
date: 2026-09-21
weight: 7
---

A bond can remain operational after losing a member link. The `bonding`
collector makes that loss of redundancy visible by reading the bonding
sysfs interface on the host.

| Metric | Type | Additional labels |
| --- | --- | --- |
| `huatuo_bamai_bonding_slaves` | gauge | `master` |
| `huatuo_bamai_bonding_slaves_up` | gauge | `master` |
| `huatuo_bamai_bonding_slave_up` | gauge (0 or 1) | `master`, `slave` |
| `huatuo_bamai_bonding_slave_link_failures_total` | counter | `master`, `slave` |

All metrics also carry `host` and `region`. A useful degradation condition is:

```promql
huatuo_bamai_bonding_slaves_up < huatuo_bamai_bonding_slaves
```

Combine it with a suitable alert duration to avoid alerting on brief failover.
Use `increase(huatuo_bamai_bonding_slave_link_failures_total[15m])` to locate
links that flap. Counters may reset when a slave is reattached.

The collector is enabled unless `bonding` is in `BlackList`. It rediscovers
bonds and membership each scrape. Hosts without bonds emit no series. Missing
slave state during reconfiguration withholds the aggregate `slaves_up` rather
than treating an unknown link as down. Other healthy bonds remain observable.

Link state reflects the bonding driver's monitoring, including backup links;
it does not establish LACP forwarding eligibility. Configure appropriate link
monitoring as described in the [Linux bonding guide](https://docs.kernel.org/networking/bonding.html).
Collection requires one membership read and two reads per member per bond.
