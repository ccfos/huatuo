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

package profiling

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"huatuo-bamai/internal/profiler"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"

	httpGin "github.com/gin-gonic/gin"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

type recordingProfileDiffService struct {
	*profileService.Service
	request *querierv1.DiffRequest
}

func (s *recordingProfileDiffService) Diff(
	_ context.Context,
	request *querierv1.DiffRequest,
) (*querierv1.DiffResponse, error) {
	s.request = request
	return &querierv1.DiffResponse{Flamegraph: &querierv1.FlameGraphDiff{}}, nil
}

func TestBuildAdjacentProfileDiffRequest(t *testing.T) {
	request, err := buildAdjacentProfileDiffRequest(&profileDiffRowsRequest{
		ProfileTypeID:  profiler.ProfileTypeCpuSample,
		Hostname:       "node-a",
		ContainerID:    `container\"a`,
		ProfilingScope: "container",
		CPU:            "2,4-7",
		PID:            "4242",
		TGID:           "4200",
		Start:          2000,
		End:            3000,
		MaxNodes:       500,
	})
	if err != nil {
		t.Fatalf("buildAdjacentProfileDiffRequest() error = %v", err)
	}

	if request.Left.Start != 1000 || request.Left.End != 1999 {
		t.Fatalf(
			"left range = %d..%d, want 1000..1999",
			request.Left.Start,
			request.Left.End,
		)
	}
	if request.Right.Start != 2000 || request.Right.End != 3000 {
		t.Fatalf(
			"right range = %d..%d, want 2000..3000",
			request.Right.Start,
			request.Right.End,
		)
	}
	wantSelector := `{container_id="container\\\"a",` +
		`profiling_scope="container",cpu="2,4-7",pid="4242",tgid="4200"}`
	if request.Left.LabelSelector != wantSelector ||
		request.Right.LabelSelector != wantSelector {
		t.Fatalf(
			"selectors = %q and %q, want %q",
			request.Left.LabelSelector,
			request.Right.LabelSelector,
			wantSelector,
		)
	}
	if request.Left.GetMaxNodes() != 500 || request.Right.GetMaxNodes() != 500 {
		t.Fatal("max_nodes was not forwarded to both selections")
	}
}

func TestBuildAdjacentProfileDiffRequestRequiresTargetAndPreviousWindow(t *testing.T) {
	if _, err := buildAdjacentProfileDiffRequest(nil); err == nil {
		t.Fatal("buildAdjacentProfileDiffRequest(nil) succeeded")
	}
	tests := []profileDiffRowsRequest{
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Start:         2000,
			End:           3000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			Start:         500,
			End:           1500,
		},
	}
	for _, request := range tests {
		if _, err := buildAdjacentProfileDiffRequest(&request); err == nil {
			t.Fatalf("buildAdjacentProfileDiffRequest(%+v) succeeded", request)
		}
	}
}

func TestBuildAdjacentProfileDiffRequestSkipsDashboardAllSentinel(t *testing.T) {
	request, err := buildAdjacentProfileDiffRequest(&profileDiffRowsRequest{
		ProfileTypeID:  profiler.ProfileTypeCpuSample,
		Hostname:       "node-a",
		ContainerID:    profileService.ProfileAllValue,
		ProfilingScope: profileService.ProfileAllValue,
		CPU:            profileService.ProfileAllValue,
		PID:            profileService.ProfileAllValue,
		TGID:           profileService.ProfileAllValue,
		Start:          2000,
		End:            3000,
	})
	if err != nil {
		t.Fatalf("buildAdjacentProfileDiffRequest() error = %v", err)
	}
	if request.Left.LabelSelector != `{hostname="node-a"}` {
		t.Fatalf("selector = %q, want hostname only", request.Left.LabelSelector)
	}
}

