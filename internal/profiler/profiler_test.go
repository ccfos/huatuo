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
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDimensions_Enabled exercises the cheap gate that guards the whole
// label-injection path. A zero-value Dimensions must report Enabled() == false
// so existing callers (which never set it) keep byte-identical output.
func TestDimensions_Enabled(t *testing.T) {
	assert.False(t, Dimensions{}.Enabled(), "zero Dimensions must be disabled")

	for _, tc := range []struct {
		name string
		dim  Dimensions
	}{
		{"PID only", Dimensions{PID: true}},
		{"TGID only", Dimensions{TGID: true}},
		{"Cgroup only", Dimensions{Cgroup: true}},
		{"ProcessGroup only", Dimensions{ProcessGroup: true}},
		{"all on", Dimensions{PID: true, TGID: true, Cgroup: true, ProcessGroup: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, tc.dim.Enabled())
		})
	}
}

// TestBuildDimensionLabels covers the canonical pprof form for every
// combination of requested dimensions and available values.
func TestBuildDimensionLabels(t *testing.T) {
	cases := []struct {
		name string
		dim  Dimensions
		pid  int
		tgid int
		cg   string
		pg   string
		want string // "" means nil/empty
	}{
		{
			name: "disabled_returns_nil",
			dim:  Dimensions{},
			want: "",
		},
		{
			name: "PID_only",
			dim:  Dimensions{PID: true},
			pid:  123,
			want: "pid=123",
		},
		{
			name: "PID_skipped_when_zero",
			dim:  Dimensions{PID: true},
			pid:  0,
			want: "",
		},
		{
			name: "all_dimensions",
			dim:  Dimensions{PID: true, TGID: true, Cgroup: true, ProcessGroup: true},
			pid:  123, tgid: 456,
			cg: "/kubepods/burstable/pod-abc",
			pg: "web-tier",
			want: "pid=123;tgid=456;cgroup=/kubepods/burstable/pod-abc;pgroup=web-tier",
		},
		{
			name: "requested_but_unset_values_skipped",
			dim:  Dimensions{PID: true, TGID: true, Cgroup: true, ProcessGroup: true},
			pid:  123, // tgid/cg/pg empty
			want: "pid=123",
		},
		{
			name: "cgroup_with_separator_in_path",
			dim:  Dimensions{Cgroup: true},
			cg:   "/sys/fs/cgroup/foo.slice;bar",
			want: "cgroup=/sys/fs/cgroup/foo.slice;bar",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := buildDimensionLabels(tc.dim, tc.pid, tc.tgid, tc.cg, tc.pg)
			if tc.want == "" {
				assert.Nil(t, got)
			} else {
				assert.Equal(t, tc.want, string(got))
			}
		})
	}
}

// renderProfile serializes a ProfileData to a textual form that contains every
// frame (string keys), so tests can assert on injected labels without depending
// on ptree's internal structure. Uses the same ptree -> pprof text path the
// project itself uses for output.
func renderProfile(t *testing.T, pd *ProfileData) string {
	t.Helper()
	require.NotNil(t, pd)
	// ptree.Profile contains a sync.Mutex, so we must not copy the value via
	// require.NotNil(pd.Profile). ParseTree always constructs the Profile via
	// ptree.New(), so a non-nil *ProfileData is sufficient for these tests.
	// Serialize via proto text format and scan for the injected label.
	// Each frame appears in the string table; we just need the labels to show up.
	var sb strings.Builder
	// ptree.Profile has a String() via proto.Message; that captures the string table.
	sb.WriteString(pd.Profile.String())
	return sb.String()
}

// TestParseCommonData_DimensionInjection_Off verifies the back-compat
// contract: when Dimensions is zero-value, no label is injected and parsing
// succeeds unchanged.
func TestParseCommonData_DimensionInjection_Off(t *testing.T) {
	data := []byte("foo;bar;baz 10\n")
	opt := &ParseOption{}

	pd, err := parseCommonData(context.Background(), time.Unix(0, 0),
		"process_cpu:cpu:nanoseconds:cpu:nanoseconds", data, opt, 4242, SampleDimensions{},
		func(int) (string, error) { return "javaApp", nil })
	require.NoError(t, err)

	rendered := renderProfile(t, pd)
	assert.Contains(t, rendered, "javaApp#4242")
	assert.NotContains(t, rendered, "pid=4242")
	assert.NotContains(t, rendered, "cgroup=")
}

// TestParseCommonData_DimensionInjection_On covers the opt-in path: with
// Dimensions.PID and Dimensions.Cgroup set, the first frame gains the
// pprof-style labels after the existing header, separated by ';'.
func TestParseCommonData_DimensionInjection_On(t *testing.T) {
	data := []byte("foo;bar;baz 10\n")
	opt := &ParseOption{Dimensions: Dimensions{PID: true, Cgroup: true}}
	sampleDims := SampleDimensions{
		CgroupPath: "/kubepods/burstable/pod-abc",
	}

	pd, err := parseCommonData(context.Background(), time.Unix(0, 0),
		"process_cpu:cpu:nanoseconds:cpu:nanoseconds", data, opt, 4242, sampleDims,
		func(int) (string, error) { return "javaApp", nil })
	require.NoError(t, err)

	rendered := renderProfile(t, pd)
	assert.Contains(t, rendered, "javaApp#4242;pid=4242;cgroup=/kubepods/burstable/pod-abc")
}

