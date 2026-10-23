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
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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
	if err := ClampNegativeSamples(t.Context(), "task-test", &profile); err != nil {
		t.Fatal(err)
	}
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
	if err := ClampNegativeSamples(t.Context(), "task-test", &profile); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatal("nonnegative output warned")
	}
}

func TestFormatNegativeSampleSkipsInvalidReferences(t *testing.T) {
	profile := types.AggregatedProfile{
		Frames:    []types.ProfileFrame{{Name: "valid"}},
		Stacks:    [][]uint32{{0, 99}},
		LabelSets: [][]types.ProfileLabel{{types.ProfileStringLabel("key", "value")}},
	}
	sample := types.AggregatedSample{UserStack: 1, KernelStack: 99, LabelSet: 99, Value: -1}

	if got, want := formatNegativeSample(&profile, &sample), "value=-1 frame=valid"; got != want {
		t.Fatalf("formatNegativeSample() = %q, want %q", got, want)
	}
}

func TestClampNegativeSamplesRejectsCanceledContext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline bool
	}{
		{name: "canceled"},
		{
			name:     "deadline",
			deadline: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
				want = context.DeadlineExceeded
			}
			cancel()
			profile := types.AggregatedProfile{Samples: []types.AggregatedSample{{Value: -1}}}
			if err := ClampNegativeSamples(ctx, "canceled", &profile); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if profile.Samples[0].Value != -1 {
				t.Fatal("canceled call modified samples")
			}
			if err := ClampNegativeSamples(ctx, "empty"); !errors.Is(err, want) {
				t.Fatalf("empty call error = %v, want %v", err, want)
			}
		})
	}
}

// Cancel after observing progress, without timing-dependent goroutines.
type clampProgressContext struct {
	context.Context
	cancel  context.CancelFunc
	samples []types.AggregatedSample
}

func (c *clampProgressContext) Err() error {
	if c.samples[0].Value == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestClampNegativeSamplesStopsDuringScan(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })
	profile := types.AggregatedProfile{Samples: make([]types.AggregatedSample, 4096)}
	for i := range profile.Samples {
		profile.Samples[i].Value = -1
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	progress := &clampProgressContext{
		Context: ctx,
		cancel:  cancel,
		samples: profile.Samples,
	}
	if err := ClampNegativeSamples(progress, "canceled", &profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if profile.Samples[0].Value != 0 || profile.Samples[len(profile.Samples)-1].Value != -1 {
		t.Fatal("scan did not stop after partial progress")
	}
	if logs.Len() != 0 {
		t.Fatal("canceled scan emitted a completion warning")
	}
}

func TestFormatNegativeSampleBoundsUnicode(t *testing.T) {
	profile := types.AggregatedProfile{
		Frames: []types.ProfileFrame{{Name: strings.Repeat("界", 200)}},
		Stacks: [][]uint32{{0, 0, 0, 0}},
		LabelSets: [][]types.ProfileLabel{{
			{
				Name:        strings.Repeat("名", 100),
				Kind:        types.ProfileLabelString,
				StringValue: strings.Repeat("值", 200),
			},
			{
				Name:        "size",
				Kind:        types.ProfileLabelNumber,
				NumberValue: 42,
			},
			{
				Name: "empty",
				Kind: types.ProfileLabelString,
			},
			{
				Name: "excluded",
				Kind: types.ProfileLabelString,
			},
		}},
	}
	sample := types.AggregatedSample{
		UserStack:   1,
		KernelStack: 1,
		LabelSet:    1,
		Value:       -7,
	}
	want := "value=-7" + strings.Repeat(" frame="+strings.Repeat("界", 128), 6) +
		" label=" + strings.Repeat("名", 64) + ":" + strings.Repeat("值", 128) + ":0 label=size::42 label=empty::0"
	if got := formatNegativeSample(&profile, &sample); got != want {
		t.Fatalf("diagnostic = %q, want %q", got, want)
	}
	if sample.Value != -7 {
		t.Fatal("formatting modified the sample")
	}
}
