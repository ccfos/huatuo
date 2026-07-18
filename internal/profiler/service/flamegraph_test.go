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
	"fmt"
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

type fakeProfileQueryStorage struct {
	documents  []*ProfileDocument
	lastFilter *SearchFilter
}

func (s *fakeProfileQueryStorage) SearchProfiles(filter *SearchFilter) ([]*ProfileDocument, error) {
	s.lastFilter = filter
	return append([]*ProfileDocument(nil), s.documents...), nil
}

func (s *fakeProfileQueryStorage) CountProfiles(*SearchFilter) (int64, error) {
	return int64(len(s.documents)), nil
}

func (*fakeProfileQueryStorage) AggregationsByField(*SearchFilter, string) ([]string, error) {
	return nil, nil
}

func installFakeProfileStorage(t *testing.T, documents ...*ProfileDocument) *fakeProfileQueryStorage {
	t.Helper()
	previous := profileStorage
	storage := &fakeProfileQueryStorage{documents: documents}
	profileStorage = storage
	t.Cleanup(func() { profileStorage = previous })
	return storage
}

func testProfileDocument(timestamp time.Time, hostname, tgid string, value int64, stack ...string) *ProfileDocument {
	stringTable := []string{"", "cpu", "nanoseconds"}
	functions := make([]*profilev1.Function, len(stack))
	locations := make([]*profilev1.Location, len(stack))
	for i, name := range stack {
		stringTable = append(stringTable, name)
		id := uint64(i + 1)
		functions[i] = &profilev1.Function{Id: id, Name: int64(len(stringTable) - 1)}
		locations[i] = &profilev1.Location{
			Id:   id,
			Line: []*profilev1.Line{{FunctionId: id}},
		}
	}
	locationIDs := make([]uint64, len(stack))
	for i := range stack {
		// pprof stores leaf first; the query conversion reverses this order.
		locationIDs[i] = uint64(len(stack) - i)
	}

	document := &ProfileDocument{
		Hostname:     hostname,
		UploadedTime: timestamp,
		TracerID:     fmt.Sprintf("profile-%s-%d", tgid, timestamp.UnixNano()),
		TracerTime:   timestamp.Format(profileTimeLayout),
	}
	document.TracerData.Flamedata.ProfileType = profiler.ProfileTypeCpuSample
	document.TracerData.Flamedata.Labels = map[string]string{
		profiler.LabelProfilingScope: "tgid",
		profiler.LabelTGID:           tgid,
	}
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

func TestSelectSeriesBucketsGroupsAndSorts(t *testing.T) {
	start := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	storage := installFakeProfileStorage(t,
		testProfileDocument(start.Add(100*time.Millisecond), "node-a", "42", 10, "root", "hot"),
		testProfileDocument(start.Add(1500*time.Millisecond), "node-a", "42", 20, "root", "hot"),
		testProfileDocument(start.Add(2500*time.Millisecond), "node-a", "42", 5, "root", "cold"),
		testProfileDocument(start.Add(500*time.Millisecond), "node-a", "7", 7, "root", "worker"),
		testProfileDocument(start.Add(500*time.Millisecond), "node-b", "42", 999, "root", "wrong-host"),
	)

	response, err := SelectSeries(&querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a",tgid=~"42|7"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(4 * time.Second).UnixMilli(),
		GroupBy:       []string{profiler.LabelTGID},
		Step:          2,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if storage.lastFilter == nil || storage.lastFilter.Hostname != "node-a" {
		t.Fatalf("pushed filter = %+v, want hostname node-a", storage.lastFilter)
	}
	if len(response.Series) != 2 {
		t.Fatalf("series = %#v, want two TGID groups", response.Series)
	}
	if got := response.Series[0].Labels[0].Value; got != "42" {
		t.Fatalf("first series TGID = %q, want stable lexical order starting with 42", got)
	}
	wantPoints := []*typesv1.Point{
		{Timestamp: start.UnixMilli(), Value: 30},
		{Timestamp: start.Add(2 * time.Second).UnixMilli(), Value: 5},
	}
	if got := response.Series[0].Points; len(got) != len(wantPoints) ||
		got[0].Timestamp != wantPoints[0].Timestamp || got[0].Value != wantPoints[0].Value ||
		got[1].Timestamp != wantPoints[1].Timestamp || got[1].Value != wantPoints[1].Value {
		t.Fatalf("TGID 42 points = %#v, want %#v", got, wantPoints)
	}
	if got := response.Series[1].Labels[0].Value; got != "7" {
		t.Fatalf("second series TGID = %q, want 7", got)
	}
}

func TestSelectSeriesAverageAndEmptyResponse(t *testing.T) {
	start := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	installFakeProfileStorage(t,
		testProfileDocument(start.Add(100*time.Millisecond), "node-a", "42", 10, "root", "hot"),
		testProfileDocument(start.Add(900*time.Millisecond), "node-a", "42", 20, "root", "hot"),
	)
	aggregation := typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_AVERAGE
	response, err := SelectSeries(&querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
		Step:          1,
		Aggregation:   &aggregation,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(response.Series) != 1 || len(response.Series[0].Points) != 1 || response.Series[0].Points[0].Value != 15 {
		t.Fatalf("average response = %#v, want one point with value 15", response)
	}

	installFakeProfileStorage(t)
	empty, err := SelectSeries(&querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
		Step:          1,
	})
	if err != nil {
		t.Fatalf("SelectSeries(empty) error = %v", err)
	}
	if empty == nil || len(empty.Series) != 0 {
		t.Fatalf("empty response = %#v, want non-nil response with no series", empty)
	}
}

func TestSelectSeriesRejectsInvalidStep(t *testing.T) {
	installFakeProfileStorage(t)
	_, err := SelectSeries(&querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         1,
		End:           2,
	})
	if err == nil {
		t.Fatal("SelectSeries() error = nil, want invalid step error")
	}
	if !errors.Is(err, ErrInvalidProfileQuery) {
		t.Fatalf("SelectSeries() error = %v, want ErrInvalidProfileQuery", err)
	}
}

func TestExactMatchersDoNotInterpretDashboardSentinels(t *testing.T) {
	start := time.Date(2026, time.July, 16, 9, 10, 0, 0, time.UTC)
	installFakeProfileStorage(t,
		testProfileDocument(start, "node-a", "42", 1, "root", "worker"),
		testProfileDocument(start, "All", "43", 1, "root", "worker"),
		testProfileDocument(start, "*", "44", 1, "root", "worker"),
		testProfileDocument(start, "", "45", 1, "root", "worker"),
	)

	for _, literal := range []string{"", "All", "*"} {
		selection, _, err := buildProfileSelection(
			profiler.ProfileTypeCpuSample,
			fmt.Sprintf(`{hostname=%q}`, literal),
			start.Add(-time.Second).UnixMilli(),
			start.Add(time.Second).UnixMilli(),
			profileQueryLimit,
		)
		if err != nil {
			t.Fatalf("buildProfileSelection(%q) error = %v", literal, err)
		}
		documents, err := searchProfileDocuments(selection)
		if err != nil {
			t.Fatalf("searchProfileDocuments(%q) error = %v", literal, err)
		}
		if len(documents) != 1 || documents[0].Hostname != literal {
			t.Fatalf("literal %q matched hosts %#v", literal, documents)
		}
	}

	selection, _, err := buildProfileSelection(
		profiler.ProfileTypeCpuSample,
		`{hostname=~".*"}`,
		start.Add(-time.Second).UnixMilli(),
		start.Add(time.Second).UnixMilli(),
		profileQueryLimit,
	)
	if err != nil {
		t.Fatalf("buildProfileSelection(all regex) error = %v", err)
	}
	documents, err := searchProfileDocuments(selection)
	if err != nil {
		t.Fatalf("searchProfileDocuments(all regex) error = %v", err)
	}
	if len(documents) != 4 {
		t.Fatalf("all regex matched %d documents, want 4", len(documents))
	}
}

func TestBuildProfileSelectionPreservesHostAndContainerScope(t *testing.T) {
	tests := []struct {
		name             string
		selector         string
		wantContainer    string
		wantInclude      bool
		wantHostOnlyTerm bool
	}{
		{
			name:             "host selector",
			selector:         `{hostname="node-a"}`,
			wantHostOnlyTerm: true,
		},
		{
			name:             "comparison all selects host profiles",
			selector:         `{hostname="node-a",container_hostname=~"^$"}`,
			wantHostOnlyTerm: true,
		},
		{
			name:          "literal container regex is pushed down",
			selector:      `{hostname="node-a",container_hostname=~"worker"}`,
			wantContainer: "worker",
			wantInclude:   true,
		},
		{
			name:        "general container regex includes container candidates",
			selector:    `{hostname="node-a",container_hostname=~"worker-.*"}`,
			wantInclude: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selection, _, err := buildProfileSelection(
				profiler.ProfileTypeCpuSample,
				tc.selector,
				1,
				2,
				profileQueryLimit,
			)
			if err != nil {
				t.Fatalf("buildProfileSelection() error = %v", err)
			}
			if got := selection.filter.ContainerHostname; got != tc.wantContainer {
				t.Fatalf("ContainerHostname = %q, want %q", got, tc.wantContainer)
			}
			if got := selection.filter.IncludeContainerProfiles; got != tc.wantInclude {
				t.Fatalf("IncludeContainerProfiles = %v, want %v", got, tc.wantInclude)
			}

			query := buildProfileAggregationQuery(selection.filter)
			hostOnlyTerm := false
			for _, filter := range query.Filters {
				if filter.Field == profileFieldContainerHostname && filter.Value == "" {
					hostOnlyTerm = true
				}
			}
			if hostOnlyTerm != tc.wantHostOnlyTerm {
				t.Fatalf("filters = %#v, hostOnlyTerm=%v, want %v", query.Filters, hostOnlyTerm, tc.wantHostOnlyTerm)
			}
		})
	}
}

