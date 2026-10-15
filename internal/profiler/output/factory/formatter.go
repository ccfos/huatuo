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

// Package factory constructs profiling output formats.
package factory

import (
	"fmt"

	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/output/chrometrace"
	"github.com/ccfos/huatuo/internal/profiler/output/flamegraph"
	"github.com/ccfos/huatuo/internal/profiler/output/json"
	"github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/internal/profiler/output/remotestore"
	"github.com/ccfos/huatuo/internal/profiler/output/speedscope"
)

// NewFormatter initializes one task's output. The caller must Close it on every exit path.
func NewFormatter(cfg output.FormatterConfig) (output.Formatter, error) {
	switch cfg.Format {
	case "", output.FormatCollapsed:
		return raw.NewFormatter(cfg)
	case output.FormatFlameGraph, output.FormatSVG:
		return flamegraph.NewFormatter(cfg)
	case output.FormatJSON:
		return json.NewFormatter(cfg)
	case output.FormatSpeedscope:
		return speedscope.NewFormatter(cfg)
	case output.FormatChromeTrace:
		return chrometrace.NewFormatter(cfg)
	case output.FormatRemoteStore:
		return remotestore.NewFormatter(cfg)
	default:
		return nil, fmt.Errorf("unsupported profiling output format %q", cfg.Format)
	}
}
