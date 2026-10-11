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

package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/executil"
)

func TestDropWatchStartTreatsCancellationAsExpectedStop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := (&dropWatchTracing{}).Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestTCPRetransmitStartTreatsCancellationAsExpectedStop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := (&tcpRetransmitTracing{}).Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestDropWatchStartIncludesDiagnosticOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed process execution requires Linux")
	}

	originalBinDir := internalconfig.CoreBinDir
	internalconfig.CoreBinDir = t.TempDir()
	t.Cleanup(func() {
		internalconfig.CoreBinDir = originalBinDir
	})

	toolPath := filepath.Join(internalconfig.CoreBinDir, "dropwatch")
	tool := []byte("#!/bin/sh\nprintf 'tool diagnostic' >&2\nexit 1\n")
	if err := os.WriteFile(toolPath, tool, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", toolPath, err)
	}
	if err := os.Chmod(toolPath, 0o700); err != nil {
		t.Fatalf("Chmod(%q) error = %v", toolPath, err)
	}

	err := (&dropWatchTracing{}).Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "tool diagnostic") {
		t.Fatalf("Start() error = %v, want diagnostic output", err)
	}
}

func TestEventCommandsPreserveFailuresDuringCancellation(t *testing.T) {
	original := internalconfig.CoreBinDir
	t.Cleanup(func() { internalconfig.CoreBinDir = original })
	commands := []struct {
		name  string
		start func(context.Context) error
	}{
		{"dropwatch", (&dropWatchTracing{}).Start},
		{"tcpshark", (&tcpRetransmitTracing{}).Start},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, failure := range []string{"none", "output", "execution"} {
				t.Run(failure, func(t *testing.T) {
					internalconfig.CoreBinDir = t.TempDir()
					marker := filepath.Join(t.TempDir(), "ready")
					exitCode := 0
					if failure == "execution" {
						exitCode = 7
					}
					output := ""
					if failure == "output" {
						output = "head -c 65537 /dev/zero"
					}
					script := fmt.Sprintf("#!/bin/sh\ntrap 'exit %d' TERM\n%s\ntouch %q\nwhile :; do :; done\n", exitCode, output, marker)
					tool := filepath.Join(internalconfig.CoreBinDir, command.name)
					if err := os.WriteFile(tool, []byte(script), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(tool, 0o700); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					result := make(chan error, 1)
					go func() { result <- command.start(ctx) }()
					deadline := time.Now().Add(3 * time.Second)
					for {
						if _, err := os.Stat(marker); err == nil {
							break
						}
						if time.Now().After(deadline) {
							cancel()
							t.Fatalf("tool did not become ready: %v", <-result)
						}
						time.Sleep(time.Millisecond)
					}
					cancel()
					err := <-result
					if failure == "none" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if err == nil {
						t.Fatal("independent failure was swallowed")
					}
					if failure == "output" && !errors.Is(err, executil.ErrOutputLimitExceeded) {
						t.Fatalf("output error: %v", err)
					}
					if failure == "execution" && !strings.Contains(err.Error(), "exit status 7") {
						t.Fatalf("execution error: %v", err)
					}
				})
			}
		})
	}
}

func TestEventCommandsPreserveCleanupFailureDuringCancellation(t *testing.T) {
	commands := []struct {
		name  string
		start func(context.Context, func(context.Context, executil.Spec, ...executil.Option) (*executil.Result, error)) error
	}{
		{"dropwatch", (&dropWatchTracing{}).start},
		{"tcpshark", (&tcpRetransmitTracing{}).start},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, diagnostic := range []string{"", "tool cleanup diagnostic"} {
				name := "without output"
				if diagnostic != "" {
					name = "with stderr"
				}
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					failure := &executil.RunError{
						ContextErr: context.Canceled,
						CleanupErr: fmt.Errorf("signal process group: %w", errors.Join(executil.ErrStopFailed, syscall.EPERM)),
					}
					calls := 0
					err := command.start(ctx, func(runCtx context.Context, spec executil.Spec, _ ...executil.Option) (*executil.Result, error) {
						calls++
						if runCtx != ctx || filepath.Base(spec.Path) != command.name {
							t.Fatalf("unexpected command context or path: %s", spec.Path)
						}
						cancel()
						if diagnostic == "" {
							return nil, failure
						}
						return &executil.Result{Stderr: []byte(diagnostic)}, failure
					})
					if calls != 1 {
						t.Fatalf("command ran %d times, want 1", calls)
					}
					for _, cause := range []error{context.Canceled, executil.ErrStopFailed, syscall.EPERM} {
						if !errors.Is(err, cause) {
							t.Errorf("lost %v: %v", cause, err)
						}
					}
					var got *executil.RunError
					if !errors.As(err, &got) || got != failure {
						t.Fatalf("lost RunError: %v", err)
					}
					if diagnostic != "" && !strings.Contains(err.Error(), diagnostic) {
						t.Fatalf("lost diagnostic: %v", err)
					}
				})
			}
		})
	}
}
