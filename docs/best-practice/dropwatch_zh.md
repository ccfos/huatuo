---
title: 网络丢包
type: docs
description: ""
author: HUATUO Team
date: 2026-06-05
weight: 4
---

{{% alert color="info" title="🎯 关于 HUATUO（华佗）" %}}
<div style="text-align: left;">
HUATUO（华佗）是由滴滴开源并依托 CCF（中国计算机学会）孵化的操作系统深度观测项目，广泛应用于AI 计算、AI 沙箱、云原生通用计算、云服务、基础架构服务等场景。
</div>
{{% /alert %}}

## 📖 概述

dropwatch 是 HUATUO 提供的内核网络丢包观测工具。它通过挂载内核 Tracepoint `tracepoint/skb/kfree_skb` 实时采集网络丢包事件，输出完整的丢包上下文：协议类型、IP 五元组、进程名、PID、网络设备、MAC 地址，以及触发丢包的完整内核调用栈。

dropwatch 支持 tcpdump 风格的内核侧过滤，仅将匹配的数据包上报到用户空间。

此外，dropwatch 支持设备白名单/黑名单过滤、全局上报限速，并可与 huatuo-bamai 集成，将丢包事件存储至 Elasticsearch 进行长期分析。


---

## 🎯 场景

### 1. Kubernetes 云原生网络丢包诊断

在容器漂移、Pod 频繁重启、Service 端口冲突等场景下，通过 dropwatch 实时捕获 `kfree_skb` 事件并关联到具体容器，快速定位丢包根因。结合 `--filter "tcp and port <service-port>"` 过滤特定业务流量，将平均故障定位时间从小时级降低至分钟级。

### 2. 网络性能毛刺分析

针对间歇性网络延迟突增或吞吐下降，可根据事件中的内核调用栈定位丢包位置（如 `tcp_v4_rcv`、`ip_output`），区分防火墙丢弃、路由失败、缓冲区溢出等原因。

### 3. 多租户环境网络隔离故障排查

在共享网络命名空间或 veth 设备的容器环境中，组合使用 `--device` 和 `--filter`，只采集目标容器的丢包事件并排除其他租户流量。

### 4. 与可观测性平台集成

通过 `--output-storage` 将丢包事件发送给 huatuo-bamai，在 Elasticsearch 中与指标和日志关联。将事件与 Grafana 中的应用错误率、延迟时间线对齐，可判断内核丢包是否与应用异常同时发生。

---

## 🚀 使用

### 1. 过滤表达式

加载时，`internal/pcapfilter` 使用纯 Go 的 `huatuo-ai/go-pcap` fork，将 tcpdump 风格表达式编译为 eBPF 字节码。

#### 1.1 支持的表达式

`huatuo-ai/go-pcap` 编译器支持以下 tcpdump 风格的表达式。

**协议**

```text
ip   ip6   tcp   udp   sctp   icmp   icmp6   igmp
pim  esp   ah    vrrp  arp    rarp   stp
ip proto tcp
ip6 proto udp
```

**主机与网段**

```text
host 10.0.0.1
host 2001:db8::1
host api.example.com
src host 10.0.0.1
dst host 2001:db8::1
src net 192.168.1.0/24
dst net 2001:db8::/32
```

主机名在过滤器编译时解析一次，返回的全部 A 和 AAAA 地址都会参与匹配。

**端口、端口范围与方向**

```text
port 80
src port 443
dst portrange 8000-8080
src or dst port 53
src and dst portrange 1000-2000
```

未指定传输层协议时，端口和端口范围匹配 TCP、UDP 和 SCTP。

**组播与以太网**

```text
ip multicast    ip6 multicast    multicast    ether multicast
ether host 00:11:22:33:44:55
```

**VLAN、QinQ 与 MPLS**

```text
vlan and tcp port 443
vlan 100 and tcp port 443
vlan 100 and vlan 200 and ip
mpls 100 and ip
mpls 100 and mpls 200 and ip6
```

每重复一次 `vlan` 或 `mpls`，报文解析游标就向内推进一层封装。

**报文算术、TCP flags 与报文长度**

```text
ether[12:2] == 0x0800
ip[0] & 0x0f > 5
ip[12:4] == 0x0a000001
tcp[tcpflags] == tcp-syn
tcp[tcpflags] & (tcp-syn|tcp-ack) == (tcp-syn|tcp-ack)
len >= 128
```

报文访问支持 `ether`、`ip`、`ip6`、`tcp` 和 `udp`，读取宽度可为 1、2 或 4 字节。比较运算支持 `=`、`==`、`!=`、`>`、`<`、`>=`、`<=`；算术运算支持 `+`、`-`、`*`、`/`、`%`、`&`、`|`、`^`、`<<`、`>>`。每次访问都会生成报文长度检查，截断报文会直接拒绝，不会越界读取。

**布尔运算与分组**

```text
tcp and (port 80 or port 443)
tcp || udp
! (arp or rarp)
```

`and`/`&&`、`or`/`||`、`not`/`!` 分别等价。AND 的优先级高于 OR；分组不明确时应使用括号。

