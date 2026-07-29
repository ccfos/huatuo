// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/utils/bytesutil"
)

func TestDropwatchPacketEventParse(t *testing.T) {
	const (
		wantKtimeNS             uint64 = 12_345_678_901_234_567
		wantTgidPid             uint64 = uint64(4321)<<32 | 8765
		wantNetCookie           uint64 = 0x0123_4567_89ab_cdef
		wantSkbAddr             uint64 = 0xffff_8880_1234_5678
		wantMemoryCgroupCSSAddr uint64 = 0xffff_8880_abcd_ef00
		wantNetdevIfindex       uint32 = 42
		wantNetdevFlags         uint32 = 0x1003
		wantNetdevQueueMapping  uint32 = 17
		wantDropReason          uint32 = 6
		wantNetInode            uint32 = 0xf000_0000
		wantNetdevName                 = "eth0"
		wantComm                       = "nginx-worker"
		wantEthProto            uint16 = 0x0800
		wantRawLen              uint16 = 120
		wantStackSize           uint64 = 2
		wantFirstStack          uint64 = 0xffff_8880_dead_beef
		wantLastStack           uint64 = 0xffff_8880_cafe_babe
	)

	buf := make([]byte, abi.DropwatchPacketEventSize)

	native := binary.NativeEndian
	native.PutUint64(buf[0:], wantKtimeNS)              // ktime_ns
	native.PutUint64(buf[8:], wantTgidPid)              // tgid_pid
	native.PutUint64(buf[16:], wantNetCookie)           // net_cookie
	native.PutUint64(buf[24:], wantSkbAddr)             // kfree_skb_addr
	native.PutUint64(buf[32:], wantMemoryCgroupCSSAddr) // memcg_css_addr
	native.PutUint32(buf[40:], wantNetdevIfindex)       // ifindex
	native.PutUint32(buf[44:], wantNetdevFlags)         // dev_flags
	native.PutUint32(buf[48:], wantNetdevQueueMapping)  // queue_mapping
	native.PutUint32(buf[52:], wantDropReason)          // drop_reason
	native.PutUint32(buf[56:], wantNetInode)            // net_inum
	copy(buf[60:], wantNetdevName)                      // dev_name[16]
	copy(buf[76:], wantComm)                            // comm[16]
	// buf[92:96] is the C tail padding, zero.
	native.PutUint16(buf[96:], wantEthProto)    // pkt_hdr.eth_proto
	native.PutUint16(buf[98:], wantRawLen)      // pkt_hdr.raw_len
	native.PutUint64(buf[232:], wantStackSize)  // stack_size
	native.PutUint64(buf[240:], wantFirstStack) // stack[0]
	native.PutUint64(buf[1248:], wantLastStack) // stack[126]

	var event abi.DropwatchPacketEvent
	if err := binary.Read(bytes.NewReader(buf), binary.NativeEndian, &event); err != nil {
		t.Fatalf("binary.Read: %v", err)
	}
	meta := event.Meta

	if meta.DropReason != wantDropReason {
		t.Errorf("DropReason = %d, want %d", meta.DropReason, wantDropReason)
	}
	if meta.NetInum != wantNetInode {
		t.Errorf("NetInum = %d, want %d", meta.NetInum, wantNetInode)
	}
	if got := bytesutil.ToStr(meta.DevName[:]); got != wantNetdevName {
		t.Errorf("DevName = %q, want %q", got, wantNetdevName)
	}
	if got := bytesutil.ToStr(meta.Comm[:]); got != wantComm {
		t.Errorf("Comm = %q, want %q", got, wantComm)
	}
	if meta.KtimeNS != wantKtimeNS || meta.TGIDPID != wantTgidPid || meta.NetCookie != wantNetCookie ||
		meta.KfreeSKBAddr != wantSkbAddr || meta.MemcgCSSAddr != wantMemoryCgroupCSSAddr {
		t.Errorf("u64 header fields misparsed: %+v", meta)
	}
	if meta.Ifindex != wantNetdevIfindex || meta.DevFlags != wantNetdevFlags ||
		meta.QueueMapping != wantNetdevQueueMapping {
		t.Errorf("netdev fields misparsed: %+v", meta)
	}
	if event.PktHdr.EthProto != wantEthProto || event.PktHdr.RawLen != wantRawLen {
		t.Errorf("packet header misparsed: %+v", event.PktHdr)
	}
	if event.StackSize != wantStackSize ||
		event.Stack[0] != wantFirstStack ||
		event.Stack[len(event.Stack)-1] != wantLastStack {
		t.Errorf("stack fields misparsed: size=%d first=%x last=%x",
			event.StackSize, event.Stack[0], event.Stack[len(event.Stack)-1])
	}
}
