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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	identity, err := memsnapshot.ReadIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return Options{ExpectedIdentity: &identity}
}

func testRun(ctx context.Context, options Options, provider memsnapshot.Provider) (*Result, error) {
	return run(ctx, os.Getpid(), options,
		func(context.Context, int) (memsnapshot.Language, error) { return memsnapshot.LanguageGo, nil },
		func(memsnapshot.Language) memsnapshot.Provider { return provider })
}

func TestRunRejectsChangedTarget(t *testing.T) {
	reason := errors.New("victim validation failed")
	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("validation-%d", failAt), func(t *testing.T) {
			options := testOptions(t)
			checks, captures, saves := 0, 0, 0
			options.CheckTarget = func(_ context.Context, identity memsnapshot.ProcessIdentity) error {
				if identity != *options.ExpectedIdentity {
					t.Fatal("selected identity or cgroup was replaced")
				}
				checks++
				if checks == failAt {
					return reason
				}
				return nil
			}
			provider := providerFunc(func(context.Context, memsnapshot.Request) (*memsnapshot.Snapshot, error) {
				captures++
				return &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete}, nil
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
			provider := providerFunc(func(ctx context.Context, _ memsnapshot.Request) (*memsnapshot.Snapshot, error) {
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
				return &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete}, nil
			})
			saves := 0
			options.Save = func(_ context.Context, result *Result) error {
				saves++
				if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
					t.Fatal("runtime failure discarded process memory")
				}
				snapshot := result.Snapshot
				if snapshot.Status != memsnapshot.StatusFailed || snapshot.Reason == "" {
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
	provider := providerFunc(func(_ context.Context, req memsnapshot.Request) (*memsnapshot.Snapshot, error) {
		if req.Identity != *options.ExpectedIdentity ||
			req.TopK != 1 || req.SamplingSeed == 0 {
			t.Fatalf("capture request = %+v", req)
		}
		return &memsnapshot.Snapshot{
			Status:  memsnapshot.StatusComplete,
			Entries: []memsnapshot.Entry{{Name: "first"}, {Name: "second"}},
		}, nil
	})
	saves := 0
	options.Save = func(_ context.Context, result *Result) error {
		saves++
		snapshot := result.Snapshot
		if snapshot.Status != memsnapshot.StatusComplete || snapshot.DurationMS == 0 ||
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

func TestProcessMemory(t *testing.T) {
	full := "VmSize: 100 kB\nVmRSS: 60 kB\nRssAnon: 40 kB\nRssFile: 20 kB\nRssShmem: 0 kB\nVmSwap: 0 kB\nVmPTE: 4 kB\n"
	m := parseProcessMemory(strings.NewReader(full))
	if m.Status != memsnapshot.StatusComplete || *m.RSSBytes != 60*1024 || *m.PageTableBytes != 4096 {
		t.Fatalf("memory = %+v", m)
	}
	for _, input := range []string{"VmRSS: 0 kB\n", "VmRSS: 0 kB\nVmSwap: bad kB\nVmSize: 18446744073709551615 kB\n"} {
		m = parseProcessMemory(strings.NewReader(input))
		data, err := json.Marshal(m)
		if err != nil || m.Status != memsnapshot.StatusPartial || m.RSSBytes == nil || *m.RSSBytes != 0 ||
			m.SwapBytes != nil || m.VirtualBytes != nil || !strings.Contains(string(data), `"rss_bytes":0`) || strings.Contains(string(data), `"swap_bytes"`) {
			t.Fatalf("missing fields confused with zero: %s, %v", data, err)
		}
	}
	if m := readProcessMemory(-1); m.Status != memsnapshot.StatusUnavailable || m.RSSBytes != nil {
		t.Fatalf("missing process = %+v", m)
	}
}

func TestRunKeepsProcessMemoryWithoutRuntime(t *testing.T) {
	for _, detectionErr := range []error{nil, errors.New("detection failed")} {
		options := testOptions(t)
		options.Save = func(_ context.Context, result *Result) error {
			if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
				t.Fatal("missing process memory in persisted result")
			}
			return nil
		}
		result, err := run(t.Context(), os.Getpid(), options,
			func(context.Context, int) (memsnapshot.Language, error) { return "", detectionErr },
			func(memsnapshot.Language) memsnapshot.Provider { return nil })
		if err != nil {
			t.Fatal(err)
		}
		want := memsnapshot.StatusUnavailable
		if detectionErr != nil {
			want = memsnapshot.StatusFailed
		}
		if result.Snapshot.Status != want {
			t.Fatalf("runtime status = %s, want %s", result.Snapshot.Status, want)
		}
	}
}

type providerFunc func(context.Context, memsnapshot.Request) (*memsnapshot.Snapshot, error)

func (f providerFunc) Capture(ctx context.Context,
	request memsnapshot.Request,
) (*memsnapshot.Snapshot, error) {
	return f(ctx, request)
}
