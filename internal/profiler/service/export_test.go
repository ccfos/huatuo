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

package service

import (
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

func exportTestRequest(start time.Time) *querierv1.SelectMergeStacktracesRequest {
	return &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(time.Minute).UnixMilli(),
	}
}

func TestSelectMergePprofUsesBoundedProfileLoader(t *testing.T) {
	start := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	document := testProfileDocument(start, "trace-a", 1, "root", "worker")
	documents := make([]*ProfileDocument, profileQueryPageSize+1)
	for index := range documents {
		documents[index] = document
	}
	storage := &fakeProfileQueryStorage{documents: documents}
	service := newTestProfileService(storage)

	profile, err := service.SelectMergePprof(t.Context(), exportTestRequest(start))
	if err != nil {
		t.Fatalf("SelectMergePprof() error = %v", err)
	}
	var total int64
	for _, sample := range profile.Sample {
		if sample != nil && len(sample.Value) > 0 {
			total += sample.Value[0]
		}
	}
	if total != int64(len(documents)) {
		t.Fatalf("merged sample total = %d, want %d", total, len(documents))
	}
	if len(storage.searchCalls) != 2 {
		t.Fatalf("search calls = %d, want 2", len(storage.searchCalls))
	}
	first, second := storage.searchCalls[0], storage.searchCalls[1]
	if first.Offset != 0 ||
		first.Limit != profileQueryPageSize ||
		second.Offset != profileQueryPageSize ||
		second.Limit != 1 {
		t.Fatalf(
			"search pages = (%d,%d),(%d,%d), want (0,1000),(1000,1)",
			first.Offset,
			first.Limit,
			second.Offset,
			second.Limit,
		)
	}
}

func TestSelectMergePprofRejectsOversizedSelectionBeforeFetch(t *testing.T) {
	start := time.Date(2026, time.July, 25, 11, 0, 0, 0, time.UTC)
	storage := &fakeProfileQueryStorage{count: profileQueryLimit + 1}
	service := newTestProfileService(storage)

	_, err := service.SelectMergePprof(t.Context(), exportTestRequest(start))
	if !errors.Is(err, ErrProfileQueryLimitExceeded) {
		t.Fatalf(
			"SelectMergePprof() error = %v, want ErrProfileQueryLimitExceeded",
			err,
		)
	}
	if len(storage.searchCalls) != 0 {
		t.Fatalf("search calls = %d, want 0", len(storage.searchCalls))
	}
}

func TestSelectMergePprofRejectsEmptyStoredProfilesWithoutPanicking(t *testing.T) {
	start := time.Date(2026, time.July, 25, 12, 30, 0, 0, time.UTC)
	empty := &ProfileDocument{
		Hostname:     "node-a",
		UploadedTime: start,
		TracerID:     "empty-profile",
	}
	empty.TracerData.Flamedata.ProfileType = profiler.ProfileTypeCpuSample
	empty.TracerData.Flamedata.Profile.Sample = []*profilev1.Sample{nil}
	service := newTestProfileService(&fakeProfileQueryStorage{
		documents: []*ProfileDocument{empty},
	})

	_, err := service.SelectMergePprof(t.Context(), exportTestRequest(start))
	if !errors.Is(err, ErrProfilesAbsent) {
		t.Fatalf(
			"SelectMergePprof() error = %v, want ErrProfilesAbsent",
			err,
		)
	}
}

func TestMarshalPprof(t *testing.T) {
	start := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	service := newTestProfileService(&fakeProfileQueryStorage{
		documents: []*ProfileDocument{
			testProfileDocument(start, "trace-a", 10, "root", "hot"),
		},
	})
	req := exportTestRequest(start)

	payload, err := service.MarshalPprof(t.Context(), req)
	if err != nil {
		t.Fatalf("MarshalPprof() error = %v", err)
	}
	decoded, err := pprof.RawFromBytes(payload)
	if err != nil {
		t.Fatalf("RawFromBytes() error = %v", err)
	}
	if len(decoded.Sample) != 1 || decoded.Sample[0].Value[0] != 10 {
		t.Fatalf("pprof samples = %#v, want value 10", decoded.Sample)
	}
}
