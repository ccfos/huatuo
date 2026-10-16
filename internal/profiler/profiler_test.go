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
	"slices"
	"testing"
	"time"
)

func TestParseRawDataStripsPySpyStatusLinesFromMultiProcessOutput(t *testing.T) {
	data, err := json.Marshal([]SampleOutput{{
		PID:    10,
		Output: "py-spy> Sampling process 10\nworker;run 1\n",
	}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	profile, err := ParseRawData(context.Background(), &ParseInput{
		StartTime:   time.Unix(0, 0),
		ProfileType: ProfileTypeCpuSample,
		Data:        data,
	})
	if err != nil {
		t.Fatalf("ParseRawData() error = %v", err)
	}
	if slices.Contains(profile.Profile.StringTable, "py-spy> Sampling process") {
		t.Fatalf("profile contains py-spy status frame: %q", profile.Profile.StringTable)
	}
}
