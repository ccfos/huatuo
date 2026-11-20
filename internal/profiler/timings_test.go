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

package profiler

import (
	"testing"
	"time"
)

func TestNewTimingsUsesRecordedStageBoundaries(t *testing.T) {
	const name = "timings-test"
	SampleSerializeTimeStore.Delete(name)
	SymbolizeToPprofTimeStore.Delete(name)
	t.Cleanup(func() {
		SampleSerializeTimeStore.Delete(name)
		SymbolizeToPprofTimeStore.Delete(name)
	})

	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	serialize := start.Add(10 * time.Millisecond)
	symbolize := start.Add(30 * time.Millisecond)
	toPprof := start.Add(40 * time.Millisecond)
	end := start.Add(70 * time.Millisecond)
	SetSampleSerializeTimeStamp(name, serialize)
	SetSymbolizeToPprofTimeStamp(name, toPprof)

	got := NewTimings(name, start, symbolize, end)
	if got.SampleCollectMs != 10 || got.SampleSerializeMs != 20 || got.SymbolizeToTreeMs != 10 || got.SymbolizeToPprofMs != 30 {
		t.Fatalf("NewTimings() = %#v, want stage durations 10/20/10/30 ms", got)
	}
}

func TestNewTimingsFallsBackForMissingOrInvalidBoundaries(t *testing.T) {
	const name = "timings-fallback-test"
	SampleSerializeTimeStore.Delete(name)
	SymbolizeToPprofTimeStore.Delete(name)
	t.Cleanup(func() {
		SampleSerializeTimeStore.Delete(name)
		SymbolizeToPprofTimeStore.Delete(name)
	})

	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	symbolize := start.Add(30 * time.Millisecond)
	end := start.Add(70 * time.Millisecond)
	SetSampleSerializeTimeStamp(name, start.Add(-time.Millisecond))
	SetSymbolizeToPprofTimeStamp(name, symbolize.Add(-time.Millisecond))

	got := NewTimings(name, start, symbolize, end)
	if got.SampleCollectMs != 30 || got.SampleSerializeMs != 0 || got.SymbolizeToTreeMs != 40 || got.SymbolizeToPprofMs != 0 {
		t.Fatalf("fallback NewTimings() = %#v, want stage durations 30/0/40/0 ms", got)
	}
}
