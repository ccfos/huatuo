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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

// errWriter always fails Write with the configured error. Used to verify that
// the dropwatch writers propagate IO errors instead of swallowing them.
type errWriter struct{ err error }

func (w errWriter) Write(_ []byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestTextWriterFormatsAllEventFields(t *testing.T) {
	var output bytes.Buffer
	w := &textWriter{w: &output}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 8, 4, 1, 2, 3, 456789000, time.UTC)},
		DropSource:        dropwatch.SourceSoftware,
		DropReason:        "SKB_DROP_REASON_TCP_CSUM",
		DropLocation:      "0xffffffff81000000",
		Source:            "tools",
		Comm:              "worker thread",
		PID:               1420,
		NetdevName:        "eth0",
		PacketSkbAddr:     "0xffff888012345678",
		PacketLenBytes:    1500,
		Layers: &packet.Packet{
			Label: "IPv4/TCP",
			IPv4: &packet.IPv4{
				Saddr: net.IPv4(10, 0, 0, 1),
				Daddr: net.IPv4(10, 0, 0, 2),
			},
			TCP: &packet.TCP{
				Sport:    12345,
				Dport:    443,
				Seq:      123,
				AckSeq:   456,
				Flags:    "ACK|PSH",
				RawFlags: packet.TCPFlagACK | packet.TCPFlagPSH,
				Window:   4096,
				SkState:  "ESTABLISHED",
			},
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := "2026-08-04T01:02:03.456789000Z " +
		"IPv4/TCP 10.0.0.1:12345 > 10.0.0.2:443 [ACK|PSH] seq=123 ack=456 win=4096 sk=ESTABLISHED " +
		"reason=SKB_DROP_REASON_TCP_CSUM drop_source=software drop_location=0xffffffff81000000 " +
		"len=1500 dev=eth0 pid=1420[worker thread] " +
		"addr=0xffff888012345678 source=tools\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestFormatHardwareEvent(t *testing.T) {
	ev := abi.DropwatchPacketEvent{}
	ev.Meta.DropSource = uint32(abi.DropwatchDropSourceHardware)
	ev.Meta.SKBAddr = 0xffff888012345678
	copy(ev.Meta.TrapName[:], "ingress_vlan_filter")
	copy(ev.Meta.TrapGroupName[:], "l2_drops")

	got, err := formatEvent(&ev, nil, "tools")
	if err != nil {
		t.Fatal(err)
	}
	if got.DropSource != dropwatch.SourceHardware {
		t.Errorf("DropSource = %q, want %q", got.DropSource, dropwatch.SourceHardware)
	}
	if got.DropReason != "ingress_vlan_filter" {
		t.Errorf("DropReason = %q, want ingress_vlan_filter", got.DropReason)
	}
	if got.DropReasonGroup != "l2_drops" {
		t.Errorf("DropReasonGroup = %q, want l2_drops", got.DropReasonGroup)
	}
	if got.PacketSkbAddr != "0xffff888012345678" {
		t.Errorf("PacketSkbAddr = %q, want 0xffff888012345678", got.PacketSkbAddr)
	}
	if got.DropLocation != "" {
		t.Errorf("DropLocation = %q, want empty", got.DropLocation)
	}
}

func TestJSONWriterObservationTimestamps(t *testing.T) {
	const kernelObservedNS uint64 = 12_345_678_901_234_567
	kernelObservedTimestamp, err := timeutil.KtimeToTimestamp(kernelObservedNS)
	if err != nil {
		t.Fatal(err)
	}

	for _, source := range []abi.DropwatchDropSource{
		abi.DropwatchDropSourceSoftware,
		abi.DropwatchDropSourceHardware,
	} {
		t.Run(dropwatch.ResolveMetadata(&abi.DropwatchPacketMeta{DropSource: uint32(source)}, nil).Source, func(t *testing.T) {
			var record abi.DropwatchPacketEvent
			record.Meta.KernelObservedNS = kernelObservedNS
			record.Meta.DropSource = uint32(source)
			var output bytes.Buffer
			event, err := formatEvent(&record, nil, "tools")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&jsonWriter{w: &output}).Write(event); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["kernel_observed_timestamp"] != kernelObservedTimestamp.FormatUTC() {
				t.Fatalf("kernel_observed_timestamp = %v, want %s", got["kernel_observed_timestamp"], kernelObservedTimestamp.FormatUTC())
			}
			if got["observed_timestamp"] != event.ObservedTimestamp.FormatUTC() {
				t.Fatalf("observed_timestamp = %v, want %s", got["observed_timestamp"], event.ObservedTimestamp.FormatUTC())
			}
			if _, exists := got["kernel_observed_ns"]; exists {
				t.Error("internal clock kernel_observed_ns leaked into JSON")
			}
		})
	}
}

