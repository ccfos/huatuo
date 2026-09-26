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
	"errors"
	"io"
	"testing"
)

type testFormatter struct{}

func (testFormatter) Name() string          { return "test" }
func (testFormatter) Add(*Sample) error     { return nil }
func (testFormatter) Write(io.Writer) error { return nil }
func (testFormatter) Reset()                {}
func (testFormatter) IsEmpty() bool         { return true }

func TestOutputFormatClassification(t *testing.T) {
	tests := []struct {
		format     OutputFormat
		wantUpload bool
		wantFlame  bool
	}{
		{format: FormatRemote, wantUpload: true},
		{format: FormatFlameGraph, wantFlame: true},
		{format: FormatSVG, wantFlame: true},
		{format: FormatCollapsed},
	}

	for _, tt := range tests {
		if got := tt.format.IsUpload(); got != tt.wantUpload {
			t.Errorf("%q IsUpload() = %t, want %t", tt.format, got, tt.wantUpload)
		}
		if got := tt.format.IsFlameGraph(); got != tt.wantFlame {
			t.Errorf("%q IsFlameGraph() = %t, want %t", tt.format, got, tt.wantFlame)
		}
	}
}

func TestNewFormatterRegistrationAndUnknownFormat(t *testing.T) {
	format := OutputFormat("test-format-contract")
	RegisterFormatter(format, func() Formatter { return testFormatter{} })

	got, err := format.NewFormatter()
	if err != nil || got.Name() != "test" {
		t.Fatalf("NewFormatter() = (%T, %v), want test formatter and nil error", got, err)
	}

	if _, err := OutputFormat("missing-format").NewFormatter(); !errors.Is(err, ErrUnregisteredFormat) {
		t.Fatalf("unknown NewFormatter() error = %v, want ErrUnregisteredFormat", err)
	}
}
