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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/utils/executil"
)

func TestFormatCmdIncludesExecutableAndArguments(t *testing.T) {
	t.Parallel()

	got := formatCmd(
		"/opt/async-profiler/bin/asprof",
		[]string{"dump", "-f", "/tmp/profile.collapsed", "164879"},
	)
	want := "/opt/async-profiler/bin/asprof dump -f /tmp/profile.collapsed 164879"
	if got != want {
		t.Fatalf("formatCmd()=%q, want %q", got, want)
	}
}

func TestExecCmdsUsesStopProfilerResultAfterCancellation(t *testing.T) {
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
			asprofPath := filepath.Join(t.TempDir(), "asprof")
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

			results := ExecCmds(ctx, []int{164879}, asprofPath, func(int) []string {
				return []string{"start"}
			})
			if len(results) != 1 {
				t.Fatalf("ExecCmds() returned %d results, want 1", len(results))
			}

			result := results[0]
			if result.Success != tt.wantSuccess {
				t.Errorf("ExecCmds() Success=%t, want %t", result.Success, tt.wantSuccess)
			}
			if tt.wantErr == "" {
				if result.CmdErr != nil {
					t.Errorf("ExecCmds() CmdErr=%v, want nil", result.CmdErr)
				}
				return
			}
			if result.CmdErr == nil || !strings.Contains(result.CmdErr.Error(), tt.wantErr) {
				t.Errorf("ExecCmds() CmdErr=%v, want substring %q", result.CmdErr, tt.wantErr)
			}
		})
	}
}

// Regression: after cancellation, execAsprofCmd runs a nested `asprof stop`
// to detach the agent from the target. A frozen JVM blocks the attach
// indefinitely, and without a deadline ExecCmds never returns, so the
// surrounding asprofCommandTimeout cannot take effect.
func TestExecCmdsReturnsWhenNestedStopProfilerHangs(t *testing.T) {
	asprofPath := filepath.Join(t.TempDir(), "asprof")
	script := `#!/bin/sh
if [ "$1" = "--libpath" ]; then
	exec sleep 60
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`
	if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake asprof: %v", err)
	}
	if err := os.Chmod(asprofPath, 0o700); err != nil {
		t.Fatalf("make fake asprof executable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan []executil.CmdResult, 1)
	go func() {
		done <- ExecCmds(ctx, []int{164879}, asprofPath, func(int) []string {
			return []string{"start"}
		})
	}()

	select {
	case results := <-done:
		if len(results) != 1 {
			t.Fatalf("ExecCmds() returned %d results, want 1", len(results))
		}
		if results[0].Success {
			t.Errorf("ExecCmds() Success=true, want false when nested stop times out")
		}
		if results[0].CmdErr == nil || !strings.Contains(results[0].CmdErr.Error(), "context deadline exceeded") {
			t.Errorf("ExecCmds() CmdErr=%v, want context deadline exceeded", results[0].CmdErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ExecCmds() did not return after cancellation: nested asprof stop is unbounded")
	}
}
