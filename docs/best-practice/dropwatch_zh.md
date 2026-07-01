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

dropwatch 支持基于 tcpdump 风格过滤表达式的内核侧过滤，过滤逻辑由内置的纯 Go pcap 编译器 `internal/pcapfilter` 在加载时编译为 eBPF 字节码，过滤完全在内核态执行，只有匹配的数据包才会上报到用户空间，降低对宿主机的性能影响。

此外，dropwatch 支持设备白名单/黑名单过滤、全局上报限速，并可与 huatuo-bamai 集成，将丢包事件存储至 Elasticsearch 进行长期分析。


---

## 🎯 场景

### 1. Kubernetes 云原生网络丢包诊断

在容器漂移、Pod 频繁重启、Service 端口冲突等场景下，通过 dropwatch 实时捕获 `kfree_skb` 事件并关联到具体容器，快速定位丢包根因。结合 `--filter "tcp and port <service-port>"` 过滤特定业务流量，将平均故障定位时间从小时级降低至分钟级。

### 2. 网络性能毛刺分析

针对间歇性网络延迟突增、吞吐下降等问题，通过 dropwatch 采集丢包事件，结合内核调用栈定位丢包发生的具体内核函数（如 `tcp_v4_rcv`、`ip_output` 等），辅助区分是防火墙丢弃、路由失败还是缓冲区溢出等原因。

### 3. 多租户环境网络隔离故障排查

在共享网络命名空间或 veth 设备的容器环境中，通过 `--device` 过滤指定网络设备，结合 `--filter` 过滤特定协议，精确采集目标容器的丢包事件，避免其他租户流量干扰诊断结果。

### 4. 与可观测性平台集成

通过 `--output-storage` 将丢包事件发送给 huatuo-bamai，存储至 Elasticsearch 后与指标、日志进行多维关联分析。将丢包事件叠加到 Grafana 时间线上，与应用错误率、延迟曲线对齐，实现内核丢包与应用异常的精确关联。

---

## 🚀 使用

### 1. 过滤表达式

过滤表达式采用 tcpdump 语法，由内置的纯 Go pcap 编译器 `internal/pcapfilter` 在加载时编译为 eBPF 字节码，过滤完全在内核侧执行，降低对宿主机影响，只有匹配的数据包才会上报到用户空间。

#### 1.1 支持的表达式

`internal/pcapfilter` 支持 tcpdump 标准语法的一个子集，下列原语可以可靠使用：

**协议**

```text
ip   ip6   tcp   udp   icmp   icmp6   igmp   pim   esp   ah   vrrp   arp   rarp
ip proto tcp      ip6 proto udp        （仅协议名，不支持数字协议号）
```

**主机地址**

```text
host 10.0.0.1
src host 10.0.0.1
dst host 10.0.0.1
```

**端口**

```text
port 80
src port 443
dst port 8080
```

**网段（CIDR）**

```text
net 10.0.0.0/8
src net 192.168.1.0/24
dst net 172.16.0.0/12
```

**组播与以太地址**

```text
ip multicast    ip6 multicast    multicast    ether multicast
ether host 00:11:22:33:44:55
```

**布尔运算与分组**

```text
tcp and port 80
tcp or udp
not arp
tcp and (port 80 or port 443)
ip and src net 192.168.1.0/24 and tcp dst port 3306
```

#### 1.2 不支持的表达式

下列表达式**不支持**，使用后会导致编译失败或产生错误的匹配结果：

| 表达式                                              | 原因                                                        |
| --------------------------------------------------- | ----------------------------------------------------------- |
| `tcp[tcpflags] & tcp-syn != 0`、`ip[8]`、`tcp[0:4]` | 字节偏移表达式（`proto[offset:size]`）未实现                |
| `ip proto 6`、`ip6 proto 17`                        | 不支持数字协议号，请改用协议名（如 `ip proto tcp`）         |
| `ether proto 0x0800`                                | 不支持十六进制 EtherType，请改用名字（如 `ether proto ip`） |
| `sctp`                                              | 关键字未识别                                                |
| `portrange 80-90`、`tcp portrange 1-100`            | 不支持端口范围                                              |
| `less N`、`greater N`                               | 不支持按报文长度过滤                                        |
| `ip broadcast`、`ether broadcast`                   | 不支持广播匹配                                              |
| `vlan`、`mpls`、`pppoes`                            | 不支持隧道/封装关键字                                       |
| `gateway`                                           | 不支持                                                      |

#### 1.3 推荐写法示例

```bash
# 监控所有 TCP 丢包（默认值——L2 和 L3 上下文均可靠）
--filter "tcp"

# TCP 和 UDP
--filter "tcp or udp"

# 指定目标主机（TCP 和 UDP 均适用）
--filter "dst host 10.0.0.1"

# 指定端口
--filter "tcp and port 443"

# 排除噪声主机
--filter "tcp and not host 169.254.169.254"

# 指定子网 + 指定端口
--filter "src net 192.168.1.0/24 and tcp dst port 3306"

# 监控非 TCP 的丢包（仅 UDP 和 ICMP——不要用 "not tcp"，会捕获到未知 L3 事件）
--filter "udp or icmp"

# 仅监控 ARP 丢包（仅 L2 上下文有效，L3 永远不匹配）
--filter "arp"
```

> **`--filter "ip"` / `--filter "ip6"` 现可正确匹配对应 IP 协议族**（L2 按 EtherType、L3 按版本 nibble）。若只关心特定传输层或主机，仍建议用更精确的 `tcp`、`udp`、`host` 或 `ip proto <name>`。

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
# 文本格式输出，监控所有设备的 TCP 丢包
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp"

# 只监控 eth0 上的丢包
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --output json