func TestProfileQueryRejectsTruncatedStorageWindow(t *testing.T) {
	start := time.Date(2026, time.July, 16, 9, 15, 0, 0, time.UTC)
	document := testProfileDocument(start, "node-a", "42", 1, "root", "worker")
	documents := make([]*ProfileDocument, profileQueryLimit+1)
	for i := range documents {
		documents[i] = document
	}
	installFakeProfileStorage(t, documents...)
	_, err := SelectMergeStacktraces(&querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.Add(-time.Second).UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
	})
	if !errors.Is(err, ErrProfileQueryLimitExceeded) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want ErrProfileQueryLimitExceeded", err)
	}
}

func TestSelectSeriesReturnsTopTenGroupsByValue(t *testing.T) {
	start := time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC)
	documents := make([]*ProfileDocument, 12)
	for i := range documents {
		documents[i] = testProfileDocument(
			start.Add(time.Duration(i)*time.Millisecond),
			"node-a",
			fmt.Sprintf("%02d", i),
			int64(i+1),
			"root",
			"worker",
		)
	}
	installFakeProfileStorage(t, documents...)
	response, err := SelectSeries(&querierv1.SelectSeriesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(time.Second).UnixMilli(),
		GroupBy:       []string{profiler.LabelTGID},
		Step:          1,
	})
	if err != nil {
		t.Fatalf("SelectSeries() error = %v", err)
	}
	if len(response.Series) != profileSeriesLimit {
		t.Fatalf("series count = %d, want %d", len(response.Series), profileSeriesLimit)
	}
	if got := response.Series[0].Labels[0].Value; got != "11" {
		t.Fatalf("first series TGID = %q, want highest-value group 11", got)
	}
	if got := response.Series[len(response.Series)-1].Labels[0].Value; got != "02" {
		t.Fatalf("last series TGID = %q, want tenth-highest group 02", got)
	}
}

