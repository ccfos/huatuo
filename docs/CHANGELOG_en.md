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

- Added `tcpshark --dropwatch-correlation local` to correlate TCP
  retransmissions with an embedded dropwatch source using one shared filter.
- Added `EventTracing.TCPRetransmit.EnableDropwatchCorrelation` for the
  huatuo-bamai tcpshark child.
- Added stable `correlation_reasons` for local no-match results.

### Changed

- Replaced the daemon-wide tuple cache with strict local matching across
  namespace, direction, sequence or ACK evidence, and monotonic ordering.
- No-match results now report `unknown` with the embedded dropwatch perf
  frontier, loss counters, and explicit coverage reasons. A no-match cannot
  exclude software evidence from before the embedded source became ready.
- `EventTracing.TCPRetransmit.Filter` now controls retransmission collection in
  both modes. Local mode applies it to both inputs, defaults an empty filter to
  `tcp`, and rejects Ethernet-address filters that cannot run equivalently on
  synthetic L3 data.
- Shutdown now finalizes pending local results inside tcpshark before the child
  exits. Embedded drops remain private; standalone dropwatch raw output is
  unchanged.
