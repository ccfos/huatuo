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

// Package chrometrace writes aggregated stacks as Chrome Trace Event JSON.
package chrometrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter places aggregate stacks consecutively on an estimated timeline.
// TODO: preserve observation timestamps before aggregation to recover chronology.
type formatter struct {
	events      []event
	endUS       int64
	destination *output.LocalDestination
}

type event struct {
	Name        string            `json:"name"`
	Phase       string            `json:"ph"`
	PID         int64             `json:"pid"`
	TID         int64             `json:"tid"`
	TimestampUS int64             `json:"ts"`
	DurationUS  *int64            `json:"dur,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
}

const maxExactInteger int64 = 1<<53 - 1

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes chrometrace output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatChromeTrace
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{destination: destination}, nil
}

func (f *formatter) Add(ctx context.Context, window *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(window.Aggregated.Samples) == 0 {
		return nil
	}

	parts, err := profilewindow.Metadata(window.ProfileType)
	if err != nil {
		return err
	}

	unit := parts[2]

	multiplier, divisor := int64(1), int64(1)
	switch unit {
	case "nanoseconds":
		divisor = 1000
	case "microseconds":
	case "milliseconds":
		multiplier = 1000
	case "seconds":
		multiplier = 1000000
	default:
		return fmt.Errorf("chrometrace: cannot estimate duration from unit %q", unit)
	}

	return output.WalkWindow(ctx, window, func(frames []string, value int64) error {
		if value < 0 {
			return fmt.Errorf("chrometrace: negative sample weight %d", value)
		}

		if value == 0 || len(frames) == 0 {
			return nil
		}

		duration := value / divisor
		if duration > (maxExactInteger-f.endUS)/multiplier {
			return fmt.Errorf("chrometrace: estimated timeline exceeds 2^53-1 microseconds")
		}

		duration *= multiplier
		if len(f.events) == 0 {
			f.events = append(f.events, event{
				Name:  "thread_name",
				Phase: "M",
				Args:  map[string]string{"name": "aggregated (estimated)"},
			})
		}

		for _, name := range frames {
			f.events = append(f.events, event{
				Name:        name,
				Phase:       "X",
				TimestampUS: f.endUS,
				DurationUS:  &duration,
			})
		}

		f.endUS += duration

		return nil
	})
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(f.events) == 0 && !f.destination.IsStdout() {
		return nil
	}

	return f.destination.WriteAndClose(func(w io.Writer) error {
		events := f.events
		if events == nil {
			events = []event{}
		}

		return json.NewEncoder(w).Encode(struct {
			Events []event `json:"traceEvents"`
		}{Events: events})
	})
}

func (f *formatter) Close() error { return f.destination.Close() }