func TestDiffBuildsDoubleFlamegraph(t *testing.T) {
	start := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	installFakeProfileStorage(t,
		testProfileDocument(start.Add(time.Second), "node-a", "left", 10, "root", "hot"),
		testProfileDocument(start.Add(11*time.Second), "node-a", "right", 5, "root", "hot"),
		testProfileDocument(start.Add(12*time.Second), "node-a", "right", 7, "root", "cold"),
	)

	maxNodes := int64(100)
	response, err := Diff(&querierv1.DiffRequest{
		Left: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{tgid="left"}`,
			Start:         start.UnixMilli(),
			End:           start.Add(5 * time.Second).UnixMilli(),
			MaxNodes:      &maxNodes,
		},
		Right: &querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: `{tgid="right"}`,
			Start:         start.Add(10 * time.Second).UnixMilli(),
			End:           start.Add(15 * time.Second).UnixMilli(),
			MaxNodes:      &maxNodes,
		},
	})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if response.Flamegraph == nil || response.Flamegraph.LeftTicks != 10 || response.Flamegraph.RightTicks != 12 {
		t.Fatalf("Diff() response = %#v, want left/right ticks 10/12", response)
	}
	wantNames := map[string]bool{"total": true, "root": true, "hot": true, "cold": true}
	for _, name := range response.Flamegraph.Names {
		delete(wantNames, name)
	}
	if len(wantNames) != 0 {
		t.Fatalf("Diff() names = %v, missing %v", response.Flamegraph.Names, wantNames)
	}
}

func TestProfileSampleStackPreservesInlineFrames(t *testing.T) {
	document := testProfileDocument(time.Now(), "node-a", "42", 1, "root", "leaf")
	profile := &document.TracerData.Flamedata.Profile
	profile.StringTable = append(profile.StringTable, "inline-parent")
	profile.Function = append(profile.Function, &profilev1.Function{
		Id:   3,
		Name: int64(len(profile.StringTable) - 1),
	})
	// Location 2 is the leaf location in testProfileDocument.
	profile.Location[1].Line = append(profile.Location[1].Line, &profilev1.Line{FunctionId: 3})
	locations := map[uint64]*profilev1.Location{}
	for _, location := range profile.Location {
		locations[location.Id] = location
	}
	functions := map[uint64]*profilev1.Function{}
	for _, function := range profile.Function {
		functions[function.Id] = function
	}

	got := profileSampleStack(profile, locations, functions, profile.Sample[0].LocationId)
	want := []string{"root", "inline-parent", "leaf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profileSampleStack() = %v, want %v", got, want)
	}
}
