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

package main

import "testing"

func TestIocbDirectBit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		major, minor int
		want         uint32
	}{
		{4, 18, iocbDirectLegacy},
		{5, 4, iocbDirectLegacy},
		{5, 9, iocbDirectLegacy},
		{5, 10, iocbDirectModern},
		{5, 15, iocbDirectModern},
		{6, 1, iocbDirectModern},
		{6, 6, iocbDirectModern},
		{7, 0, iocbDirectModern},
	}

	for _, tc := range cases {
		if got := iocbDirectBit(tc.major, tc.minor); got != tc.want {
			t.Errorf("iocbDirectBit(%d, %d) = %#x, want %#x",
				tc.major, tc.minor, got, tc.want)
		}
	}
}

func TestParseRelease(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in        string
		wantMajor int
		wantMinor int
	}{
		{"5.10.0-957.el7.x86_64", 5, 10},
		{"4.18.0-193.6.3.el8_2.x86_64", 4, 18},
		{"6.18.33.2-microsoft-standard-WSL2", 6, 18},
		{"5.4", 5, 4},
		{"garbage", 0, 0},
		{"", 0, 0},
	}

	for _, tc := range cases {
		major, minor := parseRelease(tc.in)
		if major != tc.wantMajor || minor != tc.wantMinor {
			t.Errorf("parseRelease(%q) = (%d, %d), want (%d, %d)",
				tc.in, major, minor, tc.wantMajor, tc.wantMinor)
		}
	}
}
