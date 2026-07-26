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
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

type fakeProfileQueryStorage struct {
	documents   []*ProfileDocument
	count       int64
	countErr    error
	searchErr   error
	searchCalls []SearchFilter
	pages       map[int][]*ProfileDocument
}

func (*fakeProfileQueryStorage) Close(context.Context) error { return nil }
func (*fakeProfileQueryStorage) Ready(context.Context) error { return nil }

func (s *fakeProfileQueryStorage) SearchProfilesContext(
	_ context.Context,
	filter *SearchFilter,
) ([]*ProfileDocument, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	s.searchCalls = append(s.searchCalls, *filter)
	if s.pages != nil {
		return s.pages[filter.Offset], nil
	}
	documents := s.matchingDocuments(filter)
	if filter.Offset >= len(documents) {
		return nil, nil
	}
	end := min(filter.Offset+filter.Limit, len(documents))
	return documents[filter.Offset:end], nil
}

func (s *fakeProfileQueryStorage) CountProfilesContext(
	_ context.Context,
	filter *SearchFilter,
) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	if s.count != 0 {
		return s.count, nil
	}
	return int64(len(s.matchingDocuments(filter))), nil
}

func (*fakeProfileQueryStorage) AggregationsByFieldContext(
	context.Context,
	*SearchFilter,
	string,
) ([]string, error) {
	return nil, nil
}

func (s *fakeProfileQueryStorage) matchingDocuments(filter *SearchFilter) []*ProfileDocument {
	documents := make([]*ProfileDocument, 0, len(s.documents))
documentLoop:
	for _, document := range s.documents {
		if document == nil {
			continue
		}
		timestamp := profileDocumentTimestamp(document)
		if !filter.StartTime.IsZero() && timestamp.Before(filter.StartTime) {
			continue
		}
		if !filter.EndTime.IsZero() && timestamp.After(filter.EndTime) {
			continue
		}
		if filter.ProfileType != "" &&
			filter.ProfileType != document.TracerData.Flamedata.ProfileType {
			continue
		}
		tracerID := filter.TracerID
		if tracerID == "" {
			tracerID = filter.ID
		}
		if tracerID != "" && tracerID != document.TracerID {
			continue
		}
		if filter.Hostname != "" &&
			(filter.Hostname != document.Hostname || document.ContainerHostname != "") {
			continue
		}
		if filter.ContainerID != "" && filter.ContainerID != document.ContainerID {
			continue
		}
		if filter.ContainerHostname != "" &&
			filter.ContainerHostname != document.ContainerHostname {
			continue
		}
		for name, value := range filter.Labels {
			if value != "" &&
				document.TracerData.Flamedata.Labels[name] != value {
				continue documentLoop
			}
		}
		documents = append(documents, document)
	}
	return documents
}

func newTestProfileService(storage *fakeProfileQueryStorage) *Service {
	return &Service{profileStorage: storage}
}

func testProfileDocument(
	timestamp time.Time,
	tracerID string,
	value int64,
	stack ...string,
) *ProfileDocument {
	stringTable := []string{"", "cpu", "nanoseconds"}
	functions := make([]*profilev1.Function, len(stack))
	locations := make([]*profilev1.Location, len(stack))
	locationIDs := make([]uint64, len(stack))
	for i, name := range stack {
		stringTable = append(stringTable, name)
		id := uint64(i + 1)
		functions[i] = &profilev1.Function{
			Id:   id,
			Name: int64(len(stringTable) - 1),
		}
		locations[i] = &profilev1.Location{
			Id:   id,
			Line: []*profilev1.Line{{FunctionId: id}},
		}
		locationIDs[len(stack)-1-i] = id
	}
	document := &ProfileDocument{
		Hostname:     "node-a",
		UploadedTime: timestamp,
		TracerID:     tracerID,
		TracerTime:   timestamp.Format(profileTimeLayout),
	}
	document.TracerData.Flamedata.ProfileType = profiler.ProfileTypeCpuSample
	document.TracerData.Flamedata.Profile = profilev1.Profile{
		StringTable: stringTable,
		SampleType: []*profilev1.ValueType{{
			Type: 1,
			Unit: 2,
		}},
		Function: functions,
		Location: locations,
		Sample: []*profilev1.Sample{{
			LocationId: locationIDs,
			Value:      []int64{value},
		}},
	}
	return document
}

