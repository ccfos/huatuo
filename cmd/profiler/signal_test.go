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

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func TestSignalContextCleanup(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "normal cleanup"
		if external {
			name = "external cancellation"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			var logs bytes.Buffer
			ctx, cleanup := newSignalContext(parent, &logs)
			defer cleanup()
			if external {
				cancelParent()
			}
			cleanup()
			cleanup()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error=%v", ctx.Err())
			}
			if !external && parent.Err() != nil {
				t.Fatalf("cleanup canceled parent: %v", parent.Err())
			}
			// Reading after cleanup also checks that the listener has stopped writing.
			if logs.Len() != 0 {
				t.Fatalf("unexpected signal log: %s", logs.String())
			}
		})
	}
}

func TestSignalContextHandlesTermination(t *testing.T) {
	// Isolate process-wide signal delivery from other tests and the test runner.
	const childEnv = "HUATUO_PROFILER_SIGNAL_TEST"
	childSignal := os.Getenv(childEnv)
	if childSignal == "" {
		for _, name := range []string{"TERM", "HUP"} {
			t.Run(name, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSignalContextHandlesTermination$")
				cmd.Env = append(os.Environ(), childEnv+"="+name)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("signal subprocess: %v\n%s", err, output)
				}
			})
		}
		return
	}
	sig := syscall.SIGTERM
	if childSignal == "HUP" {
		sig = syscall.SIGHUP
	}
	var logs bytes.Buffer
	ctx, cleanup := newSignalContext(t.Context(), &logs)
	defer cleanup()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		t.Fatalf("%v did not cancel collection", sig)
	}

	if profile.IsNormalStop(ctx) || !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("%v: expected cancellation without normal finalization, cause=%v", sig, context.Cause(ctx))
	}

	cleanup()
	if !strings.Contains(logs.String(), "[signal] caught signal: "+sig.String()+", canceling context") {
		t.Fatalf("missing signal diagnostic: %q", logs.String())
	}
}

func TestSignalContextHandlesUserStop(t *testing.T) {
	const childEnv = "HUATUO_PROFILER_INTERRUPT_TEST"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSignalContextHandlesUserStop$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("signal subprocess: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	ctx, cleanup := newSignalContext(t.Context(), &logs)
	defer cleanup()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not stop collection")
	}
	if !profile.IsNormalStop(ctx) {
		t.Fatalf("cause=%v", context.Cause(ctx))
	}
}
