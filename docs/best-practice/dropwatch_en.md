---
title: Network Drop Monitoring
type: docs
description: ""
author: HUATUO Team
date: 2026-06-05
weight: 4
---

{{% alert color="info" title="About HUATUO" %}}
<div style="text-align: left;">
HUATUO is an OS-level deep observability project open-sourced by DiDi and incubated under CCF (China Computer Federation). It provides kernel-level deep observability for cloud-native general computing, AI computing, cloud services, and infrastructure services.
</div>
{{% /alert %}}

## Overview

dropwatch is a kernel network drop observability tool provided by HUATUO. It attaches to the kernel tracepoint `tracepoint/skb/kfree_skb` to capture network drop events in real time, and outputs the full drop context: protocol type, IP five-tuple, process name, PID, network device, MAC address, and the complete kernel call stack that triggered the drop.

dropwatch supports tcpdump-style kernel-side filtering, so only matching packets are reported to user space.

In addition, dropwatch supports device whitelist/blacklist filtering, global per-second rate limiting, and integration with huatuo-bamai to store drop events in Elasticsearch for long-term analysis.

---

## Scenarios

### 1. Kubernetes Cloud-Native Network Drop Diagnosis

In scenarios such as container migration, frequent Pod restarts, and Service port conflicts, dropwatch captures `kfree_skb` events in real time and correlates them with specific containers to quickly identify the root cause of packet drops. Combined with `--filter "tcp and port <service-port>"` to filter specific business traffic, the mean time to root cause is reduced from hours to minutes.

### 2. Network Performance Spike Analysis

For intermittent latency spikes or throughput drops, use the kernel call stack in each event to identify the drop site (e.g. `tcp_v4_rcv`, `ip_output`) and distinguish firewall drops, routing failures, buffer overflows, and other causes.

### 3. Multi-Tenant Network Isolation Troubleshooting

In container environments that share network namespaces or veth devices, combine `--device` and `--filter` to collect drops only for the target container and exclude other tenants' traffic.

### 4. Observability Platform Integration

Use `--output-storage` to send drop events to huatuo-bamai for Elasticsearch storage and correlation with metrics and logs. Align the events with application error and latency timelines in Grafana to determine whether kernel drops coincide with application anomalies.

---

## Usage

### 1. Filter Expressions

At load time, `internal/pcapfilter` uses the pure-Go `huatuo-ai/go-pcap` fork to compile tcpdump-style expressions into eBPF bytecode.

#### 1.1 Supported Expressions

The `huatuo-ai/go-pcap` compiler supports the following tcpdump-style expression families.

**Protocols**

```text
ip   ip6   tcp   udp   sctp   icmp   icmp6   igmp
pim  esp   ah    vrrp  arp    rarp   stp
ip proto tcp
ip6 proto udp
```

**Hosts and networks**

```text
host 10.0.0.1
host 2001:db8::1
host api.example.com
src host 10.0.0.1
dst host 2001:db8::1
src net 192.168.1.0/24
dst net 2001:db8::/32
```

Host names are resolved once when the filter is compiled. All returned A and AAAA addresses participate in the match.

**Ports, ranges, and directions**

```text
port 80
src port 443
dst portrange 8000-8080
src or dst port 53
src and dst portrange 1000-2000
```

Ports and port ranges match TCP, UDP, and SCTP unless a transport protocol is specified.

**Multicast and Ethernet**

```text
ip multicast    ip6 multicast    multicast    ether multicast
ether host 00:11:22:33:44:55
```

**VLAN, QinQ, and MPLS**

```text
vlan and tcp port 443
vlan 100 and tcp port 443
vlan 100 and vlan 200 and ip
mpls 100 and ip
mpls 100 and mpls 200 and ip6
```

Each repeated `vlan` or `mpls` clause advances the packet cursor by one encapsulation layer.

**Packet arithmetic, TCP flags, and length**

```text
ether[12:2] == 0x0800
ip[0] & 0x0f > 5
ip[12:4] == 0x0a000001
tcp[tcpflags] == tcp-syn
tcp[tcpflags] & (tcp-syn|tcp-ack) == (tcp-syn|tcp-ack)
len >= 128
```

Packet access is available through `ether`, `ip`, `ip6`, `tcp`, and `udp`. Access widths can be 1, 2, or 4 bytes. Comparisons support `=`, `==`, `!=`, `>`, `<`, `>=`, and `<=`; arithmetic supports `+`, `-`, `*`, `/`, `%`, `&`, `|`, `^`, `<<`, and `>>`. Every access includes a packet-length guard, so a truncated packet rejects instead of reading beyond its data.

**Boolean operators and grouping**

