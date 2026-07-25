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
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestPprofDocumentStoreMapperEncodesProtobuf(t *testing.T) {
	start := time.Unix(1700000000, 123).UTC()
	profileData := &profiler.ProfileData{
		ProfileType: profiler.ProfileTypeCpuSample,
		Labels: map[string]string{
			profiler.LabelProfilingScope: "thread_group",
			profiler.LabelCPU:            "1,3",
			profiler.LabelTGID:           "4242",
			profiler.LabelContainerID:    "profile-container",
			"unmanaged":                  "ignored",
		},
		Profile: ptree.Profile{
			StringTable:   []string{"", "cpu", "nanoseconds"},
			TimeNanos:     start.UnixNano(),
			DurationNanos: int64(10 * time.Second),
		},
	}
	document := &Document{
		TracerID:    "trace-1",
		TracerTime:  start.Format(tracingDocumentTimeLayout),
		ContainerID: "document-container",
		TracerData:  testProfileEnvelope{profileData: profileData},
	}

	mapper := PprofDocumentStoreMapper{}
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
		t.Errorf("profile_type = %v, want %q", got, profiler.ProfileTypeCpuSample)
	}
	if got := fields["profile_start_time"]; got != start {
		t.Errorf("profile_start_time = %v, want %v", got, start)
	}
	if got := fields["profile_end_time"]; got != start.Add(10*time.Second) {
		t.Errorf("profile_end_time = %v, want %v", got, start.Add(10*time.Second))
	}
	for name, want := range map[string]string{
		profiler.LabelProfilingScope: "thread_group",
		profiler.LabelCPU:            "1,3",
		profiler.LabelTGID:           "4242",
		profiler.LabelContainerID:    "document-container",
	} {
		if got := fields[name]; got != want {
			t.Errorf("%s = %v, want %q", name, got, want)
		}
	}
	if _, ok := fields["unmanaged"]; ok {
		t.Fatal("unmanaged profile label was exposed as series metadata")
	}

	indexes := make(map[string]struct{})
	for _, index := range mapper.Indexes() {
		indexes[index.Field] = struct{}{}
	}
	for _, name := range profiler.CollectionDimensionLabelNames() {
		if _, ok := indexes[name]; !ok {
			t.Errorf("PprofDocumentStoreMapper indexes missing %q", name)
		}
	}
}

func TestPprofDocumentStoreMapperRejectsMissingProfile(t *testing.T) {
	mapper := PprofDocumentStoreMapper{}
	if _, err := mapper.Encode(&Document{TracerData: testProfileEnvelope{}}); err == nil {
		t.Fatal("Encode error = nil, want missing profile error")
	}
}

func TestPprofDocumentStoreMapperDecodeIsUnsupported(t *testing.T) {
	_, err := (PprofDocumentStoreMapper{}).Decode(nil)
	if !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Fatalf("Decode error = %v, want ErrUnsupportedOp", err)
	}
}

type testProfileEnvelope struct {
	profileData *profiler.ProfileData
}

func (e testProfileEnvelope) ProfileData() *profiler.ProfileData {
	return e.profileData
}
