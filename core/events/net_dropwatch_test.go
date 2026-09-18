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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	internalconfig "github.com/ccfos/huatuo/internal/config"
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
