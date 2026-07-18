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

package events

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unsafe"

	"github.com/google/go-cmp/cmp"
)

// buildRetransmitBuf writes a 120-byte perf record with the given fields at the
// offsets mandated by bpf/net_retransmit_skb.c perf_event_t, so the test
// verifies the Go mirror independently rather than round-tripping the struct.
func buildRetransmitBuf(
	ktime, tgpid, memcg, cookie, pktlen uint64,
	netns, seq uint32, sport, dport uint16, family, state uint8,
	saddr, daddr [16]byte, comm, dev string,
) []byte {
	buf := make([]byte, 120)
	binary.LittleEndian.PutUint64(buf[0:], ktime)
	binary.LittleEndian.PutUint64(buf[8:], tgpid)
	binary.LittleEndian.PutUint64(buf[16:], memcg)
	binary.LittleEndian.PutUint64(buf[24:], cookie)
	binary.LittleEndian.PutUint64(buf[32:], pktlen)
	binary.LittleEndian.PutUint32(buf[40:], netns)
	binary.LittleEndian.PutUint32(buf[44:], seq)
	binary.LittleEndian.PutUint16(buf[48:], sport)
	binary.LittleEndian.PutUint16(buf[50:], dport)
	buf[52] = family
	buf[53] = state
	copy(buf[56:], saddr[:])
	copy(buf[72:], daddr[:])
	copy(buf[88:], comm)
	copy(buf[104:], dev)
	return buf
}

func TestNetRetransmitPerfEventSize(t *testing.T) {
	if got := unsafe.Sizeof(netRetransmitPerfEvent{}); got != 120 {
		t.Fatalf("netRetransmitPerfEvent size = %d, want 120 (must mirror bpf/net_retransmit_skb.c perf_event_t)", got)
	}
}

func TestReadNetRetransmitPerfEventIPv4(t *testing.T) {
	var saddr, daddr [16]byte
	copy(saddr[:], []byte{10, 0, 0, 1})
	copy(daddr[:], []byte{10, 0, 0, 2})

	// sport/dport stored network order, as tcp_hdr.source/dest are __be16;
	// netutil.Ntohs reverses them. 1234 = 0x04d2 → BE bytes 04 d2.
	buf := buildRetransmitBuf(
		1_000_000, 100<<32, 0xdeadbeef, 0, 1500,
		4026531840, 0x12345678, 0x04d2, 0x0050, afINET, 1,
		saddr, daddr, "curl", "eth0")

	pd, err := readNetRetransmitPerfEvent(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	family, s, d := pd.addrs()
	if family != "ipv4" || s != "10.0.0.1" || d != "10.0.0.2" {
		t.Errorf("addrs() = (%q,%q,%q), want (ipv4,10.0.0.1,10.0.0.2)", family, s, d)
	}
	if pid := pd.TgidPid >> 32; pid != 100 {
		t.Errorf("Pid = %d, want 100", pid)
	}
	if pd.NetnsInum != 4026531840 {
		t.Errorf("NetnsInum = %d, want 4026531840", pd.NetnsInum)
	}
	if pd.MemcgCSSAddr != 0xdeadbeef {
		t.Errorf("MemcgCSSAddr = %#x, want 0xdeadbeef", pd.MemcgCSSAddr)
	}
	if diff := cmp.Diff(saddr, pd.TCPSaddr); diff != "" {
		t.Errorf("TCPSaddr (-want +got):\n%s", diff)
	}
}

func TestReadNetRetransmitPerfEventIPv6(t *testing.T) {
	var saddr, daddr [16]byte
	// fd00::1 / fd00::2 (ULA)
	saddr[0] = 0xfd
	saddr[15] = 0x01
	daddr[0] = 0xfd
	daddr[15] = 0x02

	buf := buildRetransmitBuf(
		2_000_000, 0, 0, 0, 0,
		0, 0, 0, 0, afINET6, 1,
		saddr, daddr, "", "-")

	pd, err := readNetRetransmitPerfEvent(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	family, s, d := pd.addrs()
	if family != "ipv6" || s != "fd00::1" || d != "fd00::2" {
		t.Errorf("addrs() = (%q,%q,%q), want (ipv6,fd00::1,fd00::2)", family, s, d)
	}
}

func TestReadNetRetransmitPerfEventTruncated(t *testing.T) {
	if _, err := readNetRetransmitPerfEvent(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("expected error decoding truncated buffer, got nil")
	}
}
