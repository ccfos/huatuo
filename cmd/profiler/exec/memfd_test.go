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
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/profile"

	"github.com/ccfos/huatuo/internal/executil"
)

func TestRunWithMemfdInvalidConfiguration(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123}, &executil.Spec{}, func(_ int, _ string) []string {
		t.Error("argument builder called for invalid configuration")
		return nil
	})
	if len(results) != 1 {
		t.Fatalf("RunWithMemfd() returned %d results, want 1", len(results))
	}
	result := results[0]
	if result.Err == nil || result.PID != 123 || len(result.Output) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("RunWithMemfd() = %+v, want configuration error and empty output for PID 123", result)
	}
}

func TestRunWithMemfdKeepsPIDOutputsSeparate(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123, 456}, &executil.Spec{Path: "/bin/sh"}, func(pid int, outputPath string) []string {
		return []string{
			"-c", `printf 'process %s;work 3\n' "$1" > "$2"; printf 'status'; printf 'warning' >&2`,
			"memfd-test", strconv.Itoa(pid), outputPath,
		}
	})
	if len(results) != 2 {
		t.Fatalf("RunWithMemfd() returned %d results, want 2", len(results))
	}
	seen := make(map[int]bool)
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("RunWithMemfd() error = %v", result.Err)
		}
		if seen[result.PID] || (result.PID != 123 && result.PID != 456) {
			t.Fatalf("unexpected or repeated PID %d", result.PID)
		}
		seen[result.PID] = true
		want := fmt.Sprintf("process %d;work 3\n", result.PID)
		if string(result.Output) != want || string(result.Diagnostics) != "warning" {
			t.Errorf("PID %d: output = %q, diagnostics = %q", result.PID, result.Output, result.Diagnostics)
		}
		if !strings.Contains(result.Command, "/proc/self/fd/") {
			t.Errorf("command = %q, want inherited output path", result.Command)
		}
	}
}

func TestRunWithMemfdEnforcesProfilerLimit(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123}, &executil.Spec{Path: "/bin/sh"}, func(_ int, outputPath string) []string {
		return []string{
			"-c", `truncate -s "$1" "$2"`, "memfd-test", strconv.Itoa(profilerOutputLimit + 1), outputPath,
		}
	})
	if len(results) != 1 {
		t.Fatalf("RunWithMemfd() returned %d results, want 1", len(results))
	}
	result := results[0]
	if !errors.Is(result.Err, executil.ErrOutputLimitExceeded) {
		t.Fatalf("RunWithMemfd() error = %v, want size limit error", result.Err)
	}
	if len(result.Output) != 0 {
		t.Errorf("oversized output length = %d, want 0", len(result.Output))
	}
}

func TestRunWithMemfdDiscardsOutputAfterCommandFailure(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123}, &executil.Spec{Path: "/bin/sh"}, func(_ int, path string) []string {
		return []string{"-c", `printf partial > "$1"; printf failure >&2; exit 7`, "test", path}
	})
	if len(results) != 1 || results[0].Err == nil || len(results[0].Output) != 0 || string(results[0].Diagnostics) != "failure" {
		t.Fatalf("results: %+v", results)
	}
}

