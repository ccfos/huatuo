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
	"context"
	"errors"
	"os"
	"os/exec"
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
			ctx, _, cleanup := profile.SubscribeRunSignals(parent, func(sig os.Signal) {
				t.Errorf("unexpected signal: %v", sig)
			})
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
	observed := make(chan os.Signal, 1)
	ctx, _, cleanup := profile.SubscribeRunSignals(t.Context(), func(sig os.Signal) {
		observed <- sig
	})
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

	if got := <-observed; got != sig {
		t.Fatalf("observed %v, want %v", got, sig)
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

	observed := make(chan os.Signal, 2)
	ctx, _, cleanup := profile.SubscribeRunSignals(t.Context(), func(sig os.Signal) {
		observed <- sig
	})
	defer cleanup()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-observed:
		if sig != syscall.SIGINT {
			t.Fatalf("observed %v, want SIGINT", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT was not handled")
	}
	if ctx.Err() != nil {
		t.Fatalf("SIGINT canceled publication: %v", ctx.Err())
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatalf("SIGTERM cause = %v", context.Cause(ctx))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel finalization")
	}
}
