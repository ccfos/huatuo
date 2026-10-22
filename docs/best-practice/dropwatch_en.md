---
title: Packet Drops
type: docs
description: ""
author: HUATUO Team
date: 2026-06-05
weight: 4
---

## Overview

dropwatch is HUATUO's packet-drop observation tool. It collects software drops through `tracepoint/skb/kfree_skb` and hardware drops from devlink-capable drivers through `raw_tracepoint/devlink_trap_report`. Output includes protocol types, IP five-tuples, network devices, drop reasons, and kernel stacks.

dropwatch uses the built-in `internal/pcapfilter` to compile tcpdump-style expressions. Packets are filtered in the kernel, and only matching events are reported to user space.

It also supports device allowlists and denylists, a global reporting rate limit, and integration with huatuo-bamai to store drop events in Elasticsearch for long-term analysis.

## Use Cases

- Filter drops by protocol, address, port, or device to investigate connection problems.
- Use stacks and drop reasons to analyze changes in network latency or throughput.
- Store records in HUATUO and correlate them with resource metrics and application anomalies by time.

Drop records provide observation locations and context, but cannot establish an application's fault cause alone. Hardware visibility depends on the kernel, driver, and devlink trap configuration; see "Hardware Drops."

## Filter Expressions

Filters support a subset of tcpdump syntax and compile to eBPF bytecode at load time. Only matching packets reach user space.

### Supported Expressions

The following filter primitives are supported:

**Protocols**

```text
ip   ip6   tcp   udp   icmp   icmp6   igmp   pim   esp   ah   vrrp   arp   rarp
ip proto tcp      ip6 proto udp        (protocol names only; numeric protocol IDs are unsupported)
```

**Host addresses**

```text
host 10.0.0.1
src host 10.0.0.1
dst host 10.0.0.1
```

**Ports**

```text
port 80
src port 443
dst port 8080
```

**Networks (CIDR)**

```text
net 10.0.0.0/8
src net 192.168.1.0/24
dst net 172.16.0.0/12
```

**Multicast and Ethernet addresses**

```text
ip multicast    ip6 multicast    multicast    ether multicast
ether host 00:11:22:33:44:55
```

**Boolean operators and grouping**

```text
tcp and port 80
tcp or udp
not arp
tcp and (port 80 or port 443)
ip and src net 192.168.1.0/24 and tcp dst port 3306
```

### Unsupported Expressions

The following expressions are **unsupported** and cause compilation failures or incorrect matches:

| Expression | Reason |
| --------------------------------------------------- | ----------------------------------------------------------- |
| `tcp[tcpflags] & tcp-syn != 0`, `ip[8]`, `tcp[0:4]` | Byte-offset expressions (`proto[offset:size]`) are not implemented |
| `ip proto 6`, `ip6 proto 17` | Numeric protocol IDs are unsupported; use names such as `ip proto tcp` |
| `ether proto 0x0800` | Hexadecimal EtherTypes are unsupported; use names such as `ether proto ip` |
| `sctp` | Unrecognized keyword |
| `portrange 80-90`, `tcp portrange 1-100` | Port ranges are unsupported |
| `less N`, `greater N` | Packet-length filtering is unsupported |
| `ip broadcast`, `ether broadcast` | Broadcast matching is unsupported |
| `vlan`, `mpls`, `pppoes` | Tunnel/encapsulation keywords are unsupported |
| `gateway` | Unsupported |

### Recommended Examples

```bash
# Observe all TCP drops (default; reliable in both L2 and L3 contexts)
--filter "tcp"

# TCP and UDP
--filter "tcp or udp"

# A specific destination host (both TCP and UDP)
--filter "dst host 10.0.0.1"

# A specific port
--filter "tcp and port 443"

# Exclude a noisy host
--filter "tcp and not host 169.254.169.254"

# A specific subnet and port
--filter "src net 192.168.1.0/24 and tcp dst port 3306"

# Observe non-TCP drops (UDP and ICMP only; "not tcp" also captures unknown L3 events)
--filter "udp or icmp"

# Observe ARP drops only (L2 contexts only; never matches in L3)
--filter "arp"
```