// The child installs its handler before reporting readiness, avoiding signal races.
func TestCollectorCommandSignalChild(t *testing.T) {
	if os.Getenv("HUATUO_COLLECTOR_SIGNAL_CHILD") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	defer signal.Stop(signals)
	fmt.Println("ready")
	<-signals
	if os.Getenv("HUATUO_COLLECTOR_IGNORE_STOP") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if err := os.WriteFile(os.Getenv("HUATUO_COLLECTOR_OUTPUT"), []byte("root;leaf 7\n"), 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// stopDuringLaunchContext blocks the post-launch cancellation check until the
// child is ready and the stop has been requested, reproducing the launch window.
type stopDuringLaunchContext struct {
	context.Context
	release <-chan struct{}
	checks  int
}

func (c *stopDuringLaunchContext) Err() error {
	c.checks++
	// Process.Start checks before preparing, before launching and after
	// cmd.Start; the last check is the launch window under test.
	if c.checks == 3 {
		<-c.release
	}
	return c.Context.Err()
}

func TestRunWithMemfdStopDuringLaunch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cause       error
		wantSuccess bool
	}{
		{name: "duration", cause: profile.ErrCollectionDurationElapsed, wantSuccess: true},
		{name: "requested", cause: profile.ErrStopRequested, wantSuccess: true},
		{name: "canceled", cause: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 5 {
				ctx, cancel := context.WithCancelCause(t.Context())
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				release := make(chan struct{})
				launchCtx := &stopDuringLaunchContext{Context: ctx, release: release}
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = bufio.NewReader(reader).ReadString('\n')
					cancel(tc.cause)
					close(release)
				}()

				env := append(os.Environ(), "HUATUO_COLLECTOR_SIGNAL_CHILD=1", "HUATUO_COLLECTOR_OUTPUT=/proc/self/fd/3")
				results := RunWithMemfd(launchCtx, []int{123}, &executil.Spec{
					Path: os.Args[0], Env: env, StopSignal: syscall.SIGINT, StopGracePeriod: 5 * time.Second,
				}, func(_ int, _ string) []string {
					return []string{"-test.run=^TestCollectorCommandSignalChild$"}
				}, executil.WithStdout(writer))

				writer.Close()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("child did not report readiness")
				}
				reader.Close()
				cancel(nil)

				if len(results) != 1 {
					t.Fatalf("results=%v", results)
				}
				result := results[0]
				if tc.wantSuccess {
					if result.Err != nil || string(result.Output) != "root;leaf 7\n" {
						t.Fatalf("error=%v output=%q", result.Err, result.Output)
					}
				} else if result.Err == nil || len(result.Output) != 0 {
					t.Fatalf("error=%v output=%q", result.Err, result.Output)
				}
			}
		})
	}
}

func TestRunWithMemfdStopContract(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cause       error
		ignore      bool
		wantSuccess bool
	}{
		{name: "duration", cause: profile.ErrCollectionDurationElapsed, wantSuccess: true},
		{name: "requested", cause: profile.ErrStopRequested, wantSuccess: true},
		{name: "canceled", cause: context.Canceled},
		{name: "forced kill", cause: profile.ErrStopRequested, ignore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := bufio.NewReader(reader).ReadString('\n')
				if err == nil {
					cancel(tc.cause)
				}
			}()
			defer func() { reader.Close(); <-done }()
			env := append(os.Environ(), "HUATUO_COLLECTOR_SIGNAL_CHILD=1", "HUATUO_COLLECTOR_OUTPUT=/proc/self/fd/3")
			if tc.ignore {
				env = append(env, "HUATUO_COLLECTOR_IGNORE_STOP=1")
			}
			grace := 5 * time.Second
			if tc.ignore {
				grace = 100 * time.Millisecond
			}
			results := RunWithMemfd(ctx, []int{123}, &executil.Spec{Path: os.Args[0], Env: env, StopSignal: syscall.SIGINT, StopGracePeriod: grace}, func(_ int, _ string) []string { return []string{"-test.run=^TestCollectorCommandSignalChild$"} }, executil.WithStdout(writer))
			if len(results) != 1 {
				t.Fatalf("results=%v", results)
			}
			result := results[0]
			if tc.ignore && !errors.Is(result.Err, executil.ErrStopped) {
				t.Fatalf("forced stop error=%v", result.Err)
			}
			if tc.wantSuccess {
				if result.Err != nil || string(result.Output) != "root;leaf 7\n" {
					t.Fatalf("error=%v output=%q", result.Err, result.Output)
				}
			} else if result.Err == nil || len(result.Output) != 0 {
				t.Fatalf("error=%v output=%q", result.Err, result.Output)
			}
		})
	}
}
