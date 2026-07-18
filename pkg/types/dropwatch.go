// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

import (
	"huatuo-bamai/internal/packet"
)

// DropWatchTracing is the canonical JSON schema for a dropwatch event,
// shared between the dropwatch tool (producer) and huatuo-bamai (consumer).
//
// Layers holds the layered parse result (nested object per network layer).
// Consumers select layers with field checks, e.g. `ev.Layers.TCP != nil`,
// instead of a separate type-tag field. The name "Layers" mirrors gopacket's
// terminology and keeps the field distinct from the `Packet*` BPF-metadata
// prefix family above.
type DropWatchTracing struct {
	ObservedTimestamp   string         `json:"observed_timestamp"`
	Type                string         `json:"type"`
	DropReason          string         `json:"drop_reason"`
	Source              string         `json:"source,omitempty"`
	Comm                string         `json:"comm"`
	Pid                 uint64         `json:"pid"`
	ContainerID         string         `json:"container_id,omitempty"`
	MemoryCgroupCSSAddr string         `json:"memory_cgroup_css_addr"`
	NetNamespaceCookie  uint64         `json:"net_namespace_cookie"`
	NetNamespaceInode   uint32         `json:"net_namespace_inode"`
	NetdevName          string         `json:"netdev_name"`
	NetdevIfindex       uint32         `json:"netdev_ifindex"`
	NetdevQueueMapping  uint32         `json:"netdev_queue_mapping"`
	NetdevLinkStatus    []string       `json:"netdev_linkstatus"`
	PacketSkbAddr       string         `json:"packet_skb_addr,omitempty"`
	PacketEthProto      string         `json:"packet_eth_proto"`
	PacketLen           uint32         `json:"packet_len"`
	Layers              *packet.Packet `json:"layers,omitempty"`
	Stack               string         `json:"stack"`

	// DropLayer annotates where in the network path the packet was lost, so
	// kernel dropwatch events (protocol/driver stack frames) and hardware
	// rx_dropped aggregates from netdev_hw can be correlated on one timeline.
	// See the DropLayer* values below. Omitempty keeps legacy events unchanged.
	DropLayer string `json:"drop_layer,omitempty"`

	// DropCount carries the number of packets lost for aggregate events that
	// do not represent a single skb — today only the netdev_hw hardware-drop
	// event (DropLayer == DropLayerHardware) sets it to the per-interval delta.
	// Per-packet kernel dropwatch events leave it unset.
	DropCount uint64 `json:"drop_count,omitempty"`
}

// Values for DropWatchTracing.Source.
const (
	DropSourceTypesEvent  = "events"
	DropSourceTypesTool   = "tools"
	DropSourceTypesMetric = "metrics" // hardware rx_dropped aggregate from netdev_hw
)

// Values for DropWatchTracing.DropLayer — the network-path layer where a drop
// happened. Hardware drops (counted by the NIC/driver before reaching the
// stack) never reach the kfree_skb tracepoint, so they are emitted by netdev_hw
// directly; kernel drops are classified from the drop-location stack frame.
const (
	DropLayerHardware = "hardware" // NIC/driver drop counted via rx_dropped (netdev_hw)
	DropLayerDriver   = "driver"   // link-layer ingress: netif_receive_skb, napi, qdisc...
	DropLayerProtocol = "protocol" // L3/L4 stack: ip, ipv6, tcp, udp, icmp, arp, socket...
	DropLayerUnknown  = "unknown"  // drop location could not be classified
)