func TestDisplayDiffRowsBindsEncodedQueryFromEmptyPost(t *testing.T) {
	service := &recordingProfileDiffService{Service: &profileService.Service{}}
	handler := &Handler{profileService: service}
	engine := httpGin.New()
	server.NewRoot(engine, "").POST("/diff", handler.displayDiffRows)

	values := url.Values{
		"profile_type_id": {profiler.ProfileTypeCpuSample},
		"hostname":        {"node a\"\\b"},
		"start":           {"2000"},
		"end":             {"3000"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/diff?"+values.Encode(),
		http.NoBody,
	)
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.request == nil {
		t.Fatal("Diff() request was not recorded")
	}
	want := `{hostname="node a\"\\b"}`
	if service.request.Left.LabelSelector != want {
		t.Fatalf("selector = %q, want %q", service.request.Left.LabelSelector, want)
	}
}

func TestBuildAdjacentProfileDiffRequestDefaultsAndBounds(t *testing.T) {
	valid := profileDiffRowsRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		Hostname:      "node-a",
		Start:         2000,
		End:           3000,
	}
	request, err := buildAdjacentProfileDiffRequest(&valid)
	if err != nil {
		t.Fatalf("buildAdjacentProfileDiffRequest() error = %v", err)
	}
	if request.Left.GetMaxNodes() != profileService.DefaultProfileMaxNodes ||
		request.Right.GetMaxNodes() != profileService.DefaultProfileMaxNodes {
		t.Fatal("default max_nodes was not applied to both selections")
	}

	tests := []profileDiffRowsRequest{
		{ProfileTypeID: "", Hostname: "node-a", Start: 2000, End: 3000},
		{
			ProfileTypeID: strings.Repeat("p", maxProfileDiffTypeLength+1),
			Hostname:      "node-a",
			Start:         2000,
			End:           3000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      strings.Repeat("h", maxProfileDiffTargetLength+1),
			Start:         2000,
			End:           3000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			CPU:           strings.Repeat("1", maxProfileDiffTargetLength+1),
			Start:         2000,
			End:           3000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			Start:         -1,
			End:           3000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			Start:         2000,
			End:           2000,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			Start:         2000,
			End:           3000,
			MaxNodes:      -1,
		},
		{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			Hostname:      "node-a",
			Start:         2000,
			End:           3000,
			MaxNodes:      profileService.MaxProfileNodes + 1,
		},
	}
	for _, request := range tests {
		if _, err := buildAdjacentProfileDiffRequest(&request); err == nil {
			t.Fatalf("buildAdjacentProfileDiffRequest(%+v) succeeded", request)
		}
	}
}

func TestBuildAdjacentProfileDiffRequestEscapesSelectorInput(t *testing.T) {
	request, err := buildAdjacentProfileDiffRequest(&profileDiffRowsRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		Hostname:      "node\n\"a\\b",
		Start:         2000,
		End:           3000,
	})
	if err != nil {
		t.Fatalf("buildAdjacentProfileDiffRequest() error = %v", err)
	}
	if request.Left.LabelSelector != `{hostname="node\n\"a\\b"}` {
		t.Fatalf("selector = %q", request.Left.LabelSelector)
	}
}