> **`--filter "ip"` / `--filter "ip6"` matches the corresponding IP family**, using EtherType in L2 and the version nibble in L3. For a specific transport protocol or host, prefer a narrower `tcp`, `udp`, `host`, or `ip proto <name>` expression.

## Run dropwatch

```bash
dropwatch [flags]
```

| Flag | Default | Description |
| ----------------------------- | ------ | ------------------------------------------------------------ |
| `--bpf-path <path>` | Required | Path to the `dropwatch` eBPF object |
| `--filter <expr>` | None | tcpdump-style filter expression |
| `--device <names>` | None | Device allowlist: collect drops only on these comma-separated devices, such as `eth0,eth1` |
| `--device-excluded <names>` | None | Device denylist; mutually exclusive with `--device` |
| `--duration <n>` | 0 | Exit after N seconds; 0 runs until Ctrl-C |
| `--output <json\|text>` | `text` | Output format; ignored when `--output-storage` is set |
| `--output-storage <path>` | None | Send events to huatuo-bamai through a Unix socket |
| `--task-id <id>` | None | Task ID associated with this session, usually used with `--output-storage` |
| `--max-events-per-second <n>` | 0 | Global reporting rate limit; 0 is unlimited. Applied after `--device` / `--filter` |

`--filter` and device filters are independent and both apply when specified (AND semantics). Without `--device` or `--device-excluded`, all devices are collected. These two flags are mutually exclusive. Allowlists reject SKBs without a `net_device`; denylists allow them.

At startup, dropwatch detects `devlink:devlink_trap_report`. If available, it loads both software and hardware drop probes; otherwise it logs a warning and loads only the software probe. Hardware collection also requires the NIC driver to register devlink drop traps and the target trap action to be `trap`. With action `drop`, hardware does not provide a packet copy to the CPU, so dropwatch cannot capture it.

### Hardware Drops

No additional dropwatch flags are needed. First confirm that the kernel, driver, and target trap meet the requirements:

```bash
# 1. Confirm that the kernel provides the devlink trap tracepoint
test -e /sys/kernel/tracing/events/devlink/devlink_trap_report/id || \
  test -e /sys/kernel/debug/tracing/events/devlink/devlink_trap_report/id

# 2. List devlink devices and driver-registered traps
sudo devlink dev show
sudo devlink trap show <bus/device>

# 3. Enable packet reporting for the target DROP trap
sudo devlink trap set <bus/device> trap <trap-name> action trap

# 4. Start dropwatch and show hardware drops only
sudo dropwatch --bpf-path bpf/net_dropwatch.o --output json 2>/dev/null | \
  jq -c 'select(.drop_source == "hardware")'
```

Use a device identifier returned by `devlink dev show` for `<bus/device>`, such as `pci/0000:03:00.0`. Restore the trap's previous action after diagnosis.

This capability collects only packets reported by drivers as `DEVLINK_TRAP_TYPE_DROP`. It does not capture all hardware packets and is not equivalent to NIC hardware drop counters. Drops such as full hardware queues are visible only if the driver implements and reports them as devlink drop traps. Traps of type `exception` or `control` are not reported as drop events.

`--filter`, `--device`, `--device-excluded`, and `--max-events-per-second` apply to both software and hardware events. Text output represents hardware reasons as `reason=<group>/<trap> drop_source=hardware`; JSON uses separate `drop_reason_group`, `drop_reason`, and `drop_source` fields.

`tcpshark --with-dropwatch` reuses the same source classification and reason resolution and automatically enables available hardware collection. Successful correlation preserves `drop_source`, `drop_reason`, and `drop_reason_group`. See [Retransmission Tracing](/docs/best-practice/tcpshark_en.md).

### Common Commands