func TestSearchProfileDocumentsPaginatesWithoutTruncation(t *testing.T) {
	document := testProfileDocument(time.Now(), "trace-a", 1, "root", "leaf")
	documents := make([]*ProfileDocument, 1001)
	for i := range documents {
		documents[i] = document
	}
	storage := &fakeProfileQueryStorage{documents: documents}
	service := newTestProfileService(storage)
	selection, err := buildProfileSelection(
		profiler.ProfileTypeCpuSample,
		`{hostname="node-a"}`,
		0,
		time.Now().Add(time.Hour).UnixMilli(),
	)
	if err != nil {
		t.Fatalf("buildProfileSelection() error = %v", err)
	}

	got, err := service.searchProfileDocuments(t.Context(), selection)
	if err != nil {
		t.Fatalf("searchProfileDocuments() error = %v", err)
	}
	if len(got) != len(documents) {
		t.Fatalf("documents = %d, want %d", len(got), len(documents))
	}
	if len(storage.searchCalls) != 2 {
		t.Fatalf("search calls = %d, want 2", len(storage.searchCalls))
	}
	if storage.searchCalls[0].Limit != 1000 ||
		storage.searchCalls[0].Offset != 0 ||
		storage.searchCalls[1].Limit != 1 ||
		storage.searchCalls[1].Offset != 1000 {
		t.Fatalf("search pages = %#v, want 1000@0 and 1@1000", storage.searchCalls)
	}
}

func TestSearchProfileDocumentsRejectsOversizedQuery(t *testing.T) {
	service := newTestProfileService(&fakeProfileQueryStorage{
		count: profileQueryLimit + 1,
	})
	selection, err := buildProfileSelection(
		profiler.ProfileTypeCpuSample,
		`{id="trace-a"}`,
		0,
		1,
	)
	if err != nil {
		t.Fatalf("buildProfileSelection() error = %v", err)
	}

	_, err = service.searchProfileDocuments(t.Context(), selection)
	if !errors.Is(err, ErrProfileQueryLimitExceeded) {
		t.Fatalf("searchProfileDocuments() error = %v, want query limit error", err)
	}
}

func TestSearchProfileDocumentsRejectsInvalidStorageResults(t *testing.T) {
	document := testProfileDocument(time.Now(), "trace-a", 1, "root", "leaf")
	tests := []struct {
		name    string
		storage *fakeProfileQueryStorage
	}{
		{
			name:    "negative count",
			storage: &fakeProfileQueryStorage{count: -1},
		},
		{
			name: "empty page",
			storage: &fakeProfileQueryStorage{
				count: 1,
				pages: map[int][]*ProfileDocument{0: {}},
			},
		},
		{
			name: "short page",
			storage: &fakeProfileQueryStorage{
				count: 2,
				pages: map[int][]*ProfileDocument{0: {document}},
			},
		},
		{
			name: "oversized page",
			storage: &fakeProfileQueryStorage{
				count: 1,
				pages: map[int][]*ProfileDocument{0: {document, document}},
			},
		},
		{
			name: "nil document",
			storage: &fakeProfileQueryStorage{
				count: 1,
				pages: map[int][]*ProfileDocument{0: {nil}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestProfileService(test.storage)
			selection, err := buildProfileSelection(
				profiler.ProfileTypeCpuSample,
				`{id="trace-a"}`,
				0,
				time.Now().Add(time.Hour).UnixMilli(),
			)
			if err != nil {
				t.Fatalf("buildProfileSelection() error = %v", err)
			}

			_, err = service.searchProfileDocuments(t.Context(), selection)
			if err == nil {
				t.Fatal("searchProfileDocuments() error = nil, want storage contract error")
			}
			if errors.Is(err, ErrInvalidQuery) ||
				errors.Is(err, ErrProfilesAbsent) ||
				errors.Is(err, ErrProfileQueryLimitExceeded) {
				t.Fatalf("storage contract error = %v, want internal error", err)
			}
		})
	}
}

func TestBuildProfileSelectionRejectsBroadMatchers(t *testing.T) {
	for _, selector := range []string{
		`{hostname=~"node-.*"}`,
		`{region="ap-guangzhou"}`,
		`{arbitrary_label="value"}`,
		`{id="trace-a",tracer="trace-b"}`,
		`{pid=""}`,
	} {
		t.Run(selector, func(t *testing.T) {
			_, err := buildProfileSelection(
				profiler.ProfileTypeCpuSample,
				selector,
				0,
				1,
			)
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("buildProfileSelection() error = %v, want invalid query", err)
			}
		})
	}
}

