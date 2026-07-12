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
	buf := make([]byte, 96)

	le := binary.LittleEndian
	le.PutUint64(buf[0:], 1111)        // ktime_ns
	le.PutUint64(buf[8:], 2222)        // tgid_pid
	le.PutUint64(buf[16:], 3333)       // net_cookie
	le.PutUint64(buf[24:], 4444)       // kfree_skb_addr
	le.PutUint64(buf[32:], 5555)       // memcg_css_addr
	le.PutUint32(buf[40:], 7)          // ifindex
	le.PutUint32(buf[44:], 0x1003)     // dev_flags
	le.PutUint32(buf[48:], 2)          // queue_mapping
	le.PutUint32(buf[52:], 6)          // drop_reason
	le.PutUint32(buf[56:], 4026531840) // net_inum
	copy(buf[60:], "eth0")             // dev_name[16]
	copy(buf[76:], "nginx-worker")     // comm[16]
	// buf[92:96] is the C tail padding, zero.

	var meta packetMeta
	if err := binary.Read(bytes.NewReader(buf), binary.NativeEndian, &meta); err != nil {
		t.Fatalf("binary.Read: %v", err)
	}

	if meta.DropReason != 6 {
		t.Errorf("DropReason = %d, want 6", meta.DropReason)
	}
	if meta.NetInode != 4026531840 {
		t.Errorf("NetInode = %d, want 4026531840", meta.NetInode)
	}
	if got := bytesutil.ToStr(meta.NetdevName[:]); got != "eth0" {
		t.Errorf("NetdevName = %q, want \"eth0\"", got)
	}
	if got := bytesutil.ToStr(meta.Comm[:]); got != "nginx-worker" {
		t.Errorf("Comm = %q, want \"nginx-worker\"", got)
	}
	if meta.KtimeNS != 1111 || meta.TgidPid != 2222 || meta.NetCookie != 3333 ||
		meta.SkbAddr != 4444 || meta.MemoryCgroupCSSAddr != 5555 {
		t.Errorf("u64 header fields misparsed: %+v", meta)
	}
	if meta.NetdevIfindex != 7 || meta.NetdevFlags != 0x1003 || meta.NetdevQueueMapping != 2 {
		t.Errorf("netdev fields misparsed: %+v", meta)
	}
}
