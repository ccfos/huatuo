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

package tracing

import (
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestProfileDocumentStoreMapperEncodesPprofProtobuf(t *testing.T) {
	start := time.Unix(1700000000, 123).UTC()
	profileData := &profiler.ProfileData{
		ProfileType: profiler.ProfileTypeCpuSample,
		Profile: ptree.Profile{
			StringTable:   []string{"", "cpu", "nanoseconds"},
			TimeNanos:     start.UnixNano(),
			DurationNanos: int64(10 * time.Second),
		},
	}
	document := &Document{
		TracerID:   "trace-1",
		TracerTime: start.Format(tracingDocumentTimeLayout),
		TracerData: profileData,
	}

	mapper := ProfileDocumentStoreMapper{}
	raw, err := mapper.Encode(document)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	var decoded ptree.Profile
	if err := decoded.UnmarshalVT(raw); err != nil {
		t.Fatalf("UnmarshalVT returned error: %v", err)
	}
	if decoded.TimeNanos != start.UnixNano() {
		t.Errorf("TimeNanos = %d, want %d", decoded.TimeNanos, start.UnixNano())
	}

	fields, err := mapper.Fields(document)
	if err != nil {
		t.Fatalf("Fields returned error: %v", err)
	}
	if got := fields["profile_type"]; got != profiler.ProfileTypeCpuSample {
		t.Errorf("profile_type = %v", got)
	}
	if got := fields["profile_start_time"]; got != start {
		t.Errorf("profile_start_time = %v, want %v", got, start)
	}
	if got := fields["profile_end_time"]; got != start.Add(10*time.Second) {
		t.Errorf("profile_end_time = %v", got)
	}
}

func TestProfileDocumentStoreMapperRejectsMissingProfile(t *testing.T) {
	mapper := ProfileDocumentStoreMapper{}
	if _, err := mapper.Encode(&Document{TracerData: map[string]any{"output": "missing"}}); err == nil {
		t.Fatal("Encode error = nil")
	}
}
