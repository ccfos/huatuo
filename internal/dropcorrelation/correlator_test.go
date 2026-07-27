// Copyright 2026 The HuaTuo Authors
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

package dropcorrelation

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCorrelatorMatchesHardwareRXDrop(t *testing.T) {
	correlator := New(Options{Window: 10 * time.Second})
	start := time.Unix(1_000, 0)
	event := KernelDrop{
		Timestamp: start.Add(4 * time.Second),
		Device:    "eth0",
		Direction: DirectionRX,
		Reason:    "SKB_DROP_REASON_NOMEM",
		Stack:     "__netif_receive_skb\nip_rcv",
		Protocol:  "ipv4",
	}
	if !correlator.ObserveKernel(event) {
		t.Fatal("ObserveKernel rejected valid event")
	}
	incidents := correlator.ObserveHardware(HardwareSample{
		Timestamp:   start.Add(10 * time.Second),
		PeriodStart: start,
		Device:      "eth0",
		Driver:      "mlx5_core",
		Delta:       map[Counter]uint64{CounterRXMissed: 3},
	})
	if len(incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(incidents))
	}
	incident := incidents[0]
	if incident.Layer != LayerHardware {
		t.Fatalf("layer = %s, want hardware", incident.Layer)
	}
	if incident.Confidence < 0.8 || incident.Confidence > 1 {
		t.Fatalf("confidence = %f", incident.Confidence)
	}
	if incident.Driver != "mlx5_core" {
		t.Fatalf("driver = %q", incident.Driver)
	}
	if incident.HardwareDelta[CounterRXMissed] != 3 {
		t.Fatalf("hardware delta = %v", incident.HardwareDelta)
	}
	snapshot := correlator.Snapshot()
	if snapshot.Pending != 0 || snapshot.Events != 1 || snapshot.Incidents != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestCorrelatorDoesNotCrossDevices(t *testing.T) {
	correlator := New(Options{Window: 10 * time.Second})
	now := time.Unix(2_000, 0)
	correlator.ObserveKernel(KernelDrop{
		Timestamp: now,
		Device:    "eth1",
		Direction: DirectionRX,
		Reason:    "driver drop",
	})
	incidents := correlator.ObserveHardware(HardwareSample{
		Timestamp:   now.Add(5 * time.Second),
		PeriodStart: now.Add(-time.Second),
		Device:      "eth0",
		Delta:       map[Counter]uint64{CounterRXErrors: 9},
	})
	if len(incidents) != 0 {
		t.Fatalf("cross-device incidents = %v", incidents)
	}
	if correlator.Snapshot().Pending != 1 {
		t.Fatal("event for another device was removed")
	}
}

func TestCorrelatorDoesNotCrossDirections(t *testing.T) {
	correlator := New(Options{Window: 10 * time.Second})
	now := time.Unix(3_000, 0)
	correlator.ObserveKernel(KernelDrop{
		Timestamp: now,
		Device:    "eth0",
		Direction: DirectionTX,
		Reason:    "qdisc_drop",
		Stack:     "sch_direct_xmit",
	})
	incidents := correlator.ObserveHardware(HardwareSample{
		Timestamp:   now.Add(time.Second),
		PeriodStart: now.Add(-time.Second),
		Device:      "eth0",
		Delta:       map[Counter]uint64{CounterRXMissed: 20},
	})
	if len(incidents) != 1 {
		t.Fatalf("incidents = %d, want finalized stack incident", len(incidents))
	}
	if incidents[0].Layer == LayerHardware {
		t.Fatalf("RX evidence incorrectly classified TX event: %+v", incidents[0])
	}
}