#### 1.2 不支持的表达式

下列 tcpdump 功能不支持，使用时会编译失败：

| 表达式 | 替代写法或限制 |
| --- | --- |
| `ip proto 6`、`ip6 proto 17` | 不支持数字协议号；使用 `ip proto tcp` 等协议名 |
| `ip protochain tcp` | 不支持 `protochain` 和 IPv6 扩展头遍历 |
| `less 128`、`greater 128` | 使用 `len < 128` 或 `len > 128` |
| `ip broadcast`、`ether broadcast` | 广播判断依赖当前无法取得的接口掩码 |
| `inbound`、`outbound`、`ifindex 2` | 无法使用抓包方向和接口元数据条件 |
| `gateway`、`fddi`、`wlan` | 不支持这些链路层限定符 |
| `pppoes` 及其他封装 | 仅实现以太网、VLAN/QinQ、MPLS 和 raw-IP 布局 |

> 每个表达式都会分别针对以太网（L2）和 raw-IP（L3）编译。`arp`、`rarp`、`stp`、`ether`、`vlan`、`mpls`、`ether[...]` 等仅支持 L2 的条件在 L3 路径上不匹配。

---

### 2. 运行 dropwatch

```bash
dropwatch [flags]
```

| 参数                          | 默认值 | 说明                                                         |
| ----------------------------- | ------ | ------------------------------------------------------------ |
| `--bpf-path <path>`           | 必填   | `dropwatch` eBPF 对象文件路径                                |
| `--filter <expr>`             | （无） | tcpdump 风格过滤表达式                                       |
| `--device <names>`            | （无） | 设备白名单：只采集这些设备的丢包，多个设备用逗号分隔（如 `eth0,eth1`） |
| `--device-excluded <names>`   | （无） | 设备黑名单：排除这些设备的丢包；与 `--device` 互斥           |
| `--duration <n>`              | 0      | 运行 N 秒后退出（0 表示持续运行直至 Ctrl-C）                 |
| `--output <json\|text>`       | `text` | 输出格式；设置 `--output-storage` 时会被忽略                 |
| `--output-storage <path>`     | （无） | 通过 Unix socket 将事件发送给 huatuo-bamai                   |
| `--task-id <id>`              | （无） | 关联本次会话的任务 ID；通常与 `--output-storage` 一起使用    |
| `--max-events-per-second <n>` | 0      | 全局上报限速，0 表示不限速；在 `--device` / `--filter` 后生效 |

`--filter` 与设备过滤相互正交，同时指定时两者均生效（AND 语义）。不指定 `--device` / `--device-excluded` 时采集所有设备。`--device` 和 `--device-excluded` 不能同时使用；白名单模式会丢弃没有 `net_device` 的 SKB，黑名单模式会放行没有 `net_device` 的 SKB。

#### 常用命令

```bash
# 只监控 eth0 上的 TCP 丢包
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --filter "tcp" --output json

# 抓取 60 秒后退出
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp and port 443" --duration 60 --output json

# 通过 jq 过滤仅显示 RST 包
sudo dropwatch --bpf-path bpf/dropwatch.o --output json 2>/dev/null | jq 'select(.layers.tcp.flags == "RST")'

# 采集 10 秒 JSON 输出，并排除调用栈包含 ip_finish_output 的事件
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'select(.stack | test("ip_finish_output") | not)'

# 采集 10 秒 JSON 输出，只打印除 stack 之外的字段
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'del(.stack)'
```

`jq -c` 会把每条匹配事件压缩成单行 JSON，便于保存为 NDJSON 或继续用管道处理。`test("ip_finish_output")` 判断 `stack` 是否匹配该正则，`not` 会把结果取反，因此上面的命令会排除包含 `ip_finish_output` 的调用栈；去掉 `| not` 后，就是只保留包含 `ip_finish_output` 的事件。`del(.stack)` 只从 jq 输出中删除 `stack` 字段，适合只查看时间、设备、进程、`packet_*` 元数据和 `layers` 协议字段。如需在存储前由用户态按调用栈过滤，可通过 huatuo-bamai 配置 `EventTracing.IssuesList` 实现（参见第 4 节）。

---

### 3. 事件数据结构

每条丢包事件以 NDJSON 对象（`types.DropWatchTracing`）表示。