```text
tcp and (port 80 or port 443)
tcp || udp
! (arp or rarp)
```

`and`/`&&`, `or`/`||`, and `not`/`!` are equivalent pairs. AND binds tighter than OR; use parentheses when grouping is not obvious.

#### 1.2 Unsupported Expressions

The following tcpdump features are not supported and fail compilation:

| Expression | Use or limitation |
| --- | --- |
| `ip proto 6`, `ip6 proto 17` | Numeric protocol values are unsupported; use names such as `ip proto tcp` |
| `ip protochain tcp` | `protochain` and IPv6 extension-header traversal are unsupported |
| `less 128`, `greater 128` | Use `len < 128` or `len > 128` |
| `ip broadcast`, `ether broadcast` | Broadcast predicates require interface netmask data that is unavailable |
| `inbound`, `outbound`, `ifindex 2` | Capture-direction and interface metadata predicates are unavailable |
| `gateway`, `fddi`, `wlan` | These link-layer qualifiers are unsupported |
| `pppoes` and other encapsulations | Only Ethernet, VLAN/QinQ, MPLS, and raw-IP layouts are implemented |

> Each expression is compiled for Ethernet (L2) and raw-IP (L3). L2-only clauses such as `arp`, `rarp`, `stp`, `ether`, `vlan`, `mpls`, and `ether[...]` do not match on the L3 path.

---

### 2. Running dropwatch

```bash
dropwatch [flags]
```

| Flag                        | Default | Description                                                    |
| --------------------------- | ------- | -------------------------------------------------------------- |
| `--bpf-path <path>`         | required | Path to the `dropwatch` eBPF object file                      |
| `--filter <expr>`           | (none)  | tcpdump-style filter expression                                |
| `--device <names>`          | (none)  | Device whitelist: only collect drops from these devices; comma-separated (e.g. `eth0,eth1`) |
| `--device-excluded <names>` | (none)  | Device blacklist: exclude drops from these devices; mutually exclusive with `--device` |
| `--duration <n>`            | 0       | Stop after N seconds (0 = run until Ctrl-C)                   |
| `--output <json\|text>`     | `text`  | Output format; ignored when `--output-storage` is set          |
| `--output-storage <path>`   | (none)  | Send events to huatuo-bamai via Unix socket                    |
| `--task-id <id>`            | (none)  | Task ID for this session; typically used with `--output-storage` |
| `--max-events-per-second <n>` | 0     | Global rate limit in events/sec (0 = unlimited); applied after `--device` / `--filter` |

`--filter` and device filtering are orthogonal; when both are specified, both apply (AND semantics). If neither `--device` nor `--device-excluded` is specified, all devices are collected. `--device` and `--device-excluded` are mutually exclusive; whitelist mode drops SKBs without a `net_device`, while blacklist mode passes them.

#### Examples

```bash
# Monitor TCP drops on eth0
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --filter "tcp" --output json

# Capture for 60 seconds and exit
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp and port 443" --duration 60 --output json

# Use jq to filter and show only RST packets
sudo dropwatch --bpf-path bpf/dropwatch.o --output json 2>/dev/null | jq 'select(.layers.tcp.flags == "RST")'

# Capture 10 seconds of JSON output, excluding events whose stack contains ip_finish_output
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'select(.stack | test("ip_finish_output") | not)'

# Capture 10 seconds of JSON output, printing all fields except stack
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'del(.stack)'
```

`jq -c` compresses each matching event into a single-line JSON, convenient for saving as NDJSON or further pipe processing. `test("ip_finish_output")` checks whether `stack` matches the regex; `not` negates the result, so the command above excludes stacks containing `ip_finish_output`. Remove `| not` to keep only those containing `ip_finish_output`. `del(.stack)` removes the `stack` field from the jq output, useful for viewing just the timestamp, device, process, `packet_*` metadata, and `layers` protocol fields. For userspace call-stack filtering before storage, configure `EventTracing.IssuesList` in huatuo-bamai (see Section 4).

---

### 3. Event Data Structure

Each drop event is represented as an NDJSON object (`types.DropWatchTracing`).

