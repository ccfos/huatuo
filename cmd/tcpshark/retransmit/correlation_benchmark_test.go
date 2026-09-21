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

package retransmit

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/pkg/types"
)

// Each operation keeps the same flow occupancy; fixtures and initial filling
// are excluded, while removal, insertion and capacity eviction are measured.
func BenchmarkDenseFlow(b *testing.B) {
	for _, count := range []int{16, 256, 1024, dropwatchQueueCapacity} {
		for _, eviction := range []bool{false, true} {
			operation := "match"
			if eviction {
				operation = "evict"
			}
			b.Run(fmt.Sprintf("drop/%s/%d", operation, count), func(b *testing.B) {
				event := testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
				entry, ok := retransmitEntryFromEvent(event)
				if !ok {
					b.Fatal("invalid fixture")
				}
				drop := &dropEvent{
					flow: entry.flow, namespace: entry.namespace, kernelObservedNS: uint64(time.Second),
					sequence: 100, endSequence: 200, tcpFlags: packet.TCPFlagACK,
				}
				now := time.Unix(10, 0)
				correlator := eventCorrelator{
					dropStore: newStore[*dropEvent](count, dropRetentionDuration),
				}
				for range count {
					correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)
				}
				b.ReportAllocs()
				for b.Loop() {
					if !eviction {
						got, _ := correlator.matchAndRemoveDrop(&entry)
						if got != drop {
							b.Fatal("missing matching drop")
						}
					}
					correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)
				}
				if correlator.dropStore.byDeadline.Len() != count {
					b.Fatal("cache occupancy changed")
				}
			})
			if count > retransmitQueueCapacity {
				continue
			}
			b.Run(fmt.Sprintf("waiting/%s/%d", operation, count), func(b *testing.B) {
				event := testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
				entry, ok := retransmitEntryFromEvent(event)
				if !ok {
					b.Fatal("invalid fixture")
				}
				drop := &dropEvent{
					flow: entry.flow, namespace: entry.namespace, kernelObservedNS: uint64(time.Second),
					sequence: 100, endSequence: 200, tcpFlags: packet.TCPFlagACK,
				}
				now := time.Unix(10, 0)
				correlator := eventCorrelator{
					retransmitStore: newStore[waitingRetransmit](count, retransmitRetentionDuration),
				}
				for range count {
					waiting := &storeEntry[waitingRetransmit]{
						value: waitingRetransmit{event: event, matchFields: entry},
					}
					correlator.retransmitStore.add(waiting, &waiting.value.matchFields.flow, now)
				}
				b.ReportAllocs()
				for b.Loop() {
					if !eviction && correlator.matchAndRemoveRetransmit(drop) == nil {
						b.Fatal("missing waiting retransmit")
					}
					waiting := &storeEntry[waitingRetransmit]{
						value: waitingRetransmit{event: event, matchFields: entry},
					}
					evicted := correlator.retransmitStore.add(waiting, &waiting.value.matchFields.flow, now)
					if eviction && evicted == nil {
						b.Fatal("missing capacity eviction")
					}
				}
				if correlator.retransmitStore.byDeadline.Len() != count {
					b.Fatal("queue occupancy changed")
				}
			})
		}
	}
}

type benchmarkCorrelationWriter struct {
	output    writer
	delivered chan struct{}
}

func (w *benchmarkCorrelationWriter) Write(event *types.TCPRetransmitTracing) error {
	if event.DropLocation != "host_software" {
		return fmt.Errorf("expected matched output, got %q", event.DropLocation)
	}
	if err := w.output.Write(event); err != nil {
		return err
	}
	w.delivered <- struct{}{}
	return nil
}

// One operation is one pair of ABI records through both readers, owned channel
// delivery, the real correlation loop and JSON output. Kernel I/O, symbol lookup
// (empty stack), tracer startup and shutdown are outside this workload.
func BenchmarkAsyncCorrelation(b *testing.B) {
	ctx, cancel := context.WithCancel(b.Context())
	group, ctx := errgroup.WithContext(ctx)
	rawRetransmits := make(chan abi.TCPRetransmitEvent)
	rawDrops := make(chan *abi.DropwatchPacketEvent)
	retransmits := make(chan *retransmitEvent)
	group.Go(func() error {
		defer close(retransmits)
		return readRetransmitEvents(ctx, func(dst *abi.TCPRetransmitEvent) error {
			select {
			case record := <-rawRetransmits:
				*dst = record
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, retransmits)
	})
	drops := make(chan *dropEvent)
	group.Go(func() error {
		defer close(drops)
		return readDropwatchEvents(ctx, func(dst *abi.DropwatchPacketEvent) error {
			select {
			case record := <-rawDrops:
				*dst = *record
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, drops)
	})
	sink := &benchmarkCorrelationWriter{output: &jsonWriter{w: io.Discard}, delivered: make(chan struct{})}
	group.Go(func() error {
		return runRetransmitDropCorrelation(ctx, &retransmitDropSession{
			retransmitEvents: retransmits, dropwatchEvents: drops,
			readDropwatchStatus: func() (types.DropwatchStatus, error) { return types.DropwatchStatus{}, nil },
			sink:                sink, sourceType: "tools",
		})
	})
	defer func() {
		cancel()
		if err := group.Wait(); err != nil {
			b.Fatal(err)
		}
	}()
	drop := newIPv4DropwatchTCPRecord(140)
	drop.Meta.KernelObservedNS = uint64(time.Second)
	drop.Meta.NetNamespaceCookie = 1
	record := abi.TCPRetransmitEvent{
		KernelObservedNS: uint64(time.Second) + 1, NetNamespaceCookie: 1, Family: unix.AF_INET,
		Saddr: [16]byte{10, 0, 0, 1}, Daddr: [16]byte{10, 0, 0, 2},
		Sport: 12345, Dport: 80, TCPSeq: 123, TCPEndSeq: 223,
		TCPFlags: packet.TCPFlagACK, EventType: uint8(abi.TCPRetransmitEventSKB),
	}
	b.ReportAllocs()
	for b.Loop() {
		select {
		case rawDrops <- drop:
		case <-ctx.Done():
			b.Fatal("drop reader stopped")
		}
		select {
		case rawRetransmits <- record:
		case <-ctx.Done():
			b.Fatal("retransmit reader stopped")
		}
		select {
		case <-sink.delivered:
		case <-ctx.Done():
			b.Fatal("correlation stopped")
		}
	}
}
