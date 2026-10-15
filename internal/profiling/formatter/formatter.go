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

// Package formatter defines profiling output contracts.
package formatter

import (
	"context"

	"github.com/ccfos/huatuo/pkg/types"
)

// OutputFormat enumerates the supported profiling output formats.
// The zero value "" is treated as FormatCollapsed.
type OutputFormat string

const (
	FormatCollapsed   OutputFormat = "collapsed"
	FormatFlameGraph  OutputFormat = "flamegraph"
	FormatSVG         OutputFormat = "svg"
	FormatJSON        OutputFormat = "json"
	FormatSpeedscope  OutputFormat = "speedscope"
	FormatChromeTrace OutputFormat = "chrometrace"
	FormatRemoteStore OutputFormat = "remote-store" // upload to a remote storage backend
)

// Formatter receives complete windows and finalizes one collection task.
// The owner serializes calls and stops on the first error. Add borrows a non-nil
// window with a non-nil profile until return. Complete is called at most once,
// after successful Adds. Close must be called once on every exit path, including
// after Complete; it releases resources without completing output. Instances are
// not reused, and repeated Complete or Close calls are not guaranteed.
type Formatter interface {
	Add(ctx context.Context, window *types.ProfilingWindow) error
	Complete(ctx context.Context) error
	Close() error
}

// FormatterConfig selects the representation and its filesystem destination.
// Existing paths retain their type. A missing path denotes a file unless it
// ends in a separator, which requests a directory. "stdout" or "-" denotes standard output.
type FormatterConfig struct {
	Format      OutputFormat
	Destination string
	TaskID      string
	ToolName    string
}
