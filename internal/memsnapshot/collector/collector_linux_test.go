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

func processIdentityForTest(t *testing.T) memsnapshot.ProcessIdentity {
	t.Helper()
	identity, err := memsnapshot.ReadIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func captureForTest(ctx context.Context, identity memsnapshot.ProcessIdentity, options Options,
	provider memsnapshot.Provider,
) (*Result, error) {
	return capture(ctx, identity, options,
		func(context.Context, int) (memsnapshot.Language, error) { return memsnapshot.LanguageGo, nil },
		func(memsnapshot.Language) memsnapshot.Provider { return provider })
}

func TestCaptureRejectsChangedProcess(t *testing.T) {
	reason := errors.New("process binding changed")
	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("validation-%d", failAt), func(t *testing.T) {
			identity := processIdentityForTest(t)
			checks, captures := 0, 0
			options := Options{CheckTarget: func(_ context.Context, actual memsnapshot.ProcessIdentity) error {
				if actual != identity {
					t.Fatal("selected identity was replaced")
				}
				checks++
				if checks == failAt {
					return reason
				}
				return nil
			}}
			provider := providerFunc(func(context.Context, memsnapshot.Request) (*memsnapshot.Snapshot, error) {
				captures++
				return &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete}, nil
			})
			if failAt == 0 {
				identity.StartTimeTicks++
				result, err := Capture(t.Context(), identity, options)
				if err == nil || result != nil || checks != 0 {
					t.Fatalf("stale identity accepted: result=%+v checks=%d err=%v", result, checks, err)
				}
				return
			}
			result, err := captureForTest(t.Context(), identity, options, provider)
			if !errors.Is(err, reason) || result != nil || checks != failAt ||
				(failAt <= 2 && captures != 0) || (failAt == 3 && captures != 1) {
				t.Fatalf("result=%+v checks=%d captures=%d err=%v", result, checks, captures, err)
			}
		})
	}
}

func TestCaptureRequiresProcessIdentity(t *testing.T) {
	for _, identity := range []memsnapshot.ProcessIdentity{
		{},
		{TGID: os.Getpid()},
		{TGID: -1, StartTimeTicks: 100},
	} {
		checks := 0
		result, err := Capture(t.Context(), identity, Options{
			CheckTarget: func(context.Context, memsnapshot.ProcessIdentity) error { checks++; return nil },
		})
		if err == nil || result != nil || checks != 0 {
			t.Fatalf("invalid identity %+v accepted: result=%+v checks=%d err=%v", identity, result, checks, err)
		}
	}
}

func TestCaptureFailureAndShutdown(t *testing.T) {
	for _, stage := range []string{"panic", "late timeout", "shutdown"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			identity := processIdentityForTest(t)
			options := Options{}
			if stage == "late timeout" {
				options.CaptureTimeout = time.Millisecond
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
			result, err := captureForTest(ctx, identity, options, provider)
			if !captured {
				t.Fatal("provider was not called")
			}
			if stage == "shutdown" {
				if result != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
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
		})
	}
}

func TestCaptureBoundsOutput(t *testing.T) {
	identity := processIdentityForTest(t)
	provider := providerFunc(func(_ context.Context, req memsnapshot.Request) (*memsnapshot.Snapshot, error) {
		if req.Identity != identity || req.TopK != 1 || req.SamplingSeed == 0 {
			t.Fatalf("capture request = %+v", req)
		}
		return &memsnapshot.Snapshot{
			Status:  memsnapshot.StatusComplete,
			Entries: []memsnapshot.Entry{{Name: "first"}, {Name: "second"}},
		}, nil
	})
	result, err := captureForTest(t.Context(), identity, Options{TopK: 1}, provider)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if result.Identity != identity || result.CaptureTime.IsZero() || result.SamplingSeed == 0 ||
		snapshot.Status != memsnapshot.StatusComplete || snapshot.DurationMS == 0 ||
		!snapshot.OutputTruncated || len(snapshot.Entries) != 1 || snapshot.Entries[0].Name != "first" {
		t.Fatalf("bounded result = %+v, snapshot = %+v", result, snapshot)
	}
}

func TestCaptureRejectsInvalidOptions(t *testing.T) {
	identity := processIdentityForTest(t)
	for _, options := range []Options{
		{TopK: -1},
		{TopK: memsnapshot.MaxMemoryObjectEntries + 1},
		{DetectionTimeout: -time.Second},
		{CaptureTimeout: -time.Second},
	} {
		result, err := Capture(t.Context(), identity, options)
		if err == nil || result != nil {
			t.Fatalf("invalid options %+v accepted: result=%+v err=%v", options, result, err)
		}
	}
}

func TestCaptureUsesRuntimeIndependentBudget(t *testing.T) {
	identity := processIdentityForTest(t)
	for _, language := range []memsnapshot.Language{memsnapshot.LanguageGo, memsnapshot.LanguageJava, memsnapshot.LanguagePython} {
		t.Run(string(language), func(t *testing.T) {
			provider := providerFunc(func(ctx context.Context, _ memsnapshot.Request) (*memsnapshot.Snapshot, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Millisecond {
					t.Fatal("provider did not receive capture budget")
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			result, err := capture(t.Context(), identity, Options{CaptureTimeout: time.Millisecond},
				func(context.Context, int) (memsnapshot.Language, error) { return language, nil },
				func(actual memsnapshot.Language) memsnapshot.Provider {
					if actual != language {
						t.Fatalf("provider language = %q, want %q", actual, language)
					}
					return provider
				})
			if err != nil || result == nil || result.Snapshot.Status != memsnapshot.StatusFailed {
				t.Fatalf("timed out capture: result=%+v err=%v", result, err)
			}
		})
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

func TestCaptureKeepsProcessMemoryWithoutRuntime(t *testing.T) {
	identity := processIdentityForTest(t)
	for _, detectionErr := range []error{nil, errors.New("detection failed")} {
		result, err := capture(t.Context(), identity, Options{},
			func(context.Context, int) (memsnapshot.Language, error) { return "", detectionErr },
			func(memsnapshot.Language) memsnapshot.Provider { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if result.ProcessMemory == nil || result.ProcessMemory.RSSBytes == nil {
			t.Fatal("missing process memory in returned result")
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
