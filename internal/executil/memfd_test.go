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

package executil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMemfdSeparatesDataAndPreservesCallerFiles(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "inherited")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	args := make([]string, 0, 8)
	spec := &Spec{Path: "/bin/sh", Args: args}
	result, err := RunWithMemfd(t.Context(), spec, func(path string) []string {
		if path != "/proc/self/fd/4" {
			t.Errorf("output path = %q, want fd 4 after caller's fd 3", path)
		}
		return []string{"-c", `printf 'caller' >&3; printf 'data' >&4; printf 'status'; printf 'warning' >&2`}
	}, 4, WithExtraFiles(file))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "data" || string(result.Stdout) != "status" || string(result.Stderr) != "warning" {
		t.Fatalf("result = %#v", result)
	}
	// Direct writes to fd 4 advance the shared offset; reading must start at zero.
	if _, err := file.WriteString(" still open"); err != nil {
		t.Fatalf("caller file was closed: %v", err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || string(data) != "caller still open" {
		t.Fatalf("caller file = %q, error = %v", data, err)
	}
	if !slices.Equal(args[:cap(args)], make([]string, cap(args))) {
		t.Fatal("caller argument backing array was modified")
	}
}

func TestMemfdOutputBoundaries(t *testing.T) {
	for _, size := range []int{0, 3, 4, 5} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			result, err := RunWithMemfd(t.Context(), &Spec{Path: "/bin/sh"}, func(path string) []string {
				return []string{"-c", `truncate -s "$1" "$2"`, "memfd-test", strconv.Itoa(size), path}
			}, 4)
			if size > 4 {
				if err == nil || !strings.Contains(err.Error(), "memfd output exceeds 4 bytes") || len(result.Data) != 0 {
					t.Fatalf("result = %#v, error = %v", result, err)
				}
				return
			}
			if err != nil || len(result.Data) != size {
				t.Fatalf("data length = %d, error = %v, want %d bytes", len(result.Data), err, size)
			}
		})
	}
}

func TestMemfdFailurePreservesDiagnostics(t *testing.T) {
	result, err := RunWithMemfd(t.Context(), &Spec{Path: "/bin/sh"}, func(path string) []string {
		return []string{"-c", `printf 'partial' > "$1"; printf 'status'; printf 'reason' >&2; exit 7`, "memfd-test", path}
	}, 1024)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("error = %v, want exit status 7", err)
	}
	if len(result.Data) != 0 || string(result.Stdout) != "status" || string(result.Stderr) != "reason" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMemfdCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	go func() {
		select {
		case <-ready:
			cancel()
		case <-ctx.Done():
		}
	}()
	result, err := RunWithMemfd(ctx, &Spec{Path: "/bin/sh", StopGracePeriod: 50 * time.Millisecond}, func(path string) []string {
		return []string{"-c", `printf 'partial' > "$1"; printf 'ready'; exec sleep 30`, "memfd-test", path}
	}, 1024, WithStdout(memfdReadyWriter{ready: ready}))
	if !errors.Is(err, context.Canceled) || len(result.Data) != 0 {
		t.Fatalf("result = %#v, error = %v, want canceled without data", result, err)
	}
}

type memfdReadyWriter struct{ ready chan struct{} }

func (w memfdReadyWriter) Write(p []byte) (int, error) {
	select {
	case <-w.ready:
	default:
		close(w.ready)
	}
	return len(p), nil
}

func TestMemfdFailurePathsCloseFile(t *testing.T) {
	countFiles := func() int {
		t.Helper()
		files, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, file := range files {
			target, err := os.Readlink("/proc/self/fd/" + file.Name())
			if err == nil && strings.Contains(target, "memfd:huatuo-exec") {
				count++
			}
		}
		return count
	}
	before := countFiles()
	for _, path := range []string{"", "/no/such/huatuo-command", "/bin/false", "/bin/true"} {
		_, _ = RunWithMemfd(t.Context(), &Spec{Path: path}, func(string) []string { return nil }, 4)
		if after := countFiles(); after != before {
			t.Fatalf("path %q leaked memory file: before %d, after %d", path, before, after)
		}
	}
}

func TestMemfdInvalidInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	spec := &Spec{Path: "/bin/true"}
	args := func(string) []string { return nil }
	tests := []struct {
		ctx  context.Context
		spec *Spec
		args func(string) []string
		max  int
	}{
		{nil, spec, args, 4},
		{t.Context(), nil, args, 4},
		{t.Context(), spec, nil, 4},
		{t.Context(), spec, args, 0},
		{t.Context(), spec, args, -1},
		{ctx, spec, args, 4},
	}
	for i := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tt := &tests[i]
			result, err := RunWithMemfd(tt.ctx, tt.spec, tt.args, tt.max)
			if err == nil || result == nil || len(result.Data) != 0 {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}