// TestParseCommonData_DimensionInjection_NilOpt guards against a nil
// ParseOption — the original code already tolerated nil opt, so the new
// dimension path must too.
func TestParseCommonData_DimensionInjection_NilOpt(t *testing.T) {
	data := []byte("foo;bar;baz 10\n")

	pd, err := parseCommonData(context.Background(), time.Unix(0, 0),
		"process_cpu:cpu:nanoseconds:cpu:nanoseconds", data, nil, 4242, SampleDimensions{},
		func(int) (string, error) { return "javaApp", nil })
	require.NoError(t, err)

	rendered := renderProfile(t, pd)
	assert.Contains(t, rendered, "javaApp#4242")
	assert.NotContains(t, rendered, "pid=")
}

// TestParseRawData_MultiPID_DimensionInjection verifies the multi-PID path:
// each PID's header frame picks up its own SampleOutput dimensions. Uses
// ParseRawData (the py-spy path) because it doesn't shell out to /proc for
// thread-name resolution, so the test is hermetic.
func TestParseRawData_MultiPID_DimensionInjection(t *testing.T) {
	outputs := []SampleOutput{
		{PID: 100, Output: "foo;bar 5\n", TGID: 1000, CgroupPath: "/cg/a"},
		{PID: 200, Output: "baz;qux 7\n", TGID: 2000, CgroupPath: "/cg/b"},
	}
	data, err := json.Marshal(outputs)
	require.NoError(t, err)

	opt := &ParseOption{Dimensions: Dimensions{PID: true, TGID: true, Cgroup: true}}
	input := &ParseInput{
		StartTime:   time.Unix(0, 0),
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Data:        data,
		Opt:         opt,
	}

	pd, err := ParseRawData(context.Background(), input)
	require.NoError(t, err)
	rendered := renderProfile(t, pd)

	assert.Contains(t, rendered, "pid=100;tgid=1000;cgroup=/cg/a")
	assert.Contains(t, rendered, "pid=200;tgid=2000;cgroup=/cg/b")
}

// TestSampleOutput_JSONRoundTrip guards the JSON shape: existing fields keep
// their names, new dimension fields are omitempty so old data unmarshals clean.
func TestSampleOutput_JSONRoundTrip(t *testing.T) {
	t.Run("legacy_data_unmarshals_without_new_fields", func(t *testing.T) {
		legacy := []byte(`{"pid":42,"output":"foo;bar 1"}`)
		var s SampleOutput
		require.NoError(t, json.Unmarshal(legacy, &s))
		assert.Equal(t, 42, s.PID)
		assert.Equal(t, "foo;bar 1", s.Output)
		assert.Zero(t, s.TGID)
		assert.Empty(t, s.CgroupPath)
		assert.Empty(t, s.ProcessGroup)
	})

	t.Run("new_fields_round_trip", func(t *testing.T) {
		s := SampleOutput{PID: 42, Output: "x", TGID: 4200, CgroupPath: "/cg", ProcessGroup: "web"}
		out, err := json.Marshal(s)
		require.NoError(t, err)
		assert.Contains(t, string(out), `"tgid":4200`)
		assert.Contains(t, string(out), `"cgroup_path":"/cg"`)
		assert.Contains(t, string(out), `"process_group":"web"`)
	})

	t.Run("zero_dimension_fields_omitted", func(t *testing.T) {
		s := SampleOutput{PID: 42, Output: "x"}
		out, err := json.Marshal(s)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "tgid")
		assert.NotContains(t, string(out), "cgroup_path")
		assert.NotContains(t, string(out), "process_group")
	})
}

// TestParseRawData_LegacyInput_Unchanged is the contract guard: when
// ParseOption.Dimensions is not set, output frames are byte-for-byte what
// they were before this change, regardless of what dimension values ride
// along on the sample.
func TestParseRawData_LegacyInput_Unchanged(t *testing.T) {
	// Sample carries dimension values, but opt.Dimensions is disabled.
	outputs := []SampleOutput{
		{PID: 100, Output: "foo;bar 5\n", TGID: 1000, CgroupPath: "/cg/a"},
	}
	data, err := json.Marshal(outputs)
	require.NoError(t, err)

	opt := &ParseOption{} // Dimensions not set
	input := &ParseInput{
		StartTime:   time.Unix(0, 0),
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Data:        data,
		Opt:         opt,
	}

	pd, err := ParseRawData(context.Background(), input)
	require.NoError(t, err)
	rendered := renderProfile(t, pd)

	// No labels should appear anywhere in the output.
	assert.NotContains(t, rendered, "pid=100")
	assert.NotContains(t, rendered, "cgroup=")
	assert.NotContains(t, rendered, "tgid=")
}
