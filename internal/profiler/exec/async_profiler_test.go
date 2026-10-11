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

package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunAsyncProfilerUsesStopAsyncProfilerResultAfterCancellation(t *testing.T) {
	tests := []struct {
		name        string
		stopExit    int
		wantSuccess bool
		wantErr     string
	}{
		{name: "stop succeeds", wantSuccess: true},
		{name: "stop fails", stopExit: 23, wantErr: "exit status 23"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asprofPath := filepath.Join(t.TempDir(), "renamed-profiler")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	exit %d
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`, tt.stopExit)
			if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
				t.Fatalf("write fake asprof: %v", err)
			}
			if err := os.Chmod(asprofPath, 0o700); err != nil {
				t.Fatalf("make fake asprof executable: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			results := RunAsyncProfiler(ctx, []int{164879}, asprofPath, func(int) []string {
				return []string{"start"}
			})
			if len(results) != 1 {
				t.Fatalf("RunAsyncProfiler() returned %d results, want 1", len(results))
			}

			result := results[0]
			if result.Succeeded() != tt.wantSuccess {
				t.Errorf("RunAsyncProfiler() Succeeded=%t, want %t", result.Succeeded(), tt.wantSuccess)
			}
			if tt.wantErr == "" {
				if result.Err != nil {
					t.Errorf("RunAsyncProfiler() Err=%v, want nil", result.Err)
				}
				return
			}
			if result.Err == nil || !strings.Contains(result.Err.Error(), tt.wantErr) {
				t.Errorf("RunAsyncProfiler() Err=%v, want substring %q", result.Err, tt.wantErr)
			}
		})
	}
}

func TestRunAsyncProfilerDoesNotStopAsyncProfilerWhenLaunchIsCanceled(t *testing.T) {
	dir := t.TempDir()
	asprofPath := filepath.Join(dir, "renamed-profiler")
	stopMarker := filepath.Join(dir, "stop-called")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	touch %q
fi
`, stopMarker)
	if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake asprof: %v", err)
	}
	if err := os.Chmod(asprofPath, 0o700); err != nil {
		t.Fatalf("make fake asprof executable: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	results := RunAsyncProfiler(ctx, []int{164879}, asprofPath, func(int) []string {
		return []string{"start"}
	})
	if len(results) != 1 {
		t.Fatalf("RunAsyncProfiler() returned %d results, want 1", len(results))
	}
	if !errors.Is(results[0].Err, context.Canceled) {
		t.Fatalf("RunAsyncProfiler() error = %v, want context cancellation", results[0].Err)
	}
	if _, err := os.Stat(stopMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("async-profiler stop marker error = %v, want missing file", err)
	}
}
