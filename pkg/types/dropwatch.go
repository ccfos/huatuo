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

import "huatuo-bamai/internal/packet"

// DropWatchTracing is the canonical JSON schema for a dropwatch event,
// shared between the dropwatch tool (producer) and huatuo-bamai (consumer).
type DropWatchTracing struct {
	ObservedTimestamp  string            `json:"observed_timestamp"`
	Type               string            `json:"type"`
	DropReason         string            `json:"drop_reason"`
	Comm               string            `json:"comm"`
	Pid                uint64            `json:"pid"`
	ContainerID        string            `json:"container_id,omitempty"`
	MemcgCssAddr       string            `json:"memcg_css,omitempty"`
	NetNamespaceCookie uint64            `json:"net_namespace_cookie,omitempty"`
	NetNamespaceInode  uint32            `json:"net_namespace_inode,omitempty"`
	NetdevName         string            `json:"netdev_name"`
	NetdevIfindex      uint32            `json:"netdev_ifindex"`
	NetdevQueueMapping uint32            `json:"netdev_queue_mapping"`
	NetdevLinkStatus   []string          `json:"netdev_linkstatus"`
	PacketSkbAddr      string            `json:"packet_skb_addr,omitempty"`
	PacketEthProto     string            `json:"packet_eth_proto"`
	PacketType         packet.PacketType `json:"packet_type"`
	PacketLen          uint32            `json:"packet_len"`
	PacketInfo         packet.PacketInfo `json:"packet_info,omitempty"`
	Source             string            `json:"source,omitempty"`
	Stack              string            `json:"stack"`
}
