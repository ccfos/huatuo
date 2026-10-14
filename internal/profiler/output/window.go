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

package output

import (
	"context"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/pkg/types"
)

// Formatter receives complete windows and finalizes one collection task.
// Calls are serialized by the collection pipeline; instances are not reused.
type Formatter interface {
	Add(ctx context.Context, window *types.ProfilingWindow) error
	Complete(ctx context.Context) error
}

// windowFormatter keeps the task lifecycle identical for every destination.
type windowFormatter struct {
	formatter Formatter
	completed bool
	err       error
}

// FormatterConfig selects the representation and its filesystem destination.
// Existing paths retain their type. A missing path denotes a file unless it
// ends in a separator, which requests a directory. "stdout" or "-" denotes standard output.
type FormatterConfig struct {
	Format      OutputFormat
	Destination string
}

// NewFormatter initializes output before collection starts. The caller must
// invoke cleanup on both success and failure; cleanup never commits a task.
func NewFormatter(cfg FormatterConfig, taskID, toolName string) (Formatter, func() error, error) {
	if err := ValidateDestination(cfg.Destination); err != nil {
		return nil, nil, err
	}
	var formatter Formatter
	cleanup := func() error { return nil }
	if cfg.Format == FormatRemoteStore {
		remote, err := newRemoteStoreFormatter(cfg.Destination, taskID, toolName)
		if err != nil {
			return nil, nil, err
		}
		formatter = remote
		cleanup = remote.toolstreamClient.Close
	} else {
		var renderer Renderer
		if cfg.Format != FormatJSON {
			var err error
			renderer, err = cfg.Format.NewRenderer()
			if err != nil {
				return nil, nil, err
			}
		}
		destination, err := newLocalDestination(cfg)
		if err != nil {
			return nil, nil, err
		}
		if cfg.Format == FormatJSON {
			formatter = &jsonFormatter{destination: destination}
			cleanup = destination.close
		} else {
			formatter = &localFormatter{renderer: renderer, destination: destination}
		}
	}
	return &windowFormatter{formatter: formatter}, cleanup, nil
}

func (f *windowFormatter) Add(ctx context.Context, window *types.ProfilingWindow) error {
	if f.completed {
		return errors.New("formatter already completed")
	}
	if f.err != nil {
		return f.err
	}
	if err := ctx.Err(); err != nil {
		f.err = err
		return err
	}
	if window == nil || window.Profile == nil {
		f.err = errors.New("profiling window and profile are required")
		return f.err
	}
	f.err = f.formatter.Add(ctx, window)
	return f.err
}

func (f *windowFormatter) Complete(ctx context.Context) error {
	if f.completed {
		return f.err
	}
	f.completed = true
	if f.err != nil {
		return f.err
	}
	if err := ctx.Err(); err != nil {
		f.err = err
		return err
	}
	f.err = f.formatter.Complete(ctx)
	if f.err != nil {
		f.err = fmt.Errorf("complete profiling output: %w", f.err)
	}
	return f.err
}