func TestTextWriterCombinesHardwareReasonGroup(t *testing.T) {
	var output bytes.Buffer
	w := &textWriter{w: &output}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
		DropSource:        dropwatch.SourceHardware,
		DropReason:        "ingress_vlan_filter",
		DropReasonGroup:   "l2_drops",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("reason=l2_drops/ingress_vlan_filter")) {
		t.Fatalf("output = %q, want combined hardware reason", output.String())
	}
}

func TestTextWriterPropagatesIOError(t *testing.T) {
	boom := errors.New("boom")
	w := &textWriter{w: errWriter{err: boom}}

	err := w.Write(&types.DropWatchTracing{
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
		NetdevName:        "eth0",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want %v", err, boom)
	}
}

func TestJSONWriterPropagatesIOError(t *testing.T) {
	boom := errors.New("boom")
	w := &jsonWriter{w: errWriter{err: boom}}

	err := w.Write(&types.DropWatchTracing{ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want %v", err, boom)
	}
}

func TestWritersRejectShortWrites(t *testing.T) {
	t.Parallel()

	event := &types.DropWatchTracing{ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}}
	if err := (&textWriter{w: shortWriter{}}).Write(event); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("text writer error = %v, want %v", err, io.ErrShortWrite)
	}
	if err := (&jsonWriter{w: shortWriter{}}).Write(event); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("JSON writer error = %v, want %v", err, io.ErrShortWrite)
	}
}

func BenchmarkTextWriter(b *testing.B) {
	event := &types.DropWatchTracing{
		ObservedTimestamp:       timeutil.Timestamp{Time: time.Date(2026, 8, 4, 1, 2, 3, 456789000, time.UTC)},
		KernelObservedTimestamp: &timeutil.Timestamp{Time: time.Date(2026, 8, 4, 1, 2, 3, 456000000, time.UTC)},
		DropSource:              dropwatch.SourceHardware,
		DropReason:              "ingress_vlan_filter",
		DropReasonGroup:         "l2_drops",
		PacketLenBytes:          1500,
		NetdevName:              "eth0",
	}
	w := &textWriter{w: io.Discard}

	b.ReportAllocs()
	for b.Loop() {
		if err := w.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONWriter(b *testing.B) {
	var record abi.DropwatchPacketEvent
	record.Meta.KernelObservedNS = uint64(time.Second)
	record.Meta.DropSource = uint32(abi.DropwatchDropSourceHardware)
	copy(record.Meta.TrapName[:], "ingress_vlan_filter")
	copy(record.Meta.TrapGroupName[:], "l2_drops")
	event, err := formatEvent(&record, nil, "tools")
	if err != nil {
		b.Fatal(err)
	}
	w := &jsonWriter{w: io.Discard}

	b.ReportAllocs()
	for b.Loop() {
		if err := w.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}

func TestKernelObservationUsesEventTime(t *testing.T) {
	monotonicNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatal(err)
	}
	if monotonicNS < uint64(time.Second) {
		t.Skip("host has been up for less than one second")
	}
	record := abi.DropwatchPacketEvent{}
	record.Meta.KernelObservedNS = monotonicNS - uint64(time.Second)
	event, err := formatEvent(&record, nil, "tools")
	if err != nil {
		t.Fatal(err)
	}
	if event.KernelObservedTimestamp == nil {
		t.Fatal("kernel observation timestamp is missing")
	}
	kernel := event.KernelObservedTimestamp.Time
	observed := event.ObservedTimestamp
	age := observed.Sub(kernel)
	if age < 900*time.Millisecond || age > 2*time.Second {
		t.Fatalf("kernel-to-userspace delay = %v, expected about one second", age)
	}
}

func TestFormatSoftwareReason(t *testing.T) {
	record := abi.DropwatchPacketEvent{}
	record.Meta.DropSource = uint32(abi.DropwatchDropSourceSoftware)
	record.Meta.DropReason = 5
	names := dropwatch.ReasonNames{5: "SKB_DROP_REASON_TCP_CSUM"}
	got, err := formatEvent(&record, names, "tools")
	if err != nil {
		t.Fatal(err)
	}
	if got.DropSource != "software" || got.DropReason != "SKB_DROP_REASON_TCP_CSUM" || got.DropReasonGroup != "" {
		t.Fatalf("software drop = %+v", got)
	}
}
