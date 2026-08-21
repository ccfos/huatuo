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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/mdlayher/netlink/nlenc"
	"golang.org/x/sys/unix"
)

func TestDecodeNetlinkMessageStats2(t *testing.T) {
	softwareBasic := basicStats(1_024, 767_358_010)
	softwarePackets := uint64Stats(9_357_292_602)
	hardwareBasic := basicStats(512, 123)
	hardwarePackets := uint64Stats(4_000_000_000)
	queue := queueStats(1, 2, 3, 4, 5)

	stats2 := marshalAttributes(t, []netlink.Attribute{
		{Type: tcaStatsBasic, Data: softwareBasic},
		{Type: tcaStatsPacket64, Data: softwarePackets},
		{Type: tcaStatsBasicHardware, Data: hardwareBasic},
		{Type: tcaStatsPacket64, Data: hardwarePackets},
		{Type: tcaStatsQueue, Data: queue},
	})
	message := qdiscMessage(t, 1, math.MaxUint32, []netlink.Attribute{
		{Type: unix.TCA_KIND, Data: []byte("mq\x00")},
		{Type: unix.TCA_STATS2, Data: stats2},
	})

	got, err := decodeMessage(message, map[int]string{1: "eth0"})
	if err != nil {
		t.Fatalf("decode qdisc message: %v", err)
	}
	want := Stats{
		Netdev:       "eth0",
		Parent:       math.MaxUint32,
		Kind:         "mq",
		Bytes:        1_024,
		Packets:      9_357_292_602,
		Drops:        3,
		Requeues:     4,
		Overlimits:   5,
		QueueLength:  1,
		BacklogBytes: 2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("qdisc statistics mismatch (-want +got):\n%s", diff)
	}
	if !got.IsRoot() {
		t.Fatal("root qdisc was not identified as root")
	}
}

func TestDecodeNetlinkMessageFallsBackToBasicPackets(t *testing.T) {
	stats2 := marshalAttributes(t, []netlink.Attribute{
		{Type: tcaStatsBasic, Data: basicStats(100, 200)},
	})
	message := qdiscMessage(t, 0, 0, []netlink.Attribute{
		{Type: unix.TCA_STATS2, Data: stats2},
	})

	got, err := decodeMessage(message, nil)
	if err != nil {
		t.Fatalf("decode qdisc message: %v", err)
	}
	if got.Packets != 200 {
		t.Fatalf("packets = %d, want 200", got.Packets)
	}
	if got.IsRoot() {
		t.Fatal("qdisc with parent 0 was identified as root")
	}
}

