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

// Package dump writes aggregate stacks as readable text.
package dump

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

type entry struct {
	frames []string
	value  int64
}

type formatter struct {
	destination *output.LocalDestination
	entries     []entry
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes a text dump destination.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatDump
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}
	return &formatter{destination: destination}, nil
}

func (*formatter) Name() output.OutputFormat { return output.FormatDump }

func (f *formatter) Add(ctx context.Context, window *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return output.WalkWindow(ctx, window, func(frames []string, value int64) error {
		// WalkWindow reuses its frame buffer for the next sample.
		f.entries = append(f.entries, entry{frames: append([]string(nil), frames...), value: value})
		return nil
	})
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(f.entries) == 0 && !f.destination.IsStdout() {
		return nil
	}
	return f.destination.WriteAndClose(func(writer io.Writer) error {
		for i := range f.entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := &f.entries[i]
			if _, err := fmt.Fprintf(writer, "Sample %d [value=%d]\n", i+1, item.value); err != nil {
				return err
			}
			for _, frame := range item.frames {
				if strings.ContainsAny(frame, "\r\n") {
					frame = strconv.Quote(frame)
				}
				if _, err := fmt.Fprintf(writer, "    %s\n", frame); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(writer); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *formatter) Close() error { return f.destination.Close() }
