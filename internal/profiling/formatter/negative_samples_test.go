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
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestClampNegativeSamplesBoundsDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })
	profile := types.AggregatedProfile{
		TimeNanos:     123,
		DurationNanos: 456,
		Frames:        []types.ProfileFrame{{Name: strings.Repeat("x", 10000)}},
		Stacks:        [][]uint32{{0}},
	}

	for range 100 {
		profile.Samples = append(profile.Samples, types.AggregatedSample{
			UserStack: 1,
			Value:     -4096,
		})
	}

	profile.Samples = append(profile.Samples, types.AggregatedSample{Value: 8192})
	ClampNegativeSamples("task-test", &profile)
	for i := range 100 {
		if profile.Samples[i].Value != 0 {
			t.Fatal("negative output retained")
		}
	}

	if profile.Samples[100].Value != 8192 {
		t.Fatal("positive output changed")
	}

	if profile.TimeNanos != 123 || profile.DurationNanos != 456 {
		t.Fatal("clamping changed observation timestamps")
	}

	text := logs.String()
	if strings.Contains(text, "time_nanos") {
		t.Fatalf("warning exposes a misleading profile timestamp: %s", text)
	}

	if strings.Count(text, `level="warning"`) != 1 || !strings.Contains(text, "task-test") || !strings.Contains(text, `negative_samples="100"`) || len(text) > 4096 {
		t.Fatalf("unbounded or incomplete warning: %s", text)
	}

	logs.Reset()
	ClampNegativeSamples("task-test", &profile)
	if logs.Len() != 0 {
		t.Fatal("nonnegative output warned")
	}
}
