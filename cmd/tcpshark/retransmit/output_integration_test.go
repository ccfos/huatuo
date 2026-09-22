//go:build integration

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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestSocketWriterPreservesCorrelationResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socketPath := t.TempDir() + "/events.sock"
	server, err := toolstream.NewServer(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *types.TCPRetransmitTracing, 1)
	toolstream.Register(server, "tcpshark", func(_ *toolstream.Session, event *types.TCPRetransmitTracing) error {
		select {
		case received <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	sink, closeSink, err := newWriter(nil, &writerOptions{socketPath: socketPath, toolName: "tcpshark", version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeSink(); err != nil {
			t.Error(err)
		}
	})
	for _, event := range []*types.TCPRetransmitTracing{
		{
			CorrelationReason: types.CorrelationWarmup, DropLocation: "unknown",
			NetNamespace:   true,
			DropPerfStatus: &types.DropwatchStatus{HasMapCounters: true, PerfLost: 2, LostSamples: 3, RateLimited: 4},
		},
		{
			CorrelationReason: types.CorrelationInterrupted, DropLocation: "unknown",
			DropPerfStatus: &types.DropwatchStatus{LostSamples: 3},
		},
		{CorrelationReason: types.CorrelationWaitTimeout, DropLocation: "unknown"},
		{CorrelationReason: types.CorrelationQueueFull, DropLocation: "unknown"},
		{CorrelationReason: types.CorrelationUnsupported, DropLocation: "unknown"},
		{
			CorrelationReason: types.CorrelationMatched, DropLocation: "software",
			NetNamespace: true,
			DropSource:   "software", DropReason: "SKB_DROP_REASON_TCP_CSUM",
		},
		{
			CorrelationReason: types.CorrelationMatched, DropLocation: "hardware",
			NetNamespace: true,
			DropSource:   "hardware", DropReason: "ingress_vlan_filter", DropReasonGroup: "l2_drops",
		},
		{
			CorrelationReason: types.CorrelationMatched, DropLocation: "unknown", DropSource: "unknown",
			NetNamespace: true,
		},
	} {
		if err := sink.Write(event); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-received:
			if diff := cmp.Diff(event, got); diff != "" {
				t.Fatalf("socket event differs (-want +got):\n%s", diff)
			}
		case <-ctx.Done():
			t.Fatal("timed out receiving drop metadata through toolstream")
		}
	}
}