func TestProfileDiffRowsBuildsNestedSetOrder(t *testing.T) {
	graph := &querierv1.FlameGraphDiff{
		Names: []string{"total", "a", "b", "a-child"},
		Levels: []*querierv1.Level{
			{Values: []int64{0, 30, 0, 0, 40, 0, 0}},
			{Values: []int64{
				0, 10, 5, 0, 30, 10, 1,
				0, 20, 20, 0, 10, 10, 2,
			}},
			{Values: []int64{5, 5, 5, 10, 20, 20, 3}},
		},
	}

	rows, err := profileDiffRows(graph)
	if err != nil {
		t.Fatalf("profileDiffRows() error = %v", err)
	}
	want := []profileDiffRow{
		{Level: 0, Value: 30, ValueRight: 40, Label: "total"},
		{
			Level: 1, Value: 10, Self: 5,
			ValueRight: 30, SelfRight: 10, Label: "a",
		},
		{
			Level: 2, Value: 5, Self: 5,
			ValueRight: 20, SelfRight: 20, Label: "a-child",
		},
		{
			Level: 1, Value: 20, Self: 20,
			ValueRight: 10, SelfRight: 10, Label: "b",
		},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

func TestProfileDiffParentSearchesSortedRanges(t *testing.T) {
	leftParent := &profileDiffNode{
		row:        profileDiffRow{Value: 10},
		leftStart:  0,
		rightStart: 0,
	}
	rightParent := &profileDiffNode{
		row:        profileDiffRow{ValueRight: 10},
		leftStart:  10,
		rightStart: 0,
	}
	boundaryParent := &profileDiffNode{
		row:        profileDiffRow{Value: 10},
		leftStart:  10,
		rightStart: 10,
	}
	parents := []*profileDiffNode{
		leftParent,
		rightParent,
		boundaryParent,
	}

	tests := []struct {
		name  string
		child *profileDiffNode
		want  *profileDiffNode
	}{
		{
			name: "left range touching parent end",
			child: &profileDiffNode{
				row:       profileDiffRow{Value: 2},
				leftStart: 8,
			},
			want: leftParent,
		},
		{
			name: "right range only",
			child: &profileDiffNode{
				row:        profileDiffRow{ValueRight: 1},
				rightStart: 9,
			},
			want: rightParent,
		},
		{
			name: "next parent at shared boundary",
			child: &profileDiffNode{
				row:       profileDiffRow{Value: 1},
				leftStart: 10,
			},
			want: boundaryParent,
		},
		{
			name: "range crosses parent boundary",
			child: &profileDiffNode{
				row:       profileDiffRow{Value: 2},
				leftStart: 9,
			},
		},
		{
			name: "zero width on both sides",
			child: &profileDiffNode{
				leftStart:  5,
				rightStart: 5,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := profileDiffParent(parents, test.child); got != test.want {
				t.Fatalf("profileDiffParent() = %p, want %p", got, test.want)
			}
		})
	}

	child := &profileDiffNode{row: profileDiffRow{Value: 1}}
	if got := profileDiffParent(nil, child); got != nil {
		t.Fatalf("profileDiffParent(nil) = %p, want nil", got)
	}
}

func TestProfileDiffRowsRejectsMalformedLevel(t *testing.T) {
	_, err := profileDiffRows(&querierv1.FlameGraphDiff{
		Names:  []string{"total"},
		Levels: []*querierv1.Level{{Values: []int64{0, 1}}},
	})
	if err == nil {
		t.Fatal("profileDiffRows() succeeded for a malformed level")
	}
}

func TestProfileDiffRowsRejectsMaliciousValues(t *testing.T) {
	tests := []*querierv1.FlameGraphDiff{
		{
			Names:  []string{"total"},
			Levels: []*querierv1.Level{{Values: []int64{0, -1, 0, 0, 1, 0, 0}}},
		},
		{
			Names:  []string{"total"},
			Levels: []*querierv1.Level{{Values: []int64{0, 1, 2, 0, 1, 0, 0}}},
		},
		{
			Names:  []string{"total"},
			Levels: []*querierv1.Level{{Values: []int64{0, 1, 0, 0, 1, 0, 1}}},
		},
		{
			Names: []string{"total", "orphan"},
			Levels: []*querierv1.Level{
				{Values: []int64{0, 1, 0, 0, 1, 0, 0}},
				{Values: []int64{2, 1, 0, 2, 1, 0, 1}},
			},
		},
		{
			Names: []string{"total", "overflow"},
			Levels: []*querierv1.Level{
				{Values: []int64{0, math.MaxInt64, 0, 0, 1, 0, 0}},
				{Values: []int64{math.MaxInt64, 1, 0, 0, 1, 0, 1}},
			},
		},
	}
	for index, graph := range tests {
		if _, err := profileDiffRows(graph); err == nil {
			t.Fatalf("profileDiffRows(test %d) succeeded", index)
		}
	}
}

func TestProfileDiffRowsLimitsTotalNodes(t *testing.T) {
	level := &querierv1.Level{
		Values: make(
			[]int64,
			0,
			profileService.MaxProfileNodes*diffFlamegraphNodeWidth,
		),
	}
	for range profileService.MaxProfileNodes {
		level.Values = append(level.Values, 0, 0, 0, 0, 0, 0, 0)
	}

	_, err := profileDiffRows(&querierv1.FlameGraphDiff{
		Names: []string{"total"},
		Levels: []*querierv1.Level{
			{Values: []int64{0, 1, 0, 0, 1, 0, 0}},
			level,
		},
	})
	if err == nil {
		t.Fatal("profileDiffRows() accepted more than the node limit")
	}
}

func TestProfileDiffResponseSizeLimit(t *testing.T) {
	tooLarge, err := profileDiffResponseExceedsLimit([]profileDiffRow{{
		Label: strings.Repeat("x", maxProfileDiffResponseBytes),
	}})
	if err != nil {
		t.Fatalf("profileDiffResponseExceedsLimit() error = %v", err)
	}
	if !tooLarge {
		t.Fatal("profile diff response exceeded the byte limit")
	}
}

func BenchmarkProfileDiffParent(b *testing.B) {
	const parentCount = 10_000
	parents := make([]*profileDiffNode, parentCount)
	for index := range parents {
		start := int64(index * 2)
		parents[index] = &profileDiffNode{
			row:        profileDiffRow{Value: 2, ValueRight: 2},
			leftStart:  start,
			rightStart: start,
		}
	}
	child := &profileDiffNode{
		row: profileDiffRow{
			Value:      1,
			ValueRight: 1,
		},
		leftStart:  int64((parentCount - 1) * 2),
		rightStart: int64((parentCount - 1) * 2),
	}

	b.Run("binary", func(b *testing.B) {
		for range b.N {
			if profileDiffParent(parents, child) == nil {
				b.Fatal("parent not found")
			}
		}
	})
	b.Run("linear_reference", func(b *testing.B) {
		for range b.N {
			var found *profileDiffNode
			for _, parent := range parents {
				if profileDiffRangeContains(
					parent.leftStart,
					parent.row.Value,
					child.leftStart,
					child.row.Value,
				) {
					found = parent
					break
				}
			}
			if found == nil {
				b.Fatal("parent not found")
			}
		}
	})
}