func TestCorrelatorCounterResetCannotProveHardwareDrop(t *testing.T) {
	correlator := New(Options{Window: 10 * time.Second})
	now := time.Unix(4_000, 0)
	correlator.ObserveKernel(KernelDrop{
		Timestamp: now,
		Device:    "eth0",
		Direction: DirectionRX,
		Reason:    "driver_rx_drop",
		Stack:     "mlx5e_poll_rx_cq",
	})
	incidents := correlator.ObserveHardware(HardwareSample{
		Timestamp:   now.Add(time.Second),
		PeriodStart: now.Add(-time.Second),
		Device:      "eth0",
		Driver:      "mlx5_core",
		Delta:       map[Counter]uint64{CounterRXMissed: 1_000_000},
		Reset:       true,
	})
	if len(incidents) != 0 {
		t.Fatalf("reset sample should not cover event: %v", incidents)
	}
	flushed := correlator.Flush(now.Add(11 * time.Second))
	if len(flushed) != 1 || flushed[0].Layer != LayerDriver {
		t.Fatalf("flushed = %+v, want driver classification", flushed)
	}
	if correlator.Snapshot().Resets != 1 {
		t.Fatal("counter reset metric not incremented")
	}
}

func TestCorrelatorFlushClassifiesProtocolStack(t *testing.T) {
	correlator := New(Options{Window: 5 * time.Second})
	now := time.Unix(5_000, 0)
	correlator.ObserveKernel(KernelDrop{
		Timestamp: now,
		Direction: DirectionUnknown,
		Reason:    "TCP_INVALID_SEQUENCE",
		Stack:     "tcp_validate_incoming\ntcp_rcv_established",
		Protocol:  "tcp",
	})
	if got := correlator.Flush(now.Add(4 * time.Second)); len(got) != 0 {
		t.Fatalf("early flush = %v", got)
	}
	got := correlator.Flush(now.Add(6 * time.Second))
	if len(got) != 1 {
		t.Fatalf("flush count = %d, want 1", len(got))
	}
	if got[0].Layer != LayerProtocolStack {
		t.Fatalf("layer = %s", got[0].Layer)
	}
	if got[0].Confidence < 0.8 {
		t.Fatalf("confidence = %f", got[0].Confidence)
	}
}

func TestCorrelatorPendingLimitForceFinalizesOldest(t *testing.T) {
	correlator := New(Options{PendingLimit: 2, Window: time.Minute})
	now := time.Unix(6_000, 0)
	for index := 0; index < 3; index++ {
		accepted := correlator.ObserveKernel(KernelDrop{
			Timestamp: now.Add(time.Duration(index) * time.Second),
			Device:    "eth0",
			Direction: DirectionRX,
			Reason:    fmt.Sprintf("reason_%d", index),
		})
		if !accepted {
			t.Fatalf("event %d rejected", index)
		}
	}
	snapshot := correlator.Snapshot()
	if snapshot.Pending != 2 || snapshot.DroppedPending != 1 || snapshot.Incidents != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(snapshot.Recent) != 1 || snapshot.Recent[0].Event.Reason != "reason_0" {
		t.Fatalf("recent = %+v", snapshot.Recent)
	}
}

func TestCorrelatorBoundsReasonCardinality(t *testing.T) {
	correlator := New(Options{ReasonLimit: 2, Window: time.Second})
	now := time.Unix(7_000, 0)
	for index := 0; index < 5; index++ {
		correlator.ObserveKernel(KernelDrop{
			Timestamp: now,
			Direction: DirectionUnknown,
			Reason:    fmt.Sprintf("UNBOUNDED_%d", index),
		})
	}
	correlator.Flush(now.Add(2 * time.Second))
	snapshot := correlator.Snapshot()
	reasons := make(map[string]uint64)
	for key, count := range snapshot.ByKey {
		reasons[key.Reason] += count
	}
	if len(reasons) != 3 {
		t.Fatalf("reasons = %v, want two specific + other", reasons)
	}
	if reasons["other"] != 3 {
		t.Fatalf("other count = %d, want 3", reasons["other"])
	}
}

func TestCorrelatorBoundsRecentIncidents(t *testing.T) {
	correlator := New(Options{IncidentLimit: 3, Window: time.Second})
	now := time.Unix(8_000, 0)
	for index := 0; index < 7; index++ {
		correlator.ObserveKernel(KernelDrop{
			Timestamp: now.Add(time.Duration(index) * time.Millisecond),
			Direction: DirectionUnknown,
			Reason:    fmt.Sprintf("reason_%d", index),
		})
	}
	correlator.Flush(now.Add(2 * time.Second))
	recent := correlator.Snapshot().Recent
	if len(recent) != 3 {
		t.Fatalf("recent count = %d, want 3", len(recent))
	}
	for index, want := range []string{"reason_4", "reason_5", "reason_6"} {
		if recent[index].Event.Reason != want {
			t.Fatalf("recent[%d] = %s, want %s", index, recent[index].Event.Reason, want)
		}
	}
}

