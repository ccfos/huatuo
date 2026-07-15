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

	"huatuo-bamai/internal/utils/bytesutil"
)

// TestPacketMetaParse decodes a perf sample laid out exactly like struct
// packet_meta in bpf/dropwatch.c and checks that every field lands where the
// kernel wrote it. It fails if the Go mirror drifts from the C layout, e.g.
// the stale 4-byte Type field that shifted net_inum, dev_name and comm.
func TestPacketMetaParse(t *testing.T) {
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
	)

	buf := make([]byte, 96)

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

	var meta packetMeta
	if err := binary.Read(bytes.NewReader(buf), binary.NativeEndian, &meta); err != nil {
		t.Fatalf("binary.Read: %v", err)
	}

	if meta.DropReason != wantDropReason {
		t.Errorf("DropReason = %d, want %d", meta.DropReason, wantDropReason)
	}
	if meta.NetInode != wantNetInode {
		t.Errorf("NetInode = %d, want %d", meta.NetInode, wantNetInode)
	}
	if got := bytesutil.ToStr(meta.NetdevName[:]); got != wantNetdevName {
		t.Errorf("NetdevName = %q, want %q", got, wantNetdevName)
	}
	if got := bytesutil.ToStr(meta.Comm[:]); got != wantComm {
		t.Errorf("Comm = %q, want %q", got, wantComm)
	}
	if meta.KtimeNS != wantKtimeNS || meta.TgidPid != wantTgidPid || meta.NetCookie != wantNetCookie ||
		meta.SkbAddr != wantSkbAddr || meta.MemoryCgroupCSSAddr != wantMemoryCgroupCSSAddr {
		t.Errorf("u64 header fields misparsed: %+v", meta)
	}
	if meta.NetdevIfindex != wantNetdevIfindex || meta.NetdevFlags != wantNetdevFlags ||
		meta.NetdevQueueMapping != wantNetdevQueueMapping {
		t.Errorf("netdev fields misparsed: %+v", meta)
	}
}
