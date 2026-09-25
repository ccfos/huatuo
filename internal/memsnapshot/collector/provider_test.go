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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

type providerFunc func(context.Context, memsnapshot.Request) (*memsnapshot.Snapshot, error)

func (f providerFunc) Snapshot(ctx context.Context, request memsnapshot.Request) (*memsnapshot.Snapshot, error) {
	return f(ctx, request)
}

func TestProvidersReturnContextErrors(t *testing.T) {
	for _, language := range []memsnapshot.Language{
		memsnapshot.LanguageGo, memsnapshot.LanguageJava, memsnapshot.LanguagePython,
	} {
		t.Run(string(language), func(t *testing.T) {
			p := newProvider(language)
			if p == nil {
				t.Fatal("runtime has no provider")
			}
			for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
				t.Run(want.Error(), func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					if errors.Is(want, context.DeadlineExceeded) {
						ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
						defer cancel()
					}
					result, err := p.Snapshot(ctx, memsnapshot.Request{})
					if result != nil || !errors.Is(err, want) {
						t.Fatalf("snapshot = %+v, %v, want no snapshot and %v", result, err, want)
					}
				})
			}
		})
	}
}

func TestSnapshotProviderResult(t *testing.T) {
	for _, test := range []struct {
		name   string
		status memsnapshot.Status
		reason string
	}{
		{name: "complete", status: memsnapshot.StatusComplete},
		{name: "partial", status: memsnapshot.StatusPartial},
		{name: "unsupported", status: memsnapshot.StatusUnavailable, reason: "runtime is not supported"},
		{name: "read error", status: memsnapshot.StatusFailed, reason: os.ErrPermission.Error()},
		{name: "panic", status: memsnapshot.StatusFailed, reason: "runtime snapshot panic: broken reader"},
		{name: "panic with timeout", status: memsnapshot.StatusFailed, reason: "runtime snapshot panic: broken reader"},
		{name: "timeout", status: memsnapshot.StatusFailed, reason: context.DeadlineExceeded.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.name == "timeout" || test.name == "panic with timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(1, 0))
				defer cancel()
			}
			var p provider = providerFunc(func(context.Context, memsnapshot.Request) (*memsnapshot.Snapshot, error) {
				switch test.name {
				case "read error":
					return nil, os.ErrPermission
				case "panic", "panic with timeout":
					panic("broken reader")
				}
				status := memsnapshot.StatusComplete
				if test.name == "partial" {
					status = memsnapshot.StatusPartial
				}
				return &memsnapshot.Snapshot{Status: status}, nil
			})
			if test.name == "unsupported" {
				p = nil
			}
			result := snapshotProvider(ctx, p, memsnapshot.ProcessIdentity{}, 0)
			if result == nil || result.Status != test.status || !strings.Contains(result.Reason, test.reason) {
				t.Fatalf("snapshot = %+v; want status %s and reason containing %q", result, test.status, test.reason)
			}
			if result.DurationMS == 0 {
				t.Fatal("snapshot is missing provider duration")
			}
		})
	}
}

func TestSnapshotProviderRequest(t *testing.T) {
	identity := memsnapshot.ProcessIdentity{TGID: 42, StartTimeTicks: 100}
	want := &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete}
	p := providerFunc(func(_ context.Context, request memsnapshot.Request) (*memsnapshot.Snapshot, error) {
		if request.Identity != identity || request.TopK != 7 {
			t.Fatalf("provider request = %+v; want selected identity and top-K 7", request)
		}
		return want, nil
	})
	result := snapshotProvider(t.Context(), p, identity, 7)
	if result != want {
		t.Fatalf("snapshot = %+v; want provider result", result)
	}
}
