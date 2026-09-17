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

package document

import (
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestBuilderBuildsSharedMetadata(t *testing.T) {
	startedTimestamp := time.Date(2026, 8, 28, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	builder := &Builder{region: "cn-north", hostname: "node-1"}
	document, err := builder.Build(&Input{
		TracerName:       "profiler",
		TracerID:         "job-1",
		StartedTimestamp: timeutil.Timestamp{Time: startedTimestamp},
		TracerRunType:    types.TracerRunTypeProfiling,
	})
	if err != nil {
		t.Fatalf("Builder.Build() error = %v", err)
	}
	if document.Hostname != "node-1" || document.Region != "cn-north" {
		t.Fatalf("node metadata = (%q, %q)", document.Hostname, document.Region)
	}
	if document.TracerID != "job-1" || document.TracerName != "profiler" {
		t.Fatalf("tracer metadata = (%q, %q)", document.TracerID, document.TracerName)
	}
	if document.StartedTimestamp == nil {
		t.Fatal("document started timestamp is nil")
	}
	if !document.StartedTimestamp.Equal(startedTimestamp) {
		t.Fatalf("document started timestamp = %v, want %v", document.StartedTimestamp, startedTimestamp)
	}
	if got := document.StartedTimestamp.FormatUTC(); got != "2026-08-28T02:30:00.000000000Z" {
		t.Fatalf("started timestamp output = %q", got)
	}
	if !document.UploadedTimestamp.IsZero() {
		t.Fatalf("document uploaded timestamp = %v, want zero", document.UploadedTimestamp)
	}
}

func TestNilBuilderIsRejected(t *testing.T) {
	if _, err := (*Builder)(nil).Build(&Input{}); err == nil {
		t.Fatal("Builder.Build() error = nil")
	}
}

func TestBuilderKeepsObservationClocksSeparate(t *testing.T) {
	observed := time.Date(2026, 9, 15, 10, 0, 1, 0, time.FixedZone("CST", 8*60*60))
	kernel := observed.Add(-time.Second)
	builder := &Builder{region: "test", hostname: "node"}
	for _, available := range []bool{false, true} {
		name := "without kernel observation"
		if available {
			name = "with kernel observation"
		}
		t.Run(name, func(t *testing.T) {
			input := &Input{ObservedTimestamp: timeutil.Timestamp{Time: observed}, TracerRunType: types.TracerRunTypeEvent}
			if available {
				input.KernelObservedTimestamp = timeutil.Timestamp{Time: kernel}
			}
			got, err := builder.Build(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.ObservedTimestamp == nil || !got.ObservedTimestamp.Equal(observed) {
				t.Fatalf("userspace timestamp = %v", got.ObservedTimestamp)
			}
			if output := got.ObservedTimestamp.FormatUTC(); output != "2026-09-15T02:00:01.000000000Z" {
				t.Fatalf("userspace timestamp output = %q", output)
			}
			if !available {
				if got.KernelObservedTimestamp != nil {
					t.Fatal("missing kernel timestamp was synthesized")
				}
				return
			}
			if got.KernelObservedTimestamp == nil || !got.KernelObservedTimestamp.Equal(kernel) {
				t.Fatalf("kernel timestamp = %v", got.KernelObservedTimestamp)
			}
			if output := got.KernelObservedTimestamp.FormatUTC(); output != "2026-09-15T02:00:00.000000000Z" {
				t.Fatalf("kernel timestamp output = %q", output)
			}
		})
	}
}
