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

package output_test

import (
	"errors"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
	_ "github.com/ccfos/huatuo/internal/profiler/output/chrometrace"
	_ "github.com/ccfos/huatuo/internal/profiler/output/dump"
	_ "github.com/ccfos/huatuo/internal/profiler/output/speedscope"
)

// The three profile viewers were written but never registered, so the
// OutputFormat enum could not reach them. Importing the packages must
// populate the registry.
func TestFileBackedFormatsAreRegistered(t *testing.T) {
	tests := []struct {
		format output.OutputFormat
		name   string
	}{
		{output.FormatSpeedscope, "speedscope"},
		{output.FormatChromeTrace, "chrometrace"},
		{output.FormatDump, "dump"},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			formatter, err := tt.format.NewFormatter()
			if err != nil {
				t.Fatalf("NewFormatter(%q) error = %v", tt.format, err)
			}
			if formatter == nil {
				t.Fatalf("NewFormatter(%q) = nil", tt.format)
			}
			if got := formatter.Name(); got != tt.name {
				t.Fatalf("Name() = %q, want %q", got, tt.name)
			}
		})
	}
}

func TestFileBackedFormatsAreLocalOnly(t *testing.T) {
	for _, format := range []output.OutputFormat{
		output.FormatSpeedscope,
		output.FormatChromeTrace,
		output.FormatDump,
	} {
		if format.IsUpload() {
			t.Errorf("%q must not upload to a remote backend", format)
		}
		if format.IsFlameGraph() {
			t.Errorf("%q must not be treated as a flame graph", format)
		}
	}
}

func TestNewFormatterRejectsUnregisteredFormat(t *testing.T) {
	formatter, err := output.FormatPprof.NewFormatter()
	if !errors.Is(err, output.ErrUnregisteredFormat) {
		t.Fatalf("NewFormatter(pprof) error = %v, want ErrUnregisteredFormat", err)
	}
	if formatter != nil {
		t.Fatalf("NewFormatter(pprof) = %v, want nil", formatter)
	}
}
