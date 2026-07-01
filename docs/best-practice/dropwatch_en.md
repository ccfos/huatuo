---
title: Network Drop Monitoring (dropwatch)
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

dropwatch supports kernel-side filtering based on tcpdump-style filter expressions. The filter logic is compiled into eBPF bytecode at load time by the built-in pure-Go pcap compiler `internal/pcapfilter`. Filtering is performed entirely in kernel mode — only matching packets are reported to user space, reducing performance impact on the host.

In addition, dropwatch supports device whitelist/blacklist filtering, global per-second rate limiting, and integration with huatuo-bamai to store drop events in Elasticsearch for long-term analysis.

---

## Scenarios

### 1. Kubernetes Cloud-Native Network Drop Diagnosis

In scenarios such as container migration, frequent Pod restarts, and Service port conflicts, dropwatch captures `kfree_skb` events in real time and correlates them with specific containers to quickly identify the root cause of packet drops. Combined with `--filter "tcp and port <service-port>"` to filter specific business traffic, the mean time to root cause is reduced from hours to minutes.

### 2. Network Performance Spike Analysis

For intermittent spikes in network latency or drops in throughput, dropwatch collects drop events and, together with the kernel call stack, identifies the specific kernel function where the drop occurred (e.g. `tcp_v4_rcv`, `ip_output`). This helps distinguish whether the cause is a firewall drop, routing failure, buffer overflow, or other reasons.

### 3. Multi-Tenant Network Isolation Troubleshooting

In container environments that share network namespaces or veth devices, use `--device` to filter by network device and `--filter` to filter by protocol. This precisely captures drop events for the target container, preventing other tenants' traffic from interfering with the diagnosis.

### 4. Observability Platform Integration

Use `--output-storage` to send drop events to huatuo-bamai, which stores them in Elasticsearch for multi-dimensional correlation with metrics and logs. Overlay drop events on a Grafana timeline, aligned with application error rates and latency curves, to correlate kernel drops with application anomalies precisely.

---

## Usage

### 1. Filter Expressions

Filter expressions use tcpdump syntax. The built-in pure-Go pcap compiler `internal/pcapfilter` compiles them into eBPF bytecode at load time. Filtering is performed entirely in kernel mode, reducing host impact — only matching packets are reported to user space.

#### 1.1 Supported Expressions

`internal/pcapfilter` supports a subset of the standard tcpdump syntax. The following primitives are reliable:

**Protocols**

```text
ip   ip6   tcp   udp   icmp   icmp6   igmp   pim   esp   ah   vrrp   arp   rarp
ip proto tcp      ip6 proto udp        (protocol names only; numeric protocol numbers not supported)
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

#### 1.2 Unsupported Expressions

The following expressions are **not supported**. Using them causes compilation failures or incorrect match results:

| Expression                                            | Reason                                                        |
| ----------------------------------------------------- | ------------------------------------------------------------- |
| `tcp[tcpflags] & tcp-syn != 0`, `ip[8]`, `tcp[0:4]`  | Byte-offset expressions (`proto[offset:size]`) not implemented |
| `ip proto 6`, `ip6 proto 17`                          | Numeric protocol numbers not supported; use names (e.g. `ip proto tcp`) |
| `ether proto 0x0800`                                  | Hex EtherType not supported; use names (e.g. `ether proto ip`) |
| `sctp`                                                | Keyword not recognized                                        |
| `portrange 80-90`, `tcp portrange 1-100`              | Port ranges not supported                                     |
| `less N`, `greater N`                                 | Packet-length filtering not supported                         |
| `ip broadcast`, `ether broadcast`                     | Broadcast matching not supported                              |
| `vlan`, `mpls`, `pppoes`                              | Tunnel/encapsulation keywords not supported                   |
| `gateway`                                             | Not supported                                                 |

#### 1.3 Examples

```bash
# Monitor all TCP drops (default — reliable in both L2 and L3 contexts)
--filter "tcp"

# TCP and UDP
--filter "tcp or udp"

# Specific destination host (applies to both TCP and UDP)
--filter "dst host 10.0.0.1"

# Specific port
--filter "tcp and port 443"

# Exclude a noisy host
--filter "tcp and not host 169.254.169.254"

# Specific subnet + specific port
--filter "src net 192.168.1.0/24 and tcp dst port 3306"

# Monitor non-TCP drops (UDP and ICMP only — avoid "not tcp", which captures unknown L3 events)
--filter "udp or icmp"

# Monitor ARP drops only (effective only in L2 context; never matches at L3)
--filter "arp"
```

> **`--filter "ip"` / `--filter "ip6"` now correctly match the corresponding IP protocol family** (L2 by EtherType, L3 by version nibble). If you only care about a specific transport layer or host, prefer the more precise `tcp`, `udp`, `host`, or `ip proto <name>`.

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
# Text output, monitor TCP drops on all devices
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp"

# Monitor drops on eth0 only
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --output json

# Exclude loopback
sudo dropwatch --bpf-path bpf/dropwatch.o --device-excluded lo --output json

# Combine device and protocol filters
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --filter "tcp and port 443" --output json

# Capture for 60 seconds and exit
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp and port 443" --duration 60 --output json

# Forward events to a running huatuo-bamai instance
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp" --output-storage /var/run/huatuo/events.sock

# Use jq to filter and show only RST packets
sudo dropwatch --bpf-path bpf/dropwatch.o --output json 2>/dev/null | jq 'select(.layers.tcp.flags == "RST")'

# Capture 10 seconds of JSON output, excluding events whose stack contains ip_finish_output
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'select(.stack | test("ip_finish_output") | not)'

# Capture 10 seconds of JSON output, printing all fields except stack
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'del(.stack)'
```

