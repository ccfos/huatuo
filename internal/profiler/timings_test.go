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

package profiler

import (
	"testing"
	"time"
)

func TestTimingStoresIgnoreUnexpectedValueType(t *testing.T) {
	const profilerName = "unexpected-timing-value"
	SampleSerializeTimeStore.Store(profilerName, "not a timestamp")
	SymbolizeToPprofTimeStore.Store(profilerName, 1)
	t.Cleanup(func() {
		SampleSerializeTimeStore.Delete(profilerName)
		SymbolizeToPprofTimeStore.Delete(profilerName)
	})

	if got := GetSampleSerializeTimeStamp(profilerName); !got.IsZero() {
		t.Fatalf("GetSampleSerializeTimeStamp() = %v, want zero time", got)
	}
	if got := GetSymbolizeToPprofTimeStamp(profilerName); !got.IsZero() {
		t.Fatalf("GetSymbolizeToPprofTimeStamp() = %v, want zero time", got)
	}
}

func TestTimingStoresReturnTimestamp(t *testing.T) {
	const profilerName = "timing-value"
	want := time.Date(2026, time.July, 21, 8, 0, 0, 0, time.UTC)
	SetSampleSerializeTimeStamp(profilerName, want)
	SetSymbolizeToPprofTimeStamp(profilerName, want)
	t.Cleanup(func() {
		SampleSerializeTimeStore.Delete(profilerName)
		SymbolizeToPprofTimeStore.Delete(profilerName)
	})

	if got := GetSampleSerializeTimeStamp(profilerName); !got.Equal(want) {
		t.Fatalf("GetSampleSerializeTimeStamp() = %v, want %v", got, want)
	}
	if got := GetSymbolizeToPprofTimeStamp(profilerName); !got.Equal(want) {
		t.Fatalf("GetSymbolizeToPprofTimeStamp() = %v, want %v", got, want)
	}
}
