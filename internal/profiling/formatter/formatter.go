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

// Formatter borrows immutable windows until Add returns. Add errors are not
// retried. The runner calls Complete at most once, after collection, collector
// cleanup, and pipeline processing all succeed. The runner calls Close on every
// exit path after workers finish; Close releases resources without publishing.
// Add and Complete return context.Canceled or context.DeadlineExceeded unwrapped
// when cancellation or timeout is the only failure. Independent I/O or cleanup
// failures must remain visible, including when joined with cancellation.
type Formatter interface {
	Add(ctx context.Context, window *types.AggregationWindow) error
	Complete(ctx context.Context) error
	Close() error
}

// Config selects the representation and its filesystem destination.
// Existing paths retain their type. A missing path denotes a file unless it
// ends in a separator, which requests a directory. "stdout" or "-" denotes standard output.
type Config struct {
	Format      OutputFormat
	Destination string
	TaskID      string
	ToolName    string
}
