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

package aggregator

import (
	"errors"
	"io"
	"testing"

	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

const testFormatterFormat = output.OutputFormat("test-aggregator-format")

// testFormatter is a self-contained Formatter so this test does not depend on
// generated mocks.
type testFormatter struct{}

func (testFormatter) Name() string             { return string(testFormatterFormat) }
func (testFormatter) Add(*output.Sample) error { return nil }
func (testFormatter) Write(io.Writer) error    { return nil }
func (testFormatter) Reset()                   {}
func (testFormatter) IsEmpty() bool            { return true }

// TestNewFormatterForOutput pins the factory's three-way contract: upload
// formats are rejected for file output (nil, nil — the aggregator must use
// Snapshot instead), registered formats return their formatter, and anything
// unregistered surfaces an error instead of a nil formatter.
func TestNewFormatterForOutput(t *testing.T) {
	t.Run("upload format yields no file formatter", func(t *testing.T) {
		pctx := &profctx.ProfilerContext{OutputFormat: output.FormatRemote}

		formatter, err := NewFormatterForOutput(pctx)
		if err != nil {
			t.Fatalf("NewFormatterForOutput(upload) error = %v, want nil", err)
		}

		if formatter != nil {
			t.Fatalf("NewFormatterForOutput(upload) = %v, want nil formatter", formatter)
		}
	})

	t.Run("registered format yields a formatter", func(t *testing.T) {
		output.RegisterFormatter(testFormatterFormat, func() output.Formatter { return testFormatter{} })

		pctx := &profctx.ProfilerContext{OutputFormat: testFormatterFormat}

		formatter, err := NewFormatterForOutput(pctx)
		if err != nil {
			t.Fatalf("NewFormatterForOutput(registered) error = %v, want nil", err)
		}

		if _, ok := formatter.(testFormatter); !ok {
			t.Fatalf("NewFormatterForOutput(registered) = %T, want testFormatter", formatter)
		}
	})

	t.Run("unregistered format surfaces an error", func(t *testing.T) {
		pctx := &profctx.ProfilerContext{OutputFormat: output.OutputFormat("aggregator-no-such-format")}

		formatter, err := NewFormatterForOutput(pctx)
		if formatter != nil {
			t.Fatalf("NewFormatterForOutput(unregistered) = %v, want nil formatter", formatter)
		}

		if !errors.Is(err, output.ErrUnregisteredFormat) {
			t.Fatalf("NewFormatterForOutput(unregistered) error = %v, want ErrUnregisteredFormat", err)
		}
	})
}
