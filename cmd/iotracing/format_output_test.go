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

package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

type failingOutputWriter struct {
	remaining int
	err       error
}

func (w *failingOutputWriter) Write(p []byte) (int, error) {
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.err
}

func TestTextWriterPropagatesOutputErrors(t *testing.T) {
	wantErr := errors.New("output destination full")
	largeSnapshot := &types.IOTracingSnapshot{
		Processes: make([]types.ProcessFileIOStats, 100),
	}
	tests := []struct {
		name      string
		snapshot  *types.IOTracingSnapshot
		remaining int
	}{
		{name: "final flush", snapshot: &types.IOTracingSnapshot{}},
		{name: "during rendering", snapshot: largeSnapshot, remaining: 17},
		{name: "after successful writes", snapshot: largeSnapshot, remaining: 4096 + 17},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := &failingOutputWriter{remaining: tt.remaining, err: wantErr}
			w := &textWriter{w: output}
			if err := w.Write(tt.snapshot); !errors.Is(err, wantErr) {
				t.Fatalf("Write() error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestTextWriterFlushesCompleteReport(t *testing.T) {
	var output bytes.Buffer
	w := &textWriter{w: &output}
	if err := w.Write(&types.IOTracingSnapshot{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	want := "PID      COMMAND              FS_READ FS_WRITE DISK_READ DISK_WRITE FILES\n" +
		"=======  ==================== ======= ======== ========= ========== =====\n\n"
	if got := output.String(); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}
