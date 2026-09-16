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
	"github.com/ccfos/huatuo/internal/packet"
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
	ObservedTimestamp   string         `json:"observed_timestamp,omitempty"`
	Type                string         `json:"type,omitempty"`
	DropSource          string         `json:"drop_source"`
	DropReason          string         `json:"drop_reason"`
	DropReasonGroup     string         `json:"drop_reason_group,omitempty"`
	DropLocation        string         `json:"drop_location,omitempty"`
	Source              string         `json:"source,omitempty"`
	Comm                string         `json:"comm"`
	PID                 uint64         `json:"pid"`
	ContainerID         string         `json:"container_id,omitempty"`
	MemoryCgroupCSSAddr string         `json:"memory_cgroup_css_addr"`
	NetNamespaceCookie  uint64         `json:"net_namespace_cookie"`
	NetNamespaceInum    uint32         `json:"net_namespace_inum"`
	NetdevName          string         `json:"netdev_name"`
	NetdevIfindex       uint32         `json:"netdev_ifindex"`
	NetdevQueueMapping  uint32         `json:"netdev_queue_mapping"`
	NetdevLinkStatus    []string       `json:"netdev_linkstatus"`
	PacketSkbAddr       string         `json:"packet_skb_addr,omitempty"`
	PacketEthProto      string         `json:"packet_eth_proto"`
	PacketLenBytes      uint32         `json:"packet_len_bytes"`
	Layers              *packet.Packet `json:"layers,omitempty"`
	Stack               string         `json:"stack"`
}

// DropwatchPerfStatus reports cumulative diagnostic counters for the embedded
// dropwatch source.
type DropwatchPerfStatus struct {
	// PerfLost counts dropwatch events that the kernel failed to write to the
	// perf stream (bpf_perf_event_output returned a negative error, e.g. no
	// reader attached for the current CPU).
	PerfLost uint64 `json:"perf_lost"`
	// LostSamples counts perf ring buffer overflows reported by the reader as
	// PERF_RECORD_LOST records. It is only observable while the reader is
	// running and is delivered with the next successful event.
	LostSamples uint64 `json:"lost_samples,omitempty"`
	// RateLimited counts dropwatch events rejected by the rate limiter, read
	// from the limiter state map's total_missed counter.
	RateLimited uint64 `json:"rate_limited"`
}
