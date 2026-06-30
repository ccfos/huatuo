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

package main

import (
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/symbol"
)

type packetMeta struct {
	KtimeNS             uint64
	TgidPid             uint64
	NetCookie           uint64
	SkbAddr             uint64
	MemoryCgroupCSSAddr uint64
	NetdevIfindex       uint32
	NetdevFlags         uint32
	NetdevQueueMapping  uint32
	DropReason          uint32
	Type                uint32
	NetInode            uint32
	NetdevName          [bpf.NetdevNameLen]byte
	Comm                [bpf.TaskCommLen]byte
}

type packetRaw struct {
	EthProto  uint16
	RawLen    uint16
	HasEthHdr uint16
	Pad       uint16
	PktLen    uint32
	SkState   uint32
	Raw       [packet.RawCapacity]byte
}

type dropPacketEvent struct {
	Meta      packetMeta
	Raw       packetRaw
	StackSize uint64
	Stack     [symbol.KsymStackMaxDepth]uint64
}

var (
	_ = [1]struct{}{}[96-unsafe.Sizeof(packetMeta{})]
	_ = [1]struct{}{}[136-unsafe.Sizeof(packetRaw{})]
	_ = [1]struct{}{}[240-unsafe.Offsetof(dropPacketEvent{}.Stack)]
)