# 排除 loopback
sudo dropwatch --bpf-path bpf/dropwatch.o --device-excluded lo --output json

# 设备过滤与协议过滤组合
sudo dropwatch --bpf-path bpf/dropwatch.o --device eth0 --filter "tcp and port 443" --output json

# 抓取 60 秒后退出
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp and port 443" --duration 60 --output json

# 将事件转发给正在运行的 huatuo-bamai 实例
sudo dropwatch --bpf-path bpf/dropwatch.o --filter "tcp" --output-storage /var/run/huatuo/events.sock

# 通过 jq 过滤仅显示 RST 包
sudo dropwatch --bpf-path bpf/dropwatch.o --output json 2>/dev/null | jq 'select(.layers.tcp.flags == "RST")'

# 采集 10 秒 JSON 输出，并排除调用栈包含 ip_finish_output 的事件
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'select(.stack | test("ip_finish_output") | not)'

# 采集 10 秒 JSON 输出，只打印除 stack 之外的字段
sudo dropwatch --output json --duration 10 --bpf-path bpf/dropwatch.o | jq -c 'del(.stack)'
```

`jq -c` 会把每条匹配事件压缩成单行 JSON，便于保存为 NDJSON 或继续用管道处理。`test("ip_finish_output")` 判断 `stack` 是否匹配该正则，`not` 会把结果取反，因此上面的命令会排除包含 `ip_finish_output` 的调用栈；去掉 `| not` 后，就是只保留包含 `ip_finish_output` 的事件。`del(.stack)` 只从 jq 输出中删除 `stack` 字段，适合只查看时间、设备、进程、`packet_*` 元数据和 `layers` 协议字段。如需在内核侧按调用栈过滤，可通过 huatuo-bamai 配置 `EventTracing.IssuesList` 实现（参见第 4 节）。

---

### 3. 事件数据结构

每条丢包事件以 JSON 对象（`types.DropWatchTracing`）表示。

| 字段                     | 类型     | 说明                                          |
| ------------------------ | -------- | --------------------------------------------- |
| `observed_timestamp`     | string   | 采集到事件的 UTC 时间戳（RFC3339Nano）        |
| `type`                   | string   | 事件类型保留字段，当前为空字符串              |
| `drop_reason`            | string   | 丢包原因保留字段，当前为空字符串              |
| `source`                 | string   | 事件来源，存在时标识 `events` 或 `tools`（omitempty） |
| `comm`                   | string   | 丢包时的进程名                                |
| `pid`                    | uint64   | 进程 TGID                                     |
| `container_id`           | string   | 容器 ID（由 huatuo-bamai 解析填充，omitempty）|
| `memory_cgroup_css_addr` | string   | 内存 cgroup CSS 地址，用于容器归属解析        |
| `net_namespace_cookie`   | uint64   | 网络命名空间 cookie，用于容器归属解析         |
| `net_namespace_inode`    | uint32   | 网络命名空间 inode，用于容器归属解析          |
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
| `layers.ether` | 二层字段：`src`、`dst`、`type`、`len`（仅 802.3 帧存在）    |
| `layers.ipv4`  | IPv4 字段：`version`、`ihl`、`tos`、`len`、`id`、`flags`、`frag_offset`、`ttl`、`protocol`、`checksum`、`src`、`dst` |
| `layers.ipv6`  | IPv6 字段：`version`、`traffic_class`、`flow_label`、`len`、`next_header`、`hop_limit`、`src`、`dst` |
| `layers.tcp`   | TCP 字段：`sport`、`dport`、`seq`、`ack`、`data_offset`、`flags`、`window`、`checksum`、`urgent`、`sk_state` |
| `layers.udp`   | UDP 字段：`sport`、`dport`、`len`、`checksum`                |
| `layers.icmp`  | ICMP/ICMPv6 字段：`type`、`code`、`checksum`、`id`、`seq`    |
| `layers.arp`   | ARP 字段：`addr_type`、`protocol`、`hw_address_size`、`prot_address_size`、`operation`、`sender_mac`、`sender_ip`、`target_mac`、`target_ip` |

---

### 4. 与 huatuo-bamai 集成

huatuo-bamai 以子进程形式启动 `dropwatch`，并通过 `--output-storage` 将事件发送到内置处理流程，并最终存储到 Elasticsearch。典型参数如下：

```bash
dropwatch \
  --bpf-path <CoreBpfDir>/dropwatch.o \
  --output-storage /var/run/huatuo/events.sock \
  --filter "tcp"
```

#### 4.1 配置项参考（`huatuo-bamai.conf`）

```toml
[EventTracing]
    # 已知噪声调用栈过滤。dropwatch 会丢弃 stack 匹配这些正则的事件。
    # 默认示例覆盖邻居表清理和 bnxt TX 完成释放 SKB。
    IssuesList = [["neigh_invalidate", "neigh_invalidate"], ["bnxt_tx_int", "bnxt_tx_int"]]

[EventTracing.Dropwatch]
    # tcpdump 过滤表达式，转发给 dropwatch --filter。
    # 默认值: "tcp"
    Filter = "tcp"

    # 转发给 dropwatch --max-events-per-second。
    # 默认值: 100
    MaxEventsPerSecond = 100
```

#### 4.2 噪声过滤

以下三类 `kfree_skb` 事件默认被过滤，因为它们不是真实的数据面丢包：

| 模式                                  | 调用栈帧前缀                       | 原因                                                         |
| ------------------------------------- | ---------------------------------- | ------------------------------------------------------------ |
| TCP `CLOSE_WAIT` + `skb_rbtree_purge` | `skb_rbtree_purge/`                | 正常的套接字关闭流程：内核在关闭 `CLOSE_WAIT` 状态的套接字时会释放飞行中的 SKB。 |
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
