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

package profile

import (
	"strings"
	"testing"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
	"google.golang.org/protobuf/proto"
)

func TestParseTreePreservesProfileContract(t *testing.T) {
	items := []*TreeItem{
		BuildTreeItem(nil, []string{"root", "work"}, nil, 7),
		BuildTreeItem(nil, []string{"root", "work"}, nil, 3),
		BuildTreeItem(nil, []string{"root"}, nil, 2),
		BuildTreeItem(nil, []string{"root", "root", ""}, nil, 5),
		BuildTreeItem(nil, nil, nil, 4),
		BuildTreeItem(nil, []string{"zero"}, nil, 0),
	}
	for _, profileType := range []string{
		ProfileTypeCpuSample, ProfileTypeOffCpuSample, ProfileTypeMemSample,
		ProfileTypeLockCountSample, ProfileTypeLockTimeSample, ProfileTypeIRQTracingSample,
	} {
		for _, empty := range []bool{false, true} {
			name := profileType
			if empty {
				name += "/empty"
			}
			t.Run(name, func(t *testing.T) {
				input := items
				if empty {
					input = nil
				}
				rate := int64(0)
				if profileType == ProfileTypeCpuSample {
					rate = 100
				}
				start := time.Unix(123, 456)
				got, err := ParseTree(start, profileType, input, &ParseOption{SampleRate: rate})
				if err != nil {
					t.Fatal(err)
				}
				// The third-party encoder is a compatibility oracle for the existing
				// wire contract, including empty stacks, repeated frames and string IDs.
				tree := ptree.New()
				for _, item := range input {
					tree.InsertStack(item.Stack, item.Value)
				}
				scale := uint64(1)
				if rate > 0 {
					scale = uint64(time.Second.Nanoseconds() / rate)
				}
				tree.Scale(scale)
				fields := strings.Split(profileType, ":")
				old := tree.Pprof(&ptree.PprofMetadata{Type: fields[1], Unit: fields[2], PeriodType: fields[3], PeriodUnit: fields[4], Period: int64(scale), StartTime: start})
				encoded, err := old.MarshalVT()
				if err != nil {
					t.Fatal(err)
				}
				want := new(profilev1.Profile)
				if err := want.UnmarshalVT(encoded); err != nil {
					t.Fatal(err)
				}
				if got.ProfileType != profileType || !proto.Equal(got.Profile, want) {
					t.Fatalf("profile differs from existing wire contract:\ngot %v\nwant %v", got.Profile, want)
				}
			})
		}
	}
}