| Field                    | Type     | Description                                                   |
| ------------------------ | -------- | ------------------------------------------------------------- |
| `observed_timestamp`     | string   | UTC userspace receive/format time (RFC3339Nano), not the kernel hook timestamp |
| `type`                   | string   | Reserved TCP type; currently unset (`1` common, `2` SYN flood, `3`/`4` listen overflow) |
| `drop_reason`            | string   | Kernel `skb_drop_reason` name resolved from BTF; numeric fallback, or `NOT_SUPPORTED` when unavailable |
| `source`                 | string   | Event source; `tools` for standalone dropwatch and `events` when launched by huatuo-bamai |
| `comm`                   | string   | Process name at the time of the drop                          |
| `pid`                    | uint64   | Process TGID                                                  |
| `container_id`           | string   | Container ID (populated by huatuo-bamai resolution, omitempty) |
| `memory_cgroup_css_addr` | string   | Memory cgroup CSS address, used for container resolution       |
| `net_namespace_cookie`   | uint64   | Network namespace cookie, used for container resolution        |
| `net_namespace_inum`    | uint32   | Network namespace inum, used for container resolution          |
| `netdev_name`            | string   | Network device name (e.g. `eth0`)                             |
| `netdev_ifindex`         | uint32   | Network interface index                                       |
| `netdev_queue_mapping`   | uint32   | TX queue mapping                                              |
| `netdev_linkstatus`      | []string | Network device link status flags                              |
| `packet_skb_addr`        | string   | SKB address (hexadecimal, omitempty)                         |
| `packet_eth_proto`       | string   | Raw EtherType (hexadecimal, e.g. `0x0800`)                   |
| `packet_len`             | uint32   | Packet length in bytes                                        |
| `layers`                 | object   | Layered protocol parse result; missing layers are omitted      |
| `stack`                  | string   | Kernel call stack (newline-separated)                         |

`layers` uses fixed fields to express the protocol stack, without relying on a separate protocol enumeration:

| Field          | Description                                                                                              |
| -------------- | -------------------------------------------------------------------------------------------------------- |
| `layers.label` | Protocol combination label, e.g. `IPv4/TCP`, `IPv6/UDP`, `ARP`, `unknown`                                |
| `layers.ether` | L2 fields when a real Ethernet header is present: `saddr`, `daddr`, `type`, `len`; `len` is non-zero only for IEEE 802.3 framing |
| `layers.ipv4`  | IPv4 fields: `version`, `ihl`, `tos`, `len`, `id`, `flags`, `frag_offset`, `ttl`, `protocol`, `checksum`, `saddr`, `daddr` |
| `layers.ipv6`  | IPv6 fields: `version`, `traffic_class`, `flow_label`, `len`, `next_header`, `hop_limit`, `saddr`, `daddr`  |
| `layers.tcp`   | TCP fields: `sport`, `dport`, `seq`, `ack_seq`, `data_offset`, `flags`, `window`, `checksum`, `urgent`, `sk_state` |
| `layers.udp`   | UDP fields: `sport`, `dport`, `len`, `checksum`                                                         |
| `layers.icmp`  | ICMP/ICMPv6 fields: `type`, `code`, `checksum`, `id`, `seq`                                             |
| `layers.arp`   | ARP fields: `addr_type`, `protocol`, `hw_address_size`, `prot_address_size`, `operation`, `sender_mac`, `sender_ip`, `target_mac`, `target_ip` |

---

### 4. Integration with huatuo-bamai

huatuo-bamai launches `dropwatch` as a subprocess and uses `--output-storage` to send events to the built-in processing pipeline, which ultimately stores them in Elasticsearch. Typical parameters:

```bash
dropwatch \
  --bpf-path <CoreBpfDir>/dropwatch.o \
  --output-storage /var/run/huatuo-toolstream.sock \
  --filter "tcp"
```

#### 4.1 Configuration Reference (`huatuo-bamai.conf`)

```toml
[EventTracing]
    # Optional call-stack filters. dropwatch discards events whose stack matches a configured regex.
    # Default: []
    IssuesList = []

[EventTracing.Dropwatch]
    # tcpdump filter expression, forwarded to dropwatch --filter.
    # Default: "tcp"
    Filter = "tcp"

    # Forwarded to dropwatch --max-events-per-second.
    # Default: 100
    MaxEventsPerSecond = 100
```

#### 4.2 Noise Filtering

No call-stack noise rule is enabled by default. When `EventTracing.IssuesList` is configured, huatuo-bamai discards matching events. The following patterns are possible operator-configured filters; validate them against the local kernel and workload before enabling them:

| Pattern                                | Stack Frame Prefix                | Reason                                                                                                      |
| -------------------------------------- | --------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| ARP/neighbor table expiry              | `neigh_invalidate/`               | Neighbor table entry expiration cleanup; does not affect any active data flow. Remove the rule from `EventTracing.IssuesList` to disable this filter. |
| bnxt NIC TX completion                 | `bnxt_tx_int/` or `__bnxt_tx_int/` | The Broadcom bnxt NIC driver calls `kfree_skb` to release SKBs after DMA transmit completion; this is normal behavior, not a drop. |

---

## Closing

{{% alert color="info" %}}
<div style="text-align: center;">
Stars welcome: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
</div>
{{% /alert %}}