func TestCorrelatorSnapshotIsDeepCopy(t *testing.T) {
	correlator := New(Options{Window: time.Second})
	now := time.Unix(9_000, 0)
	correlator.ObserveKernel(KernelDrop{
		Timestamp: now,
		Device:    "eth0",
		Direction: DirectionRX,
		Reason:    "drop",
	})
	correlator.ObserveHardware(HardwareSample{
		Timestamp:   now.Add(time.Second),
		PeriodStart: now,
		Device:      "eth0",
		Delta:       map[Counter]uint64{CounterRXErrors: 2},
		Sources:     map[Counter][]string{CounterRXErrors: {"ethtool:rx_errors"}},
	})

	first := correlator.Snapshot()
	first.ByKey[MetricKey{}] = 999
	first.Recent[0].Evidence[0] = "mutated"
	sample := first.LastHardwareByIface["eth0"]
	sample.Delta[CounterRXErrors] = 999
	first.LastHardwareByIface["eth0"] = sample

	second := correlator.Snapshot()
	if second.ByKey[MetricKey{}] == 999 {
		t.Fatal("ByKey was not copied")
	}
	if second.Recent[0].Evidence[0] == "mutated" {
		t.Fatal("incident evidence was not copied")
	}
	if second.LastHardwareByIface["eth0"].Delta[CounterRXErrors] == 999 {
		t.Fatal("hardware delta was not copied")
	}
}

func TestCorrelatorRecentSince(t *testing.T) {
	correlator := New(Options{Window: time.Second})
	now := time.Unix(10_000, 0)
	for index := 0; index < 3; index++ {
		eventAt := now.Add(time.Duration(index) * time.Second)
		correlator.ObserveKernel(KernelDrop{
			Timestamp: eventAt,
			Direction: DirectionUnknown,
			Reason:    fmt.Sprintf("event_%d", index),
		})
		correlator.Flush(eventAt.Add(2 * time.Second))
	}
	got := correlator.RecentSince(now.Add(3 * time.Second))
	if len(got) != 2 {
		t.Fatalf("recent since count = %d, want 2", len(got))
	}
	if got[0].FinalizedAt.After(got[1].FinalizedAt) {
		t.Fatal("RecentSince is not oldest first")
	}
}

func TestCorrelatorInvalidKernelEventsAreRejected(t *testing.T) {
	correlator := New(Options{})
	if correlator.ObserveKernel(KernelDrop{Direction: DirectionRX}) {
		t.Fatal("event without timestamp accepted")
	}
	if correlator.ObserveKernel(KernelDrop{Timestamp: time.Now()}) {
		t.Fatal("event without direction accepted")
	}
	if snapshot := correlator.Snapshot(); snapshot.Events != 0 || snapshot.Pending != 0 {
		t.Fatalf("invalid events changed snapshot: %+v", snapshot)
	}
}

func TestInferDirection(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		reason    string
		stack     string
		want      Direction
	}{
		{name: "RX stack", stack: "napi_gro_receive\n__netif_receive_skb\nip_rcv", want: DirectionRX},
		{name: "TX stack", stack: "sch_direct_xmit\ndev_hard_start_xmit", want: DirectionTX},
		{name: "ingress reason", reason: "tc_ingress_drop", want: DirectionRX},
		{name: "egress reason", reason: "tc_egress_drop", want: DirectionTX},
		{name: "conflicting evidence", stack: "netif_receive dev_queue_xmit", want: DirectionUnknown},
		{name: "no evidence", eventType: "kfree_skb", want: DirectionUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := InferDirection(test.eventType, test.reason, test.stack); got != test.want {
				t.Fatalf("direction = %s, want %s", got, test.want)
			}
		})
	}
}