`jq -c` compresses each matching event into a single-line JSON, convenient for saving as NDJSON or further pipe processing. `test("ip_finish_output")` checks whether `stack` matches the regex; `not` negates the result, so the command above excludes stacks containing `ip_finish_output`. Remove `| not` to keep only those containing `ip_finish_output`. `del(.stack)` removes the `stack` field from the jq output, useful for viewing just the timestamp, device, process, `packet_*` metadata, and `layers` protocol fields. For kernel-side call-stack filtering, configure `EventTracing.IssuesList` in huatuo-bamai (see Section 4).

---

### 3. Event Data Structure

Each drop event is represented as an NDJSON object (`types.DropWatchTracing`).

| Field                    | Type     | Description                                                   |
| ------------------------ | -------- | ------------------------------------------------------------- |
| `observed_timestamp`     | string   | UTC timestamp when the event was captured (RFC3339Nano)       |
| `type`                   | string   | Event type reserved field; currently empty string             |
| `drop_reason`            | string   | Drop reason reserved field; currently empty string            |
| `source`                 | string   | Event source; when present, indicates `events` or `tools` (omitempty) |
| `comm`                   | string   | Process name at the time of the drop                          |
| `pid`                    | uint64   | Process TGID                                                  |
| `container_id`           | string   | Container ID (populated by huatuo-bamai resolution, omitempty) |
| `memory_cgroup_css_addr` | string   | Memory cgroup CSS address, used for container resolution       |
| `net_namespace_cookie`   | uint64   | Network namespace cookie, used for container resolution        |
| `net_namespace_inode`    | uint32   | Network namespace inode, used for container resolution         |
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
| `layers.ether` | L2 fields: `src`, `dst`, `type`, `len` (present only for 802.3 frames)                                   |
| `layers.ipv4`  | IPv4 fields: `version`, `ihl`, `tos`, `len`, `id`, `flags`, `frag_offset`, `ttl`, `protocol`, `checksum`, `src`, `dst` |
| `layers.ipv6`  | IPv6 fields: `version`, `traffic_class`, `flow_label`, `len`, `next_header`, `hop_limit`, `src`, `dst`  |
| `layers.tcp`   | TCP fields: `sport`, `dport`, `seq`, `ack`, `data_offset`, `flags`, `window`, `checksum`, `urgent`, `sk_state` |
| `layers.udp`   | UDP fields: `sport`, `dport`, `len`, `checksum`                                                         |
| `layers.icmp`  | ICMP/ICMPv6 fields: `type`, `code`, `checksum`, `id`, `seq`                                             |
| `layers.arp`   | ARP fields: `addr_type`, `protocol`, `hw_address_size`, `prot_address_size`, `operation`, `sender_mac`, `sender_ip`, `target_mac`, `target_ip` |

---

### 4. Integration with huatuo-bamai

huatuo-bamai launches `dropwatch` as a subprocess and uses `--output-storage` to send events to the built-in processing pipeline, which ultimately stores them in Elasticsearch. Typical parameters:

```bash
dropwatch \
  --bpf-path <CoreBpfDir>/dropwatch.o \
  --output-storage /var/run/huatuo/events.sock \
  --filter "tcp"
```

#### 4.1 Configuration Reference (`huatuo-bamai.conf`)

```toml
[EventTracing]
    # Known noisy call-stack filters. dropwatch discards events whose stack matches these regexes.
    # The default examples cover neighbor table cleanup and bnxt TX completion SKB frees.
    IssuesList = [["neigh_invalidate", "neigh_invalidate"], ["bnxt_tx_int", "bnxt_tx_int"]]

[EventTracing.Dropwatch]
    # tcpdump filter expression, forwarded to dropwatch --filter.
    # Default: "tcp"
    Filter = "tcp"

    # Forwarded to dropwatch --max-events-per-second.
    # Default: 100
    MaxEventsPerSecond = 100
```

#### 4.2 Noise Filtering

The following three categories of `kfree_skb` events are filtered by default because they are not real data-plane drops:

| Pattern                                | Stack Frame Prefix                | Reason                                                                                                      |
| -------------------------------------- | --------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| TCP `CLOSE_WAIT` + `skb_rbtree_purge`  | `skb_rbtree_purge/`               | Normal socket teardown: the kernel releases in-flight SKBs when closing a socket in `CLOSE_WAIT` state.     |
| ARP/neighbor table expiry              | `neigh_invalidate/`               | Neighbor table entry expiration cleanup; does not affect any active data flow. Remove the rule from `EventTracing.IssuesList` to disable this filter. |
| bnxt NIC TX completion                 | `bnxt_tx_int/` or `__bnxt_tx_int/` | The Broadcom bnxt NIC driver calls `kfree_skb` to release SKBs after DMA transmit completion; this is normal behavior, not a drop. |

---

## Closing

{{% alert color="info" %}}
<div style="text-align: center;">
Stars welcome: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
</div>
{{% /alert %}}