```bash
# Text output for TCP drops on all devices
sudo dropwatch --bpf-path bpf/net_dropwatch.o --filter "tcp"

# Observe drops on eth0 only
sudo dropwatch --bpf-path bpf/net_dropwatch.o --device eth0 --output json

# Exclude loopback
sudo dropwatch --bpf-path bpf/net_dropwatch.o --device-excluded lo --output json

# Combine device and protocol filters
sudo dropwatch --bpf-path bpf/net_dropwatch.o --device eth0 --filter "tcp and port 443" --output json

# Collect for 60 seconds, then exit
sudo dropwatch --bpf-path bpf/net_dropwatch.o --filter "tcp and port 443" --duration 60 --output json

# Forward events to a running huatuo-bamai instance
sudo dropwatch --bpf-path bpf/net_dropwatch.o --filter "tcp" --output-storage /var/run/huatuo-toolstream.sock

# Collect JSON for 10 seconds, excluding stacks containing ip_finish_output
sudo dropwatch --output json --duration 10 --bpf-path bpf/net_dropwatch.o | jq -c 'select(.stack | test("ip_finish_output") | not)'

# Collect JSON for 10 seconds and print all fields except stack
sudo dropwatch --output json --duration 10 --bpf-path bpf/net_dropwatch.o | jq -c 'del(.stack)'
```

`jq -c` writes each matching event as one JSON line for NDJSON storage or further piping. `test("ip_finish_output")` matches that regular expression against `stack`, and `not` negates the result. The command therefore excludes stacks containing `ip_finish_output`. Remove `| not` to retain only those events.

`del(.stack)` removes `stack` only from jq output, letting you inspect time, device, process, `packet_*` metadata, and `layers` fields. To filter stacks in user space before storage, configure `EventTracing.IssuesList` in huatuo-bamai (see "huatuo-bamai Integration").

## Event Fields

Each drop event is an NDJSON object (`types.DropWatchTracing`).

| Field | Type | Description |
| ------------------------ | -------- | --------------------------------------------- |
| `observed_timestamp` | string | UTC time generated when user space receives/formats the event (RFC3339Nano), not kernel hook time |
| `kernel_observed_timestamp` | string | Kernel observation time in UTC (RFC3339Nano), converted from raw monotonic time |
| `type` | string | Reserved TCP event type, currently unset (`1` ordinary drop, `2` SYN flood, `3`/`4` listen overflow) |
| `drop_source` | string | `software` for the kernel protocol stack; `hardware` for a devlink DROP trap |
| `drop_reason` | string | Software: `SKB_DROP_REASON_*`; if kernel BTF resolution fails, log a warning and fall back to a number. Hardware: devlink trap name |
| `drop_reason_group` | string | Devlink trap group for categorizing hardware drops; omitted for software drops |
| `drop_location` | string | Hexadecimal `kfree_skb` call address for software drops; omitted for hardware drops |
| `source` | string | `tools` when dropwatch runs independently; `events` when started by huatuo-bamai |
| `comm` | string | Process name at the time of the drop |
| `pid` | uint64 | Process TGID |
| `container_id` | string | Container ID resolved by huatuo-bamai (omitempty) |
| `memory_cgroup_css_addr` | string | Memory cgroup CSS address for container attribution |
| `net_namespace_cookie` | uint64 | Network namespace cookie for container attribution |
| `net_namespace_inum` | uint32 | Network namespace inum for container attribution |
| `netdev_name` | string | Network device name, such as `eth0` |
| `netdev_ifindex` | uint32 | Network interface index |
| `netdev_queue_mapping` | uint32 | TX queue mapping |
| `netdev_linkstatus` | []string | Network device link flags |
| `packet_skb_addr` | string | Hexadecimal SKB address (omitempty) |
| `packet_eth_proto` | string | Raw EtherType in hexadecimal, such as `0x0800` |
| `packet_len_bytes` | uint32 | Snapshot of kernel `skb->len`; the logical SKB length may differ from the on-wire frame length |
| `layers` | object | Decoded protocol layers; unavailable layers are omitted |
| `stack` | string | Kernel stack, separated by newlines |

For hardware events, `stack` shows where the driver called the devlink trap reporting interface, not the actual discard location inside the ASIC. Investigate hardware causes using `drop_reason_group`, `drop_reason`, device details, and driver documentation.

`layers` represents the protocol stack with fixed fields rather than a separate protocol enum:

| Field | Description |
| -------------- | ------------------------------------------------------------ |
| `layers.label` | Protocol combination, such as `IPv4/TCP`, `IPv6/UDP`, `ARP`, or `unknown` |
| `layers.ether` | L2 fields when a real Ethernet header is present: `saddr`, `daddr`, `type`, `len`; `len` is nonzero only for IEEE 802.3 framing |
| `layers.ipv4` | IPv4 fields: `version`, `ihl`, `tos`, `len`, `id`, `flags`, `frag_offset`, `ttl`, `protocol`, `checksum`, `saddr`, `daddr` |
| `layers.ipv6` | IPv6 fields: `version`, `traffic_class`, `flow_label`, `len`, `next_header`, `hop_limit`, `saddr`, `daddr` |
| `layers.tcp` | TCP fields: `sport`, `dport`, `seq`, `ack_seq`, `data_offset`, `window`, `checksum`, `urgent`, `sk_state` |
| `layers.udp` | UDP fields: `sport`, `dport`, `len`, `checksum` |
| `layers.icmp` | ICMP/ICMPv6 fields: `type`, `code`, `checksum`, `id`, `seq`; ICMPv6 Echo requests/replies include the identifier and sequence, while other ICMPv6 messages omit them |
| `layers.arp` | ARP fields: `addr_type`, `protocol`, `hw_address_size`, `prot_address_size`, `operation`, `sender_mac`, `sender_ip`, `target_mac`, `target_ip` |

ICMPv6 Echo Request/Reply (types 128/129) populates `id` and `seq` only when all four Echo bytes are captured. If the Echo fields are truncated, common ICMPv6 fields remain and ID/Seq are zero. If the common header is also truncated, the decoded IPv6 layer remains but `layers.icmp` is absent. Other ICMPv6 message types do not populate Echo fields.

JSON omits zero-valued `id` and `seq` independently. Text omits them only when both are zero. Missing output fields therefore cannot distinguish absent or truncated Echo fields from valid zero values.

## huatuo-bamai Integration

huatuo-bamai starts `dropwatch` as a child process. `--output-storage` sends events to its built-in processing pipeline for eventual Elasticsearch storage. Typical arguments:

```bash
dropwatch \
  --bpf-path <CoreBpfDir>/net_dropwatch.o \
  --output-storage /var/run/huatuo-toolstream.sock \
  --filter "tcp"
```

### Configuration

```toml
[EventTracing]
    # Optional stack filtering. dropwatch discards events whose stacks match configured regexes.
    # Default: []
    IssuesList = []

[EventTracing.Dropwatch]
    # tcpdump filter for standalone dropwatch.
    # Default: "tcp"
    Filter = "tcp"

    # Forwarded to dropwatch --max-events-per-second.
    # Default: 100
    MaxEventsPerSecond = 100

[EventTracing.TCPRetransmit]
    # Use tcpshark's private embedded dropwatch source.
    EnableDropwatch = false
```

Standalone dropwatch always outputs raw `DropWatchTracing`. Local TCP retransmission correlation loads a separate `net_dropwatch.o`; both inputs use `EventTracing.TCPRetransmit.Filter` and output only finalized `TCPRetransmitTracing` results. Both modes can run concurrently. Embedded drops are not also saved as raw events.

### Noise Filtering

Stack noise filtering is disabled by default. huatuo-bamai discards matching events only after `EventTracing.IssuesList` is configured. The following patterns are candidates for operator configuration; verify them against the local kernel and workload before enabling them:

| Pattern | Stack Frame Prefix | Reason |
| ------------------------------------- | ---------------------------------- | ------------------------------------------------------------ |
| ARP/neighbor entry expiry | `neigh_invalidate/` | Cleanup of expired neighbor entries without affecting active flows. Remove the corresponding `EventTracing.IssuesList` rule to disable filtering. |
| bnxt NIC TX completion | `bnxt_tx_int/` or `__bnxt_tx_int/` | The Broadcom bnxt driver calls `kfree_skb` after DMA transmission completes. This is normal cleanup, not a packet drop. |
