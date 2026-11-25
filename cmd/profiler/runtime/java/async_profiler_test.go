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

package java

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
)

// The child can become ready before Start returns. Done is first observed by
// runAsyncProfiler after launch, so cancellation waits for both acknowledgments.
type profilerLaunchContext struct {
	context.Context
	launched chan struct{}
	once     sync.Once
}

func (c *profilerLaunchContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.launched) })
	return c.Context.Done()
}

func TestRunAsyncProfilerUsesStopAsyncProfilerResultAfterCancellation(t *testing.T) {
	tests := []struct {
		name        string
		stopExit    int
		wantSuccess bool
		signalExit  bool
		wantErr     string
	}{
		{name: "stop succeeds", wantSuccess: true},
		{name: "signal stop succeeds", signalExit: true, wantSuccess: true},
		{name: "stop fails", stopExit: 23, wantErr: "exit status 23"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asprofPath := filepath.Join(t.TempDir(), "renamed-profiler")
			readyPath := filepath.Join(filepath.Dir(asprofPath), "ready")
			trap := "trap 'exit 0' TERM"
			if tt.signalExit {
				trap = ""
			}
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	exit %d
fi
%s
: > %q
while :; do sleep 1; done
`, tt.stopExit, trap, readyPath)
			if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
				t.Fatalf("write fake asprof: %v", err)
			}
			if err := os.Chmod(asprofPath, 0o700); err != nil {
				t.Fatalf("make fake asprof executable: %v", err)
			}

			baseCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx := &profilerLaunchContext{Context: baseCtx, launched: make(chan struct{})}
			// Cancel after launch so this test exercises stopping, even under load.
			go func() {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(readyPath); err == nil {
						select {
						case <-ctx.launched:
						case <-baseCtx.Done():
							return
						}
						cancel()
						return
					}
					select {
					case <-baseCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()

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

func TestRunAsyncProfilerReportsExecutionFailureOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiler")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	results := RunAsyncProfiler(t.Context(), []int{123}, path, func(int) []string { return nil })
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("RunAsyncProfiler() = %+v, want execution failure", results)
	}
	if count := strings.Count(results[0].Err.Error(), "exit status 7"); count != 1 {
		t.Fatalf("execution failure appeared %d times: %v", count, results[0].Err)
	}
	if errors.Is(results[0].Err, executil.ErrCleanupFailed) {
		t.Fatalf("ordinary exit was classified as cleanup failure: %v", results[0].Err)
	}
}

func TestRunAsyncProfilerStopsWithItsStartLibrary(t *testing.T) {
	dir := t.TempDir()
	asprofPath := filepath.Join(dir, "asprof")
	stopLibPath := filepath.Join(dir, "stop-libpath")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	printf '%%s' "$2" > %q
	exit 0
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`, stopLibPath)
	if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(asprofPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	libPath := "/tmp/libasyncProfiler-session123.so"
	results := RunAsyncProfiler(ctx, []int{1234}, asprofPath, func(int) []string {
		return []string{"start", "--libpath", libPath}
	})
	if len(results) != 1 || !results[0].Succeeded() {
		t.Fatalf("RunAsyncProfiler() results=%v, want successful stop", results)
	}
	got, err := os.ReadFile(stopLibPath)
	if err != nil || string(got) != libPath {
		t.Fatalf("stop --libpath=%q, error=%v, want %q", got, err, libPath)
	}
}
