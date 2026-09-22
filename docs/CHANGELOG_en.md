---
title: Change Log
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 50
---

## Unreleased

### Added

- Added `tcpshark --with-dropwatch --bpf-path-dir <dir>` to correlate TCP
  retransmissions with an embedded dropwatch source using one shared filter.
- Added `EventTracing.TCPRetransmit.EnableDropwatchCorrelation` for the
  huatuo-bamai tcpshark child.
- Added a single `correlation_reason` for every finalized local correlation,
  including `warmup` for expired waits whose retransmission predates source readiness,
  and an independent namespace-match diagnostic.

### Changed

- Replaced the daemon-wide tuple cache with strict local matching across
  namespace, direction, sequence or ACK evidence, and monotonic ordering.
- No-match results now report `unknown` with cumulative embedded-dropwatch
  loss counters, map-counter availability, and one terminal reason. Retransmissions wait up to 100 ms, while
  candidate drops have a one-second causal age limit.
- `EventTracing.TCPRetransmit.Filter` now controls retransmission collection in
  both modes. Local mode applies it to both inputs, defaults an empty filter to
  `tcp`, and rejects Ethernet-address filters that cannot run equivalently on
  synthetic L3 data.
- Shutdown now finalizes pending local results inside tcpshark before the child
  exits. Embedded drops remain private; standalone dropwatch raw output is
  unchanged.
