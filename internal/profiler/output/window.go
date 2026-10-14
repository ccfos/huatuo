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
	"io"

	"github.com/ccfos/huatuo/internal/toolstream"
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

// NewFormatter binds output resources without taking ownership of their lifetime.
func NewFormatter(format OutputFormat, writer io.Writer, toolstreamClient *toolstream.Client, finish func(Renderer) error) (Formatter, error) {
	var formatter Formatter
	switch format {
	case FormatRemoteStore:
		if toolstreamClient == nil {
			return nil, errors.New("remote store formatter requires toolstream client")
		}
		formatter = &remoteStoreFormatter{toolstreamClient: toolstreamClient}
	case FormatJSON:
		if writer == nil {
			return nil, errors.New("JSON formatter requires writer")
		}
		formatter = &jsonFormatter{writer: writer}
	default:
		renderer, err := format.NewRenderer()
		if err != nil {
			return nil, err
		}
		if writer == nil && finish == nil {
			return nil, errors.New("local formatter requires output destination")
		}
		formatter = &localFormatter{renderer: renderer, writer: writer, finish: finish}
	}
	return &windowFormatter{formatter: formatter}, nil
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
