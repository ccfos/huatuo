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

package qdisc

import (
	"math"
	"testing"

	"github.com/mdlayher/netlink"
	"github.com/mdlayher/netlink/nlenc"
)

func TestParseMessageUsesPacket64(t *testing.T) {
	const wantPackets uint64 = 9_357_292_602
	const basicPackets uint32 = 767_358_010

	basic := make([]byte, 12)
	nlenc.PutUint64(basic[0:8], 1_024)
	nlenc.PutUint32(basic[8:12], basicPackets)
	packet64 := make([]byte, 8)
	nlenc.PutUint64(packet64, wantPackets)

	hardwareBasic := make([]byte, 12)
	nlenc.PutUint64(hardwareBasic[0:8], 512)
	nlenc.PutUint32(hardwareBasic[8:12], 123)
	hardwarePackets := make([]byte, 8)
	nlenc.PutUint64(hardwarePackets, 4_000_000_000)

	stats, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStatsBasic, Data: basic},
		{Type: tcaStatsPacket64, Data: packet64},
		{Type: tcaStatsBasicHardware, Data: hardwareBasic},
		{Type: tcaStatsPacket64, Data: hardwarePackets},
	})
	if err != nil {
		t.Fatalf("marshal qdisc statistics: %v", err)
	}
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaKind, Data: []byte("mq\x00")},
		{Type: tcaStats2, Data: stats},
	})
	if err != nil {
		t.Fatalf("marshal qdisc attributes: %v", err)
	}

	data := make([]byte, 20+len(attrs))
	nlenc.PutUint32(data[4:8], 1)
	copy(data[20:], attrs)

	info, err := parseMessage(netlink.Message{Data: data}, map[int]string{1: "eth0"})
	if err != nil {
		t.Fatalf("parse qdisc message: %v", err)
	}
	if info.Packets != wantPackets {
		t.Fatalf("packets = %d, want %d", info.Packets, wantPackets)
	}
}

func TestParseMessageFallsBackToBasicPackets(t *testing.T) {
	const wantPackets uint32 = 767_358_010

	basic := make([]byte, 12)
	nlenc.PutUint64(basic[0:8], 1_024)
	nlenc.PutUint32(basic[8:12], wantPackets)
	stats, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStatsBasic, Data: basic},
	})
	if err != nil {
		t.Fatalf("marshal qdisc statistics: %v", err)
	}
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStats2, Data: stats},
	})
	if err != nil {
		t.Fatalf("marshal qdisc attributes: %v", err)
	}

	data := make([]byte, 20+len(attrs))
	copy(data[20:], attrs)

	info, err := parseMessage(netlink.Message{Data: data}, nil)
	if err != nil {
		t.Fatalf("parse qdisc message: %v", err)
	}
	if info.Packets != uint64(wantPackets) {
		t.Fatalf("packets = %d, want %d", info.Packets, wantPackets)
	}
}

func TestParseMessageNormalizesRootParent(t *testing.T) {
	data := make([]byte, 20)
	nlenc.PutUint32(data[12:16], math.MaxUint32)

	info, err := parseMessage(netlink.Message{Data: data}, nil)
	if err != nil {
		t.Fatalf("parse qdisc message: %v", err)
	}
	if info.Parent != 0 {
		t.Fatalf("parent = %#x, want 0", info.Parent)
	}
}

func TestParseMessageLegacyStats(t *testing.T) {
	legacy := make([]byte, 36)
	nlenc.PutUint64(legacy[0:8], 1<<32+2)
	nlenc.PutUint32(legacy[8:12], 3)
	nlenc.PutUint32(legacy[12:16], 4)
	nlenc.PutUint32(legacy[16:20], 5)
	nlenc.PutUint32(legacy[28:32], 6)
	nlenc.PutUint32(legacy[32:36], 7)

	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaKind, Data: []byte("fq_codel\x00")},
		{Type: tcaStats, Data: legacy},
	})
	if err != nil {
		t.Fatalf("marshal qdisc attributes: %v", err)
	}

	data := make([]byte, 20+len(attrs))
	nlenc.PutUint32(data[4:8], 1)
	copy(data[20:], attrs)

	info, err := parseMessage(netlink.Message{Data: data}, map[int]string{1: "eth0"})
	if err != nil {
		t.Fatalf("parse qdisc message: %v", err)
	}
	if info.IfaceName != "eth0" || info.Kind != "fq_codel" {
		t.Fatalf("identity = %q/%q, want eth0/fq_codel", info.IfaceName, info.Kind)
	}
	if info.Bytes != 1<<32+2 || info.Packets != 3 || info.Drops != 4 ||
		info.Overlimits != 5 || info.Qlen != 6 || info.Backlog != 7 {
		t.Fatalf("legacy stats = %+v", info)
	}
	if info.Requeues != 0 {
		t.Fatalf("legacy requeues = %d, want 0", info.Requeues)
	}
}

func TestParseMessageStats2QueueFields(t *testing.T) {
	basic := make([]byte, 12)
	nlenc.PutUint64(basic[0:8], 100)
	nlenc.PutUint32(basic[8:12], 200)
	queue := make([]byte, 20)
	for i, value := range []uint32{1, 2, 3, 4, 5} {
		nlenc.PutUint32(queue[i*4:(i+1)*4], value)
	}

	stats, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStatsBasic, Data: basic},
		{Type: tcaStatsQueue, Data: queue},
	})
	if err != nil {
		t.Fatalf("marshal qdisc statistics: %v", err)
	}
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStats2, Data: stats},
	})
	if err != nil {
		t.Fatalf("marshal qdisc attributes: %v", err)
	}

	data := make([]byte, 20+len(attrs))
	copy(data[20:], attrs)
	info, err := parseMessage(netlink.Message{Data: data}, nil)
	if err != nil {
		t.Fatalf("parse qdisc message: %v", err)
	}
	if info.Bytes != 100 || info.Packets != 200 || info.Qlen != 1 ||
		info.Backlog != 2 || info.Drops != 3 || info.Requeues != 4 ||
		info.Overlimits != 5 {
		t.Fatalf("stats2 = %+v", info)
	}
}

func TestParseMessageRejectsShortMessage(t *testing.T) {
	_, err := parseMessage(netlink.Message{Data: make([]byte, 19)}, nil)
	if err == nil {
		t.Fatal("parseMessage() error = nil, want short message error")
	}
}

func TestParseStats2RejectsShortQueue(t *testing.T) {
	stats, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStatsQueue, Data: make([]byte, 19)},
	})
	if err != nil {
		t.Fatalf("marshal qdisc statistics: %v", err)
	}
	if _, err := parseStats2(stats); err == nil {
		t.Fatal("parseStats2() error = nil, want short queue error")
	}
}