func TestDecodeNetlinkMessageLegacyStats(t *testing.T) {
	legacy := legacyStats(1<<32+2, 3, 4, 5, 6, 7)
	message := qdiscMessage(t, 1, 0, []netlink.Attribute{
		{Type: unix.TCA_KIND, Data: []byte("fq_codel\x00")},
		{Type: unix.TCA_STATS, Data: legacy},
	})

	got, err := decodeMessage(message, map[int]string{1: "eth0"})
	if err != nil {
		t.Fatalf("decode qdisc message: %v", err)
	}
	want := Stats{
		Netdev:       "eth0",
		Kind:         "fq_codel",
		Bytes:        1<<32 + 2,
		Packets:      3,
		Drops:        4,
		Overlimits:   5,
		QueueLength:  6,
		BacklogBytes: 7,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("legacy qdisc statistics mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeNetlinkMessagePrefersStats2(t *testing.T) {
	legacy := legacyStats(1, 2, 3, 4, 5, 6)
	stats2 := marshalAttributes(t, []netlink.Attribute{
		{Type: tcaStatsBasic, Data: basicStats(10, 20)},
		{Type: tcaStatsQueue, Data: queueStats(30, 40, 50, 60, 70)},
	})

	tests := []struct {
		name       string
		attributes []netlink.Attribute
	}{
		{
			name: "legacy before stats2",
			attributes: []netlink.Attribute{
				{Type: unix.TCA_STATS, Data: legacy},
				{Type: unix.TCA_STATS2, Data: stats2},
			},
		},
		{
			name: "stats2 before legacy",
			attributes: []netlink.Attribute{
				{Type: unix.TCA_STATS2, Data: stats2},
				{Type: unix.TCA_STATS, Data: legacy},
			},
		},
	}

	want := Stats{
		Bytes:        10,
		Packets:      20,
		Drops:        50,
		Requeues:     60,
		Overlimits:   70,
		QueueLength:  30,
		BacklogBytes: 40,
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeMessage(qdiscMessage(t, 0, 0, tt.attributes), nil)
			if err != nil {
				t.Fatalf("decode qdisc message: %v", err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("qdisc statistics mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeNetlinkMessageErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{
			name:    "short message",
			data:    make([]byte, tcMessageLen-1),
			wantErr: "qdisc message length is 19 bytes",
		},
		{
			name:    "short attribute header",
			data:    append(make([]byte, tcMessageLen), make([]byte, 3)...),
			wantErr: "netlink attribute has 3 bytes",
		},
		{
			name:    "attribute shorter than header",
			data:    append(make([]byte, tcMessageLen), 3, 0, 1, 0),
			wantErr: "netlink attribute length is 3 bytes",
		},
		{
			name:    "attribute exceeds message",
			data:    append(make([]byte, tcMessageLen), 8, 0, 1, 0),
			wantErr: "only 4 bytes remain",
		},
		{
			name: "short legacy statistics",
			data: qdiscMessage(t, 0, 0, []netlink.Attribute{
				{Type: unix.TCA_STATS, Data: make([]byte, 35)},
			}),
			wantErr: "legacy qdisc statistics length is 35 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeMessage(tt.data, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeStats2Errors(t *testing.T) {
	tests := []struct {
		name          string
		attributeType uint16
		data          []byte
		wantErr       string
	}{
		{
			name:          "short basic statistics",
			attributeType: tcaStatsBasic,
			data:          make([]byte, 11),
			wantErr:       "basic statistics length is 11 bytes",
		},
		{
			name:          "short queue statistics",
			attributeType: tcaStatsQueue,
			data:          make([]byte, 19),
			wantErr:       "queue statistics length is 19 bytes",
		},
		{
			name:          "short 64-bit packet statistics",
			attributeType: tcaStatsPacket64,
			data:          make([]byte, 7),
			wantErr:       "64-bit packet statistics length is 7 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attributes := marshalAttributes(t, []netlink.Attribute{
				{Type: tt.attributeType, Data: tt.data},
			})
			_, err := decodeStats2(attributes)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func qdiscMessage(
	t testing.TB,
	ifindex uint32,
	parent uint32,
	attributes []netlink.Attribute,
) []byte {
	t.Helper()

	encoded := marshalAttributes(t, attributes)
	data := make([]byte, tcMessageLen+len(encoded))
	nlenc.PutUint32(data[4:8], ifindex)
	nlenc.PutUint32(data[12:16], parent)
	copy(data[tcMessageLen:], encoded)
	return data
}

func marshalAttributes(t testing.TB, attributes []netlink.Attribute) []byte {
	t.Helper()

	encoded, err := netlink.MarshalAttributes(attributes)
	if err != nil {
		t.Fatalf("marshal netlink attributes: %v", err)
	}
	return encoded
}

func basicStats(bytes uint64, packets uint32) []byte {
	data := make([]byte, 12)
	nlenc.PutUint64(data[0:8], bytes)
	nlenc.PutUint32(data[8:12], packets)
	return data
}

func uint64Stats(value uint64) []byte {
	data := make([]byte, 8)
	nlenc.PutUint64(data, value)
	return data
}

func queueStats(
	queueLength uint32,
	backlogBytes uint32,
	drops uint32,
	requeues uint32,
	overlimits uint32,
) []byte {
	data := make([]byte, 20)
	nlenc.PutUint32(data[0:4], queueLength)
	nlenc.PutUint32(data[4:8], backlogBytes)
	nlenc.PutUint32(data[8:12], drops)
	nlenc.PutUint32(data[12:16], requeues)
	nlenc.PutUint32(data[16:20], overlimits)
	return data
}

func legacyStats(
	bytes uint64,
	packets uint32,
	drops uint32,
	overlimits uint32,
	queueLength uint32,
	backlogBytes uint32,
) []byte {
	data := make([]byte, 36)
	nlenc.PutUint64(data[0:8], bytes)
	nlenc.PutUint32(data[8:12], packets)
	nlenc.PutUint32(data[12:16], drops)
	nlenc.PutUint32(data[16:20], overlimits)
	nlenc.PutUint32(data[28:32], queueLength)
	nlenc.PutUint32(data[32:36], backlogBytes)
	return data
}
