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
	if err == nil {
		t.Fatalf("StartAsprofSampling() error=%v, want positive duration error", err)
	}
}

func TestAsprofPath(t *testing.T) {
	toolPath := filepath.Join("opt", "async-profiler")
	if got, want := asprofPath(toolPath), filepath.Join(toolPath, "bin", "asprof"); got != want {
		t.Fatalf("asprofPath()=%q, want %q", got, want)
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
		"-o", "collapsed,dot",
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
		{
			name:                "exact windows",
			duration:            20 * time.Second,
			aggregationInterval: 10 * time.Second,
			want:                4,
		},
		{
			name:                "partial window",
			duration:            21 * time.Second,
			aggregationInterval: 10 * time.Second,
			want:                5,
		},
		{
			name:                "duration below interval",
			duration:            time.Second,
			aggregationInterval: 10 * time.Second,
			want:                3,
		},
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

	got := stopWithOutputArgs(
		1234,
		"session123",
		"mem",
		4,
	)
	want := []string{
		"stop",
		"--libpath", "/tmp/libasyncProfiler.so",
		"-o", "collapsed,dot",
		"-f", "/tmp/huatuo-asprof-session123-mem-1234-4.collapsed",
		"--total",
		"1234",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stopWithOutputArgs()=%q, want %q", got, want)
	}
}

func TestStopJavaProfilerCleansDeployment(t *testing.T) {
	t.Parallel()
	toolPath := t.TempDir()
	writeAgentSource(t, toolPath, []byte("agent"), 0o600)
	directory := t.TempDir()
	installed, err := installJavaAgents(toolPath, []string{directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, directory := range installed {
			_ = directory.Close()
		}
	})
	// Startup failure and normal shutdown share this cleanup without active PIDs.
	opt := &AsprofSamplingOption{agentDirectories: installed}
	if err := StopJavaProfiler(opt); err != nil {
		t.Fatal(err)
	}
	if opt.agentDirectories != nil {
		t.Fatal("stopped session retains deployment handles")
	}
	if _, err := os.Stat(filepath.Join(directory, "libasyncProfiler.so")); !os.IsNotExist(err) {
		t.Fatalf("agent remains after stop: %v", err)
	}
	if _, err := installed[0].Stat(); err == nil {
		t.Fatal("stop left the directory handle open")
	}
}
