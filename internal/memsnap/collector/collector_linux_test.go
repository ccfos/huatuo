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

package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnap"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	identity, err := memsnap.ReadIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return Options{ExpectedIdentity: &identity}
}

func testRun(ctx context.Context, options Options, provider memsnap.Provider) (*Result, error) {
	return run(ctx, os.Getpid(), options,
		func(context.Context, int) (memsnap.Language, error) { return memsnap.LanguageGo, nil },
		func(memsnap.Language) memsnap.Provider { return provider })
}

func TestRunRejectsChangedTarget(t *testing.T) {
	reason := errors.New("victim validation failed")
	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("validation-%d", failAt), func(t *testing.T) {
			options := testOptions(t)
			checks, captures, saves := 0, 0, 0
			options.CheckTarget = func(_ context.Context, identity memsnap.ProcessIdentity) error {
				if identity != *options.ExpectedIdentity {
					t.Fatal("selected identity or cgroup was replaced")
				}
				checks++
				if checks == failAt {
					return reason
				}
				return nil
			}
			provider := providerFunc(func(context.Context, memsnap.Request) (*memsnap.Snapshot, error) {
				captures++
				return &memsnap.Snapshot{Status: memsnap.StatusComplete}, nil
			})
			options.Save = func(context.Context, *Result) error { saves++; return nil }
			if failAt == 0 {
				options.ExpectedIdentity.StartTimeTicks++
				if _, err := Run(t.Context(), os.Getpid(), options); err == nil || checks != 0 || saves != 0 {
					t.Fatalf("stale identity accepted: checks=%d saves=%d err=%v", checks, saves, err)
				}
				return
			}
			_, err := testRun(t.Context(), options, provider)
			if !errors.Is(err, reason) || checks != failAt ||
				saves != 0 || (failAt <= 2 && captures != 0) || (failAt == 3 && captures != 1) {
				t.Fatalf("checks=%d captures=%d saves=%d err=%v", checks, captures, saves, err)
			}
		})
	}
}

func TestRunFailurePersistenceAndShutdown(t *testing.T) {
	for _, stage := range []string{"panic", "late timeout", "shutdown"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			options := testOptions(t)
			if stage == "late timeout" {
				options.GoTimeout = time.Millisecond
			}
			captured := false
			provider := providerFunc(func(ctx context.Context, _ memsnap.Request) (*memsnap.Snapshot, error) {
				captured = true
				switch stage {
				case "panic":
					panic("broken reader")
				case "late timeout":
					<-ctx.Done()
				case "shutdown":
					cancel()
				}
				// A provider returning success must not override cancellation.
				return &memsnap.Snapshot{Status: memsnap.StatusComplete}, nil
			})
			saves := 0
			options.Save = func(_ context.Context, result *Result) error {
				saves++
				snapshot := result.Snapshot
				if snapshot.Status != memsnap.StatusFailed || snapshot.Reason == "" {
					t.Fatalf("failure artifact = %+v", snapshot)
				}
				if stage == "panic" && !strings.Contains(snapshot.Reason, "broken reader") {
					t.Fatalf("missing panic reason: %+v", snapshot)
				}
				if stage == "late timeout" && !strings.Contains(snapshot.Reason, "deadline exceeded") {
					t.Fatalf("missing deadline reason: %+v", snapshot)
				}
				return nil
			}
			_, err := testRun(ctx, options, provider)
			if !captured {
				t.Fatal("provider was not called")
			}
			if stage == "shutdown" {
				if saves != 0 || !errors.Is(err, context.Canceled) {
					t.Fatalf("saves=%d err=%v", saves, err)
				}
			} else if saves != 1 || err != nil {
				t.Fatalf("saves=%d err=%v", saves, err)
			}
		})
	}
}

func TestRunBoundsBeforePersistence(t *testing.T) {
	options := testOptions(t)
	provider := providerFunc(func(_ context.Context, req memsnap.Request) (*memsnap.Snapshot, error) {
		if req.Identity != *options.ExpectedIdentity ||
			req.TopK != 1 || req.SamplingSeed == 0 {
			t.Fatalf("capture request = %+v", req)
		}
		return &memsnap.Snapshot{
			Status:  memsnap.StatusComplete,
			Entries: []memsnap.Entry{{Name: "first"}, {Name: "second"}},
		}, nil
	})
	saves := 0
	options.Save = func(_ context.Context, result *Result) error {
		saves++
		snapshot := result.Snapshot
		if snapshot.Status != memsnap.StatusComplete || snapshot.DurationMS == 0 ||
			!snapshot.OutputTruncated || len(snapshot.Entries) != 1 || snapshot.Entries[0].Name != "first" {
			t.Fatalf("saved snapshot = %+v", snapshot)
		}
		return nil
	}
	options.TopK = 1
	_, err := testRun(t.Context(), options, provider)
	if err != nil || saves != 1 {
		t.Fatalf("saves=%d err=%v", saves, err)
	}
	options.Save = nil
	result, err := testRun(t.Context(), options, provider)
	if err != nil || result == nil || len(result.Snapshot.Entries) != 1 || saves != 1 {
		t.Fatalf("capture without persistence: result=%+v saves=%d err=%v", result, saves, err)
	}
}

type providerFunc func(context.Context, memsnap.Request) (*memsnap.Snapshot, error)

func (f providerFunc) Capture(ctx context.Context,
	request memsnap.Request,
) (*memsnap.Snapshot, error) {
	return f(ctx, request)
}