func TestClassifyKernelEvidence(t *testing.T) {
	tests := []struct {
		name  string
		event KernelDrop
		layer Layer
	}{
		{
			name:  "driver stack",
			event: KernelDrop{Stack: "mlx5e_poll_rx_cq"},
			layer: LayerDriver,
		},
		{
			name:  "driver reason",
			event: KernelDrop{Reason: "xdp_drop"},
			layer: LayerDriver,
		},
		{
			name:  "protocol stack",
			event: KernelDrop{Stack: "nf_hook_slow\nip_rcv"},
			layer: LayerProtocolStack,
		},
		{
			name:  "protocol metadata",
			event: KernelDrop{Protocol: "tcp", Reason: "unknown"},
			layer: LayerProtocolStack,
		},
		{
			name:  "unknown",
			event: KernelDrop{Reason: "unknown"},
			layer: LayerUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, confidence, evidence := classifyKernelEvidence(test.event)
			if got != test.layer {
				t.Fatalf("layer = %s, want %s", got, test.layer)
			}
			if confidence <= 0 || confidence > 1 {
				t.Fatalf("confidence = %f", confidence)
			}
			if len(evidence) == 0 {
				t.Fatal("evidence is empty")
			}
		})
	}
}

func TestSnapshotSortedKeys(t *testing.T) {
	snapshot := Snapshot{ByKey: map[MetricKey]uint64{
		{Device: "z", Direction: DirectionTX, Layer: LayerDriver, Reason: "b"}:   1,
		{Device: "a", Direction: DirectionRX, Layer: LayerHardware, Reason: "c"}: 1,
		{Device: "a", Direction: DirectionRX, Layer: LayerHardware, Reason: "a"}: 1,
	}}
	got := snapshot.SortedKeys()
	want := []MetricKey{
		{Device: "a", Direction: DirectionRX, Layer: LayerHardware, Reason: "a"},
		{Device: "a", Direction: DirectionRX, Layer: LayerHardware, Reason: "c"},
		{Device: "z", Direction: DirectionTX, Layer: LayerDriver, Reason: "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %#v, want %#v", got, want)
	}
}

func TestCorrelatorConcurrentIngestAndSnapshot(t *testing.T) {
	correlator := New(Options{
		Window:        5 * time.Second,
		PendingLimit:  10_000,
		IncidentLimit: 256,
	})
	start := time.Unix(20_000, 0)
	const workers = 8
	const perWorker = 250

	var wait sync.WaitGroup
	wait.Add(workers + 2)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < perWorker; index++ {
				correlator.ObserveKernel(KernelDrop{
					Timestamp: start.Add(time.Duration(index) * time.Millisecond),
					Device:    fmt.Sprintf("eth%d", worker%2),
					Direction: DirectionRX,
					Reason:    fmt.Sprintf("reason_%d", index%10),
					Stack:     "ip_rcv",
				})
			}
		}(worker)
	}
	go func() {
		defer wait.Done()
		for index := 0; index < perWorker; index++ {
			at := start.Add(time.Duration(index+1) * time.Millisecond)
			correlator.ObserveHardware(HardwareSample{
				Timestamp:   at,
				PeriodStart: start,
				Device:      fmt.Sprintf("eth%d", index%2),
				Delta:       map[Counter]uint64{CounterRXMissed: 1},
			})
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < perWorker; index++ {
			_ = correlator.Snapshot()
			_ = correlator.RecentSince(start)
		}
	}()
	wait.Wait()
	correlator.Flush(start.Add(time.Minute))

	snapshot := correlator.Snapshot()
	if snapshot.Events != workers*perWorker {
		t.Fatalf("events = %d, want %d", snapshot.Events, workers*perWorker)
	}
	if snapshot.Pending != 0 {
		t.Fatalf("pending = %d after flush", snapshot.Pending)
	}
	if snapshot.Incidents != snapshot.Events {
		t.Fatalf("incidents = %d, events = %d", snapshot.Incidents, snapshot.Events)
	}
}

func TestConfigureDefaultReplacesInstance(t *testing.T) {
	first := ConfigureDefault(Options{PendingLimit: 1})
	if Default() != first {
		t.Fatal("Default did not return configured correlator")
	}
	second := ConfigureDefault(Options{PendingLimit: 2})
	if second == first || Default() != second {
		t.Fatal("ConfigureDefault did not atomically replace instance")
	}
}