func TestSelectSeriesBucketsAndGroups(t *testing.T) {
	start := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	storage := &fakeProfileQueryStorage{documents: []*ProfileDocument{
		testProfileDocument(start.Add(100*time.Millisecond), "trace-a", 10, "root", "hot"),
		testProfileDocument(start.Add(1500*time.Millisecond), "trace-a", 20, "root", "hot"),
		testProfileDocument(start.Add(500*time.Millisecond), "trace-b", 7, "root", "worker"),
	}}
	service := newTestProfileService(storage)

	response, err := service.SelectSeries(t.Context(), &querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(4 * time.Second).UnixMilli(),
		GroupBy:       []string{"tracer"},
		Step:          2,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(response.Series) != 2 {
		t.Fatalf("series = %#v, want two tracer groups", response.Series)
	}
	if got := response.Series[0].Labels[0].Value; got != "trace-a" {
		t.Fatalf("first series tracer = %q, want trace-a", got)
	}
	wantPoints := []*typesv1.Point{{
		Value:     30,
		Timestamp: start.UnixMilli(),
	}}
	if got := response.Series[0].Points; !reflect.DeepEqual(got, wantPoints) {
		t.Fatalf("trace-a points = %#v, want %#v", got, wantPoints)
	}
}

func TestCollectionDimensionsFilterAndGroupFixtureProfiles(t *testing.T) {
	start := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	matching := testProfileDocument(
		start.Add(time.Second),
		"trace-pid-4242",
		25,
		"root",
		"hot",
	)
	matching.TracerData.Flamedata.Labels = map[string]string{
		profiler.LabelProfilingScope: "pid",
		profiler.LabelCPU:            "2,4,5,6,7",
		profiler.LabelPID:            "4242",
	}
	matching.TracerData.Flamedata.Profile.StringTable = append(
		matching.TracerData.Flamedata.Profile.StringTable,
		profiler.LabelProfilingScope,
		"pid",
		"2,4,5,6,7",
		"4242",
	)
	matching.TracerData.Flamedata.Profile.Sample[0].Label = []*profilev1.Label{
		{Key: 5, Str: 6},
		{Key: 1, Str: 7},
		{Key: 6, Str: 8},
	}
	other := testProfileDocument(
		start.Add(2*time.Second),
		"trace-pid-9000",
		100,
		"root",
		"cold",
	)
	other.TracerData.Flamedata.Labels = map[string]string{
		profiler.LabelProfilingScope: "pid",
		profiler.LabelCPU:            "3",
		profiler.LabelPID:            "9000",
	}
	service := newTestProfileService(&fakeProfileQueryStorage{
		documents: []*ProfileDocument{matching, other},
	})

	response, err := service.SelectSeries(
		t.Context(),
		&querierv1.SelectSeriesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{profiling_scope="pid",cpu="2,4,5,6,7"}`,
			Start:         start.UnixMilli(),
			End:           start.Add(time.Minute).UnixMilli(),
			GroupBy:       []string{profiler.LabelPID},
			Step:          10,
		},
	)
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(response.Series) != 1 ||
		len(response.Series[0].Labels) != 1 ||
		response.Series[0].Labels[0].Name != profiler.LabelPID ||
		response.Series[0].Labels[0].Value != "4242" ||
		len(response.Series[0].Points) != 1 ||
		response.Series[0].Points[0].Value != 25 {
		t.Fatalf("dimensioned series = %#v", response.Series)
	}

	flamegraph, err := service.SelectMergeStacktraces(
		t.Context(),
		&querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{pid="4242"}`,
			Start:         start.UnixMilli(),
			End:           start.Add(time.Minute).UnixMilli(),
		},
	)
	if err != nil {
		t.Fatalf("SelectMergeStacktraces() error = %v", err)
	}
	if flamegraph.GetFlamegraph().GetTotal() != 25 {
		t.Fatalf(
			"dimensioned flame graph total = %d, want 25",
			flamegraph.GetFlamegraph().GetTotal(),
		)
	}

	payload, err := service.MarshalPprof(
		t.Context(),
		&querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{pid="4242"}`,
			Start:         start.UnixMilli(),
			End:           start.Add(time.Minute).UnixMilli(),
		},
	)
	if err != nil {
		t.Fatalf("MarshalPprof() error = %v", err)
	}
	exported, err := pprof.RawFromBytes(payload)
	if err != nil {
		t.Fatalf("decode exported pprof: %v", err)
	}
	if len(exported.Sample) != 1 || len(exported.Sample[0].Label) != 3 {
		t.Fatalf("exported managed sample labels = %#v", exported.Sample)
	}
	exportedLabels := make(map[string]string, len(exported.Sample[0].Label))
	for _, label := range exported.Sample[0].Label {
		name, nameOK := profileString(exported.StringTable, label.Key)
		value, valueOK := profileString(exported.StringTable, label.Str)
		if nameOK && valueOK {
			exportedLabels[name] = value
		}
	}
	if !reflect.DeepEqual(
		exportedLabels,
		matching.TracerData.Flamedata.Labels,
	) {
		t.Fatalf(
			"exported labels = %#v, want %#v",
			exportedLabels,
			matching.TracerData.Flamedata.Labels,
		)
	}
}

func TestProfileNodeLimits(t *testing.T) {
	if got, err := normalizeProfileMaxNodes(0); err != nil ||
		got != defaultProfileNodes {
		t.Fatalf("normalizeProfileMaxNodes(0) = (%d, %v)", got, err)
	}
	for _, value := range []int64{-1, profileNodeLimit + 1} {
		if _, err := normalizeProfileMaxNodes(value); !errors.Is(
			err,
			ErrInvalidQuery,
		) {
			t.Fatalf(
				"normalizeProfileMaxNodes(%d) error = %v, want invalid query",
				value,
				err,
			)
		}
	}
}

func TestDiffBuildsDoubleFlamegraph(t *testing.T) {
	start := time.Date(2026, time.July, 25, 11, 0, 0, 0, time.UTC)
	service := newTestProfileService(&fakeProfileQueryStorage{documents: []*ProfileDocument{
		testProfileDocument(start.Add(time.Second), "left", 10, "root", "hot"),
	}})
	maxNodes := int64(100)
	response, err := service.Diff(t.Context(), &querierv1.DiffRequest{
		Left: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{id="left"}`,
			Start:         start.UnixMilli(),
			End:           start.Add(5 * time.Second).UnixMilli(),
			MaxNodes:      &maxNodes,
		},
		Right: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{id="right"}`,
			Start:         start.Add(10 * time.Second).UnixMilli(),
			End:           start.Add(15 * time.Second).UnixMilli(),
			MaxNodes:      &maxNodes,
		},
	})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if response.Flamegraph == nil || response.Flamegraph.LeftTicks != 10 {
		t.Fatalf("Diff() response = %#v, want 10 left ticks", response)
	}
}

func TestEmptyStoredProfileHasNoUsableSamples(t *testing.T) {
	start := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	document := &ProfileDocument{
		Hostname:     "node-a",
		UploadedTime: start,
		TracerID:     "empty-profile",
	}
	document.TracerData.Flamedata.ProfileType = profiler.ProfileTypeCpuSample
	service := newTestProfileService(&fakeProfileQueryStorage{
		documents: []*ProfileDocument{document},
	})

	_, err := service.SelectMergeStacktraces(
		t.Context(),
		&querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{id="empty-profile"}`,
			Start:         start.Add(-time.Second).UnixMilli(),
			End:           start.Add(time.Second).UnixMilli(),
		},
	)
	if !errors.Is(err, ErrProfilesAbsent) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want profiles absent", err)
	}

	series, err := service.SelectSeries(t.Context(), &querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{id="empty-profile"}`,
		Start:         start.Add(-time.Second).UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
		Step:          1,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(series.Series) != 0 {
		t.Fatalf("SelectSeries() series = %#v, want empty", series.Series)
	}
}

func TestSamplesWithoutValuesAreSkipped(t *testing.T) {
	start := time.Date(2026, time.July, 25, 13, 0, 0, 0, time.UTC)
	document := testProfileDocument(start, "no-values", 1, "root", "leaf")
	document.TracerData.Flamedata.Profile.Sample = []*profilev1.Sample{
		nil,
		{LocationId: []uint64{2, 1}},
	}
	service := newTestProfileService(&fakeProfileQueryStorage{
		documents: []*ProfileDocument{document},
	})

	series, err := service.SelectSeries(t.Context(), &querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{id="no-values"}`,
		Start:         start.Add(-time.Second).UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
		Step:          1,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(series.Series) != 0 {
		t.Fatalf("SelectSeries() series = %#v, want empty", series.Series)
	}

	_, err = service.SelectMergeStacktraces(
		t.Context(),
		&querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{id="no-values"}`,
			Start:         start.Add(-time.Second).UnixMilli(),
			End:           start.Add(time.Second).UnixMilli(),
		},
	)
	if !errors.Is(err, ErrProfilesAbsent) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want profiles absent", err)
	}
}

func TestProfileQueryStorageErrorsRemainInternal(t *testing.T) {
	sentinel := fmt.Errorf("backend unavailable")
	service := newTestProfileService(&fakeProfileQueryStorage{countErr: sentinel})
	_, err := service.SelectSeries(t.Context(), &querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{id="trace-a"}`,
		Start:         0,
		End:           1,
		Step:          1,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("SelectSeries() error = %v, want wrapped backend error", err)
	}
}
