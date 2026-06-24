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

package timeutil_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"huatuo-bamai/internal/timeutil"
)

const layoutLen = 30 // "2006-01-02T15:04:05.000000000Z"

var layoutRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`)

func TestFormatUTC_NormalizesToZ(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	local := time.Date(2026, 6, 14, 20, 0, 0, 0, cst)
	got := timeutil.FormatUTC(local)
	require.Equal(t, "2026-06-14T12:00:00.000000000Z", got)
}

func TestFormatUTC_FixedWidth(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),           // whole second
		time.Date(2026, 6, 14, 12, 0, 0, 1, time.UTC),           // 1 ns
		time.Date(2026, 6, 14, 12, 0, 0, 123_000_000, time.UTC), // ms
		time.Date(2026, 6, 14, 12, 0, 0, 123_456_000, time.UTC), // µs
		time.Date(2026, 6, 14, 12, 0, 0, 123_456_789, time.UTC), // ns
		time.Date(2026, 6, 14, 12, 0, 0, 999_999_999, time.UTC), // ns max
	}
	for _, tt := range cases {
		got := timeutil.FormatUTC(tt)
		require.Len(t, got, layoutLen, "value=%v output=%q", tt, got)
		require.Regexp(t, layoutRe, got)
	}
}

func TestFormatUTC_PreservesNanos(t *testing.T) {
	in := time.Date(2026, 6, 14, 12, 0, 0, 123_456_789, time.UTC)
	require.Equal(t, "2026-06-14T12:00:00.123456789Z", timeutil.FormatUTC(in))
}

func TestFormatUTC_LexicalEqualsChronological(t *testing.T) {
	earlier := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(1 * time.Nanosecond)
	a := timeutil.FormatUTC(earlier)
	b := timeutil.FormatUTC(later)
	require.True(t, earlier.Before(later))
	require.Less(t, a, b, "lexical order must follow chronological order")
}

func TestFormatUTC_ZeroValue(t *testing.T) {
	got := timeutil.FormatUTC(time.Time{})
	require.Equal(t, "0001-01-01T00:00:00.000000000Z", got)
}

func TestParse_AcceptsAllRFC3339Precisions(t *testing.T) {
	wantInstant := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)

	cases := []string{
		"2026-06-14T12:00:00Z",
		"2026-06-14T12:00:00.0Z",
		"2026-06-14T12:00:00.000Z",
		"2026-06-14T12:00:00.000000Z",
		"2026-06-14T12:00:00.000000000Z",
	}
	for _, s := range cases {
		got, err := timeutil.Parse(s)
		require.NoError(t, err, "input=%q", s)
		require.True(t, got.Equal(wantInstant), "input=%q got=%v", s, got)
	}
}

func TestParse_NormalizesOffsetToUTC(t *testing.T) {
	got, err := timeutil.Parse("2026-06-14T20:00:00.000000000+08:00")
	require.NoError(t, err)
	require.Equal(t, time.UTC, got.Location())
	require.Equal(
		t,
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		got,
	)
}

func TestParse_RoundTrip(t *testing.T) {
	want := time.Date(2026, 6, 14, 12, 34, 56, 789_012_345, time.UTC)
	s := timeutil.FormatUTC(want)
	got, err := timeutil.Parse(s)
	require.NoError(t, err)
	require.True(t, got.Equal(want))
}

func TestParse_RejectsGarbage(t *testing.T) {
	cases := []string{
		"",
		"not a time",
		"2026-06-14 12:00:00", // legacy storage format with space, no timezone
		"2026/06/14T12:00:00Z",
	}
	for _, s := range cases {
		_, err := timeutil.Parse(s)
		require.Error(t, err, "should reject %q", s)
	}
}