| 字段                     | 类型     | 说明                                          |
| ------------------------ | -------- | --------------------------------------------- |
| `observed_timestamp`     | string   | 用户态接收/格式化事件时生成的 UTC 时间（RFC3339Nano），不是内核 hook 时间 |
| `type`                   | string   | 预留 TCP 事件类型，当前未设置（`1` 普通丢包、`2` SYN flood、`3`/`4` listen overflow） |
| `drop_reason`            | string   | 通过 BTF 解析的内核 `skb_drop_reason` 名称；无法解析时为数值，内核不支持时为 `NOT_SUPPORTED` |
| `source`                 | string   | 事件来源；独立运行 dropwatch 时为 `tools`，由 huatuo-bamai 启动时为 `events` |
| `comm`                   | string   | 丢包时的进程名                                |
| `pid`                    | uint64   | 进程 TGID                                     |
| `container_id`           | string   | 容器 ID（由 huatuo-bamai 解析填充，omitempty）|
| `memory_cgroup_css_addr` | string   | 内存 cgroup CSS 地址，用于容器归属解析        |
| `net_namespace_cookie`   | uint64   | 网络命名空间 cookie，用于容器归属解析         |
| `net_namespace_inum`    | uint32   | 网络命名空间 inum，用于容器归属解析           |
| `netdev_name`            | string   | 网络设备名（如 `eth0`）                       |
| `netdev_ifindex`         | uint32   | 网络接口索引                                  |
| `netdev_queue_mapping`   | uint32   | TX 队列映射                                   |
| `netdev_linkstatus`      | []string | 网络设备链路标志                              |
| `packet_skb_addr`        | string   | SKB 地址（十六进制，omitempty）              |
| `packet_eth_proto`       | string   | 原始 EtherType（十六进制，如 `0x0800`）       |
| `packet_len`             | uint32   | 数据包长度（字节）                            |
| `layers`                 | object   | 分层协议解析结果，缺失的层会省略              |
| `stack`                  | string   | 内核调用栈（换行分隔）                        |

`layers` 使用固定字段表达协议栈，不再依赖单独的协议枚举：

| 字段           | 说明                                                         |
| -------------- | ------------------------------------------------------------ |
| `layers.label` | 协议组合标签，如 `IPv4/TCP`、`IPv6/UDP`、`ARP`、`unknown`    |
| `layers.ether` | 存在真实 Ethernet header 时输出二层字段：`saddr`、`daddr`、`type`、`len`；仅 IEEE 802.3 framing 的 `len` 非零 |
| `layers.ipv4`  | IPv4 字段：`version`、`ihl`、`tos`、`len`、`id`、`flags`、`frag_offset`、`ttl`、`protocol`、`checksum`、`saddr`、`daddr` |
| `layers.ipv6`  | IPv6 字段：`version`、`traffic_class`、`flow_label`、`len`、`next_header`、`hop_limit`、`saddr`、`daddr` |
| `layers.tcp`   | TCP 字段：`sport`、`dport`、`seq`、`ack_seq`、`data_offset`、`flags`、`window`、`checksum`、`urgent`、`sk_state` |
| `layers.udp`   | UDP 字段：`sport`、`dport`、`len`、`checksum`                |
| `layers.icmp`  | ICMP/ICMPv6 字段：`type`、`code`、`checksum`、`id`、`seq`    |
| `layers.arp`   | ARP 字段：`addr_type`、`protocol`、`hw_address_size`、`prot_address_size`、`operation`、`sender_mac`、`sender_ip`、`target_mac`、`target_ip` |

---

### 4. 与 huatuo-bamai 集成

huatuo-bamai 以子进程形式启动 `dropwatch`，并通过 `--output-storage` 将事件发送到内置处理流程，并最终存储到 Elasticsearch。典型参数如下：

```bash
dropwatch \
  --bpf-path <CoreBpfDir>/dropwatch.o \
  --output-storage /var/run/huatuo-toolstream.sock \
  --filter "tcp"
```

#### 4.1 配置项参考（`huatuo-bamai.conf`）

```toml
[EventTracing]
    # 可选调用栈过滤。dropwatch 会丢弃 stack 匹配已配置正则的事件。
    # 默认值: []
    IssuesList = []

[EventTracing.Dropwatch]
    # tcpdump 过滤表达式，转发给 dropwatch --filter。
    # 默认值: "tcp"
    Filter = "tcp"

    # 转发给 dropwatch --max-events-per-second。
    # 默认值: 100
    MaxEventsPerSecond = 100
```

#### 4.2 噪声过滤

默认不启用任何调用栈噪声过滤。配置 `EventTracing.IssuesList` 后，huatuo-bamai 才会丢弃匹配事件。下表是可由运维人员配置的候选模式；启用前应结合本机内核和工作负载验证：

| 模式                                  | 调用栈帧前缀                       | 原因                                                         |
| ------------------------------------- | ---------------------------------- | ------------------------------------------------------------ |
| ARP/邻居表到期                        | `neigh_invalidate/`                | 邻居表项到期清理，不影响任何活跃数据流。可从 `EventTracing.IssuesList` 移除对应规则以关闭过滤。 |
| bnxt 网卡 TX 完成                     | `bnxt_tx_int/` 或 `__bnxt_tx_int/` | Broadcom bnxt 网卡驱动在 DMA 发送完成后调用 `kfree_skb` 释放 SKB，此为正常行为，非丢包。 |

---

## 🌟 结尾

{{% alert color="info" %}}
<div style="text-align: center;">
🌟 欢迎 Star: <a href="https://github.com/ccfos/huatuo" target="_blank">https://github.com/ccfos/huatuo</a>
<br><br>
👀 欢迎订阅官方微信公众号<br>
<img src="/img/contact-weixin.png" alt="微信公众号二维码" style="max-width: 200px; margin-top: 10px;">
</div>
{{% /alert %}}
