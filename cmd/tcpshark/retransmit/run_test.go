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

package retransmit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidation(t *testing.T) {
	tests := []struct {
		name     string
		config   *RunConfig
		canceled bool
		want     string
	}{
		{name: "canceled before setup", config: &RunConfig{}, canceled: true, want: "context canceled"},
		{name: "missing output", config: &RunConfig{}, want: "output is nil"},
		{name: "invalid output", config: &RunConfig{Output: io.Discard, OutputFormat: "invalid"}, want: "unsupported output"},
		{name: "missing bpf path", config: &RunConfig{Output: io.Discard, OutputFormat: OutputJSON}, want: "BPF path is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			err := Run(ctx, test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() = %v, want %q", err, test.want)
			}
			if test.canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() = %v, want context.Canceled", err)
			}
		})
	}
}

func TestRunPreservesLoadErrorAndBorrowedOutput(t *testing.T) {
	output := &borrowedOutput{Writer: io.Discard}
	err := Run(t.Context(), &RunConfig{
		Tracing:      Config{BPFPath: filepath.Join(t.TempDir(), "missing.o")},
		Output:       output,
		OutputFormat: OutputText,
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run() = %v, want missing object error", err)
	}
	if output.closed {
		t.Fatal("Run closed caller-owned output")
	}
}

type borrowedOutput struct {
	io.Writer
	closed bool
}

func (w *borrowedOutput) Close() error {
	w.closed = true
	return nil
}
