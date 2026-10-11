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
	"encoding/json"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestToolEventsPersistSeparateObservationTimes(t *testing.T) {
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if err := tracing.EnableDocumentWriter(store, document.New("test")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracing.DisableDocumentWriter)
	events, cancel := store.Subscribe()
	defer cancel()
	const observed = "2026-09-15T02:00:01Z"
	const kernel = "2026-09-15T02:00:00Z"
	tests := []struct {
		name string
		send func(string) error
	}{
		{name: "tcp retransmit", send: func(timestamp string) error {
			var event types.TCPRetransmitTracing
			if err := json.Unmarshal([]byte(`{"observed_timestamp":"`+observed+`","kernel_observed_timestamp":"`+timestamp+`"}`), &event); err != nil {
				return err
			}
			event.KernelObservedNS = 12345
			return handleTCPRetransmitEvent(nil, &event)
		}},
		{name: "dropwatch", send: func(timestamp string) error {
			var event types.DropWatchTracing
			if err := json.Unmarshal([]byte(`{"observed_timestamp":"`+observed+`","kernel_observed_timestamp":"`+timestamp+`"}`), &event); err != nil {
				return err
			}
			return handleDropwatchEvent(nil, &event)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.send(kernel); err != nil {
				t.Fatal(err)
			}
			select {
			case doc := <-events:
				if doc.KernelObservedTimestamp == nil || doc.KernelObservedTimestamp.Format(time.RFC3339Nano) != kernel {
					t.Fatalf("kernel timestamp = %v", doc.KernelObservedTimestamp)
				}
				if doc.ObservedTimestamp == nil || doc.ObservedTimestamp.Format(time.RFC3339Nano) != observed {
					t.Fatalf("userspace timestamp = %v", doc.ObservedTimestamp)
				}
				data, err := json.Marshal(doc.TracerData)
				if err != nil {
					t.Fatal(err)
				}
				var raw map[string]any
				if err := json.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"kernel_observed_ns", "kernel_observed_timestamp", "observed_timestamp"} {
					if _, ok := raw[field]; ok {
						t.Fatalf("tracer_data contains metadata field %q", field)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("document was not published")
			}
			if err := tt.send("invalid"); err == nil {
				t.Fatal("malformed kernel UTC timestamp accepted")
			}
		})
	}
}

func TestRASKernelObservationTime(t *testing.T) {
	monotonicNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatal(err)
	}
	event := &rasEvent{KernelObservedNS: monotonicNS}
	before := time.Now().UTC()
	data, err := newRasTracingData(event, "CPU", "MCE", "test", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if delta := before.Sub(data.kernelObservedTimestamp.Time); delta < -time.Second || delta > time.Second {
		t.Fatalf("converted RAS time differs by %v", delta)
	}
}

func TestToolEventsRequireObservationTime(t *testing.T) {
	if err := handleDropwatchEvent(nil, &types.DropWatchTracing{}); err == nil {
		t.Fatal("dropwatch event without observation time accepted")
	}
	if err := handleTCPRetransmitEvent(nil, &types.TCPRetransmitTracing{}); err == nil {
		t.Fatal("TCP retransmit event without observation time accepted")
	}
}
