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
	"os"
	"testing"
	"time"
)

func TestParseWindowsPreservesSamplesAroundEmptyWindows(t *testing.T) {
	for name, parse := range map[string]func(context.Context, *ParseInput) (*ProfileData, error){
		"collapsed": ParseCollapsedData,
		"raw":       ParseRawData,
	} {
		t.Run(name, func(t *testing.T) {
			for _, emptyOnly := range []bool{false, true} {
				outputs := []SampleOutput{{PID: os.Getpid()}}
				want := int64(0)
				if !emptyOnly {
					outputs = append(outputs, SampleOutput{PID: os.Getpid(), Output: "work;allocate 4096\n"}, SampleOutput{PID: os.Getpid()})
					want = 4096
				}
				data, err := json.Marshal(outputs)
				if err != nil {
					t.Fatal(err)
				}
				profile, err := parse(t.Context(), &ParseInput{
					StartTime: time.Now(), ProfileType: ProfileTypeMemSample,
					ProfilerName: name, PID: os.Getpid(), Data: data,
					Opt: &ParseOption{SampleRate: NoSampleRate},
				})
				if err != nil {
					t.Fatal(err)
				}
				var total int64
				for _, sample := range profile.Profile.Sample {
					total += sample.Value[0]
				}
				if total != want {
					t.Fatalf("emptyOnly=%t: sample total=%d, want %d", emptyOnly, total, want)
				}
			}
		})
	}
}
