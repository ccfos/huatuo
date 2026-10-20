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
	"sync"
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

func TestEventCommandsPreserveTimeouts(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	for _, command := range []struct {
		name  string
		start func(context.Context) error
	}{
		{"dropwatch", (&dropWatchTracing{}).Start},
		{"tcpshark", (&tcpRetransmitTracing{}).Start},
	} {
		t.Run(command.name, func(t *testing.T) {
			err := command.start(ctx)
			if !errors.Is(err, executil.ErrRunTimeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Start() error = %v, want command timeout", err)
			}
			if errors.Is(err, executil.ErrRunCanceled) {
				t.Fatalf("Start() incorrectly marked timeout as cancellation: %v", err)
			}
		})
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

// Run first observes Done after Start acknowledges launch. The child can
// publish its marker earlier, so that marker alone cannot gate cancellation.
type commandLaunchContext struct {
	context.Context
	launched chan struct{}
	once     sync.Once
}

func (c *commandLaunchContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.launched) })
	return c.Context.Done()
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
			for _, scenario := range []string{"none", "stdout tail", "execution"} {
				t.Run(scenario, func(t *testing.T) {
					internalconfig.CoreBinDir = t.TempDir()
					marker := filepath.Join(t.TempDir(), "ready")
					exitCode := 0
					if scenario == "execution" {
						exitCode = 7
					}
					output := ""
					if scenario == "stdout tail" {
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
					baseCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					ctx := &commandLaunchContext{Context: baseCtx, launched: make(chan struct{})}
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
					select {
					case <-ctx.launched:
					case <-baseCtx.Done():
						t.Fatal("command did not acknowledge launch")
					}
					cancel()
					err := <-result
					if scenario != "execution" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if err == nil {
						t.Fatal("independent failure was swallowed")
					}
					if !strings.Contains(err.Error(), "exit status 7") {
						t.Fatalf("execution error: %v", err)
					}
				})
			}
		})
	}
}
