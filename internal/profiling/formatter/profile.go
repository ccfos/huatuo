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
	"context"
	"strconv"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

// DisplayLabels projects labels only at the presentation boundary. Quoting
// prevents multi-value and numeric labels from becoming ambiguous prefixes.
func DisplayLabels(dst []string, labels []types.ProfileLabel) []string {
	for i := range labels {
		dst = append(dst, DisplayLabel(&labels[i], labels))
	}
	return dst
}

// DisplayLabel formats a label using the full sample label set for IRQ vector names.
func DisplayLabel(label *types.ProfileLabel, labels []types.ProfileLabel) string {
	if label.Kind == types.ProfileLabelNumber {
		return label.Name + "=" + strconv.FormatInt(label.NumberValue, 10) + " " + strconv.Quote(label.NumberUnit)
	}

	value := label.StringValue
	if label.Name == "irq_vector" {
		for i := range labels {
			if labels[i].Name != "irq_type" {
				continue
			}
			if labels[i].StringValue == "softirq" {
				names := [...]string{"HI", "TIMER", "NET_TX", "NET_RX", "BLOCK", "IRQ_POLL", "TASKLET", "SCHED", "HRTIMER", "RCU"}
				n, err := strconv.ParseUint(value, 10, 32)
				if err == nil && n < uint64(len(names)) {
					value = names[n]
				}
			}
			break
		}
	}
	return label.Name + "=" + strconv.Quote(value)
}

// WalkWindow creates display frames without changing shared frame identities.
func WalkWindow(ctx context.Context, w *types.AggregationWindow, visit func([]string, int64) error) error {
	var path []string
	return WalkSamples(ctx, &w.Aggregated, func(frames []string, labels []types.ProfileLabel, weight int64) error {
		path = append(DisplayLabels(path[:0], labels), frames...)
		return visit(path, weight)
	})
}

// WalkSamples passes each root-first stack separately from its labels. The
// callback borrows frames and labels only for the duration of the call.
func WalkSamples(
	ctx context.Context,
	a *types.AggregatedProfile,
	visit func([]string, []types.ProfileLabel, int64) error,
) error {
	var frames []string
	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		sample := &a.Samples[i]
		frames = frames[:0]
		if sample.UserStack != 0 {
			for _, id := range a.Stacks[sample.UserStack-1] {
				frames = append(frames, a.Frames[id].Name)
			}
		}

		if sample.KernelStack != 0 {
			for _, id := range a.Stacks[sample.KernelStack-1] {
				frames = append(frames, a.Frames[id].Name+"_[k]")
			}
		}

		if sample.UserStack == 0 && sample.KernelStack == 0 {
			frames = append(frames, "[unknown]")
		}

		if err := visit(frames, window.Labels(a, sample), sample.Value); err != nil {
			return err
		}
	}

	return nil
}
