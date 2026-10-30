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

package formatter

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

func BenchmarkClampNegativeSamples(b *testing.B) {
	log.SetOutput(io.Discard)
	previousLevel := log.GetLevel().String()
	log.SetLevel("warn")
	b.Cleanup(func() {
		log.SetOutput(os.Stdout)
		log.SetLevel(previousLevel)
	})

	for _, tc := range []struct {
		name  string
		value int64
	}{
		{
			name:  "nonnegative",
			value: 1,
		},
		{
			name:  "negative",
			value: -1,
		},
	} {
		b.Run(tc.name, func(b *testing.B) {
			profile := types.AggregatedProfile{
				Frames: []types.ProfileFrame{{Name: strings.Repeat("frame", 40)}},
				Stacks: [][]uint32{{0, 0, 0}},
				LabelSets: [][]types.ProfileLabel{{
					{
						Name:        "label1",
						Kind:        types.ProfileLabelString,
						StringValue: strings.Repeat("value", 40),
					},
					{
						Name:        "label2",
						Kind:        types.ProfileLabelString,
						StringValue: "value2",
					},
					{
						Name:        "label3",
						Kind:        types.ProfileLabelNumber,
						NumberValue: 42,
					},
				}},
				Samples: make([]types.AggregatedSample, 4096),
			}
			for i := range profile.Samples {
				profile.Samples[i] = types.AggregatedSample{
					UserStack:   1,
					KernelStack: 1,
					LabelSet:    1,
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				for i := range profile.Samples {
					profile.Samples[i].Value = tc.value
				}
				if err := ClampNegativeSamples(b.Context(), "benchmark", &profile); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
