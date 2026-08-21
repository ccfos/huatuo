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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestStartAsprofSamplingValidatesDuration(t *testing.T) {
	t.Parallel()

	_, err := StartAsprofSampling(context.Background(), &AsprofSamplingOption{
		AggrInterval: time.Second,
	})
	if err == nil || err.Error() != "start async-profiler: duration must be positive" {
		t.Fatalf("StartAsprofSampling() error=%v, want positive duration error", err)
	}
}

func TestAsyncProfilerPaths(t *testing.T) {
	t.Parallel()

	toolPath := filepath.Join("opt", "async-profiler")
	if got, want := asprofPath(toolPath), filepath.Join(toolPath, "bin", "asprof"); got != want {
		t.Fatalf("asprofPath()=%q, want %q", got, want)
	}
	if got, want := agentLibraryPath(toolPath), filepath.Join(toolPath, "lib", "libasyncProfiler.so"); got != want {
		t.Fatalf("agentLibraryPath()=%q, want %q", got, want)
	}
}

func TestCopyAgentLibUsesLibDirectory(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	libDir := filepath.Join(toolPath, "lib")
	if err := os.Mkdir(libDir, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error=%v", libDir, err)
	}

	want := []byte("async-profiler-agent")
	source := filepath.Join(libDir, "libasyncProfiler.so")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", source, err)
	}

	targetDir := t.TempDir()
	if err := copyAgentLib(toolPath, targetDir); err != nil {
		t.Fatalf("copyAgentLib() error=%v", err)
	}

	target := filepath.Join(targetDir, "libasyncProfiler.so")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", target, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("copied agent=%q, want %q", got, want)
	}
}

func TestStartAsprofCallbackBuildsStartCommand(t *testing.T) {
	t.Parallel()

	profileOutFile := make(map[int]string)
	args := startAsprofCallback(
		profileOutFile,
		[]string{
			"--libpath", "/tmp/libasyncProfiler.so",
			"-e", "cpu",
		},
		"cpu",
		"session123",
		10*time.Second,
		4,
	)(999999999)
	wantArgs := []string{
		"start",
		"--libpath", "/tmp/libasyncProfiler.so",
		"-e", "cpu",
		"--loop", "10s",
		"-o", "collapsed",
		"-f", "/tmp/huatuo-asprof-session123-cpu-999999999-%n{4}.collapsed",
		"999999999",
	}
	if !slices.Equal(args, wantArgs) {
		t.Fatalf("startAsprofCallback() args=%q, want %q", args, wantArgs)
	}
	if got, want := profileOutFile[999999999], "/tmp/huatuo-asprof-session123-cpu-999999999-*.collapsed"; got != want {
		t.Fatalf("profile output path=%q, want %q", got, want)
	}
}

func TestAsprofOutputFileCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		duration            time.Duration
		aggregationInterval time.Duration
		want                uint64
	}{
		{name: "exact windows", duration: 20 * time.Second, aggregationInterval: 10 * time.Second, want: 4},
		{name: "partial window", duration: 21 * time.Second, aggregationInterval: 10 * time.Second, want: 5},
		{name: "duration below interval", duration: time.Second, aggregationInterval: 10 * time.Second, want: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := asprofOutputFileCount(tt.duration, tt.aggregationInterval)
			if got != tt.want {
				t.Fatalf("asprofOutputFileCount()=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestStopWithOutputArgsBuildsCollapsedOutputCommand(t *testing.T) {
	t.Parallel()

	got := stopWithOutputArgs(1234, "session123", "mem", 4)
	want := []string{
		"stop",
		"--libpath", "/tmp/libasyncProfiler.so",
		"-o", "collapsed",
		"-f", "/tmp/huatuo-asprof-session123-mem-1234-4.collapsed",
		"1234",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stopWithOutputArgs()=%q, want %q", got, want)
	}
}
