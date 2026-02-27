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

package tracing

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	pkgtypes "huatuo-bamai/pkg/types"
)

type stubEvent struct {
	startFunc func(ctx context.Context) error
}

func (s *stubEvent) Start(ctx context.Context) error {
	return s.startFunc(ctx)
}

func waitCancelReady(te *EventTracing) bool {
	for range 50 {
		if te.cancelCtx != nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestNewMgrTracingEvent(t *testing.T) {
	resetRegisterState()
	t.Cleanup(resetRegisterState)

	newAttr := func(flag uint32) *EventTracingAttr {
		return &EventTracingAttr{
			Flag:        flag,
			Interval:    1,
			TracingData: &stubEvent{startFunc: func(context.Context) error { return nil }},
		}
	}

	RegisterEventTracing("trace_only", func() (*EventTracingAttr, error) {
		return newAttr(FlagTracing), nil
	})
	RegisterEventTracing("metric_only", func() (*EventTracingAttr, error) {
		return newAttr(FlagMetric), nil
	})

	mgr, err := NewMgrTracingEvent(nil)
	if err != nil {
		t.Errorf("NewMgrTracingEvent() error=%v", err)
		return
	}

	if got, want := len(mgr.tracingEvents), 1; got != want {
		t.Errorf("tracingEvents len=%d, want %d", got, want)
		return
	}

	if _, ok := mgr.tracingEvents["trace_only"]; !ok {
		t.Errorf("trace_only should be included in manager")
		return
	}

	if _, ok := mgr.tracingEvents["metric_only"]; ok {
		t.Errorf("metric_only should not be included in tracing manager")
	}
}

func TestMgrTracingEventStart(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() (*MgrTracingEvent, string)
		validate func(*testing.T, error)
	}{
		{
			name: "not found",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{tracingEvents: map[string]*EventTracing{}, blackListed: nil}, "trace-not-found"
			},
			validate: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("MgrTracingEventStart() error=nil, want non-nil")
					return
				}
				if !strings.Contains(err.Error(), "not found") {
					t.Errorf("MgrTracingEventStart() error=%q, want contain %q", err.Error(), "not found")
				}
			},
		},
		{
			name: "blacklisted",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{
					tracingEvents: map[string]*EventTracing{
						"trace-2026": {name: "trace-2026"},
					},
					blackListed: []string{"trace-2026"},
				}, "trace-2026"
			},
			validate: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("MgrTracingEventStart() error=nil, want non-nil")
					return
				}
				if !strings.Contains(err.Error(), "blackListed") {
					t.Errorf("MgrTracingEventStart() error=%q, want contain %q", err.Error(), "blackListed")
				}
			},
		},
		{
			name: "already running",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{
					tracingEvents: map[string]*EventTracing{
						"trace-2026": {name: "trace-2026", isRunning: true},
					},
					blackListed: nil,
				}, "trace-2026"
			},
			validate: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("MgrTracingEventStart() error=nil, want non-nil")
					return
				}
				if !strings.Contains(err.Error(), "already running") {
					t.Errorf("MgrTracingEventStart() error=%q, want contain %q", err.Error(), "already running")
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			mgr, targetName := tests[i].setup()
			err := mgr.MgrTracingEventStart(targetName)
			tests[i].validate(t, err)
		})
	}
}

func TestMgrTracingEventStartAndStopSuccess(t *testing.T) {
	te := &EventTracing{
		ic: &stubEvent{
			startFunc: func(ctx context.Context) error {
				<-ctx.Done()
				return pkgtypes.ErrExitByCancelCtx
			},
		},
		name:     "trace-2026",
		interval: 1,
	}
	mgr := &MgrTracingEvent{
		tracingEvents: map[string]*EventTracing{"trace-2026": te},
		blackListed:   nil,
		mu:            sync.Mutex{},
	}

	startErr := mgr.MgrTracingEventStart("trace-2026")
	if startErr != nil {
		t.Errorf("MgrTracingEventStart() error=%v", startErr)
		return
	}

	if !waitCancelReady(te) {
		t.Errorf("cancelCtx was not initialized in time")
		return
	}

	stopErr := mgr.MgrTracingEventStop("trace-2026")
	if stopErr != nil {
		t.Errorf("MgrTracingEventStop() error=%v", stopErr)
	}
}

func TestMgrTracingEventStop(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() (*MgrTracingEvent, string)
		validate func(*testing.T, error)
	}{
		{
			name: "not found",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{tracingEvents: map[string]*EventTracing{}}, "trace-not-found"
			},
			validate: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("MgrTracingEventStop() error=nil, want non-nil")
					return
				}
				if !strings.Contains(err.Error(), "not found") {
					t.Errorf("MgrTracingEventStop() error=%q, want contain %q", err.Error(), "not found")
				}
			},
		},
		{
			name: "not running",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{
					tracingEvents: map[string]*EventTracing{
						"trace-2026": {name: "trace-2026", isRunning: false},
					},
				}, "trace-2026"
			},
			validate: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("MgrTracingEventStop() error=nil, want non-nil")
					return
				}
				if !strings.Contains(err.Error(), "not running") {
					t.Errorf("MgrTracingEventStop() error=%q, want contain %q", err.Error(), "not running")
				}
			},
		},
		{
			name: "running",
			setup: func() (*MgrTracingEvent, string) {
				return &MgrTracingEvent{
					tracingEvents: map[string]*EventTracing{
						"trace-2026": {name: "trace-2026", isRunning: true, cancelCtx: func() {}},
					},
				}, "trace-2026"
			},
			validate: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("MgrTracingEventStop() error=%v, want nil", err)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			mgr, targetName := tests[i].setup()
			err := mgr.MgrTracingEventStop(targetName)
			tests[i].validate(t, err)
		})
	}
}

func TestMgrTracingInfoDump(t *testing.T) {
	tests := []struct {
		nameKey   string
		wantName  string
		wantRun   bool
		wantHit   int
		wantIntvl int
		wantFlag  uint32
	}{
		{nameKey: "trace-2026", wantName: "trace-2026", wantRun: true, wantHit: 3, wantIntvl: 2, wantFlag: FlagTracing},
		{nameKey: "trace-2027", wantName: "trace-2027", wantRun: false, wantHit: 1, wantIntvl: 5, wantFlag: FlagMetric | FlagTracing},
	}

	mgr := &MgrTracingEvent{
		tracingEvents: map[string]*EventTracing{},
	}
	for i := range tests {
		mgr.tracingEvents[tests[i].nameKey] = &EventTracing{
			name:      tests[i].wantName,
			isRunning: tests[i].wantRun,
			hitCount:  tests[i].wantHit,
			interval:  tests[i].wantIntvl,
			flag:      tests[i].wantFlag,
		}
	}

	info := mgr.MgrTracingInfoDump()
	if len(info) != len(tests) {
		t.Errorf("MgrTracingInfoDump len=%d, want %d", len(info), len(tests))
		return
	}

	for i := range tests {
		t.Run(tests[i].nameKey, func(t *testing.T) {
			got := info[tests[i].nameKey]
			if got == nil {
				t.Errorf("info[%q]=nil, want non-nil", tests[i].nameKey)
				return
			}
			if got.Name != tests[i].wantName {
				t.Errorf("info[%q].Name=%q, want %q", tests[i].nameKey, got.Name, tests[i].wantName)
			}
			if got.Running != tests[i].wantRun {
				t.Errorf("info[%q].Running=%v, want %v", tests[i].nameKey, got.Running, tests[i].wantRun)
			}
			if got.HitCount != tests[i].wantHit {
				t.Errorf("info[%q].HitCount=%d, want %d", tests[i].nameKey, got.HitCount, tests[i].wantHit)
			}
			if got.Interval != tests[i].wantIntvl {
				t.Errorf("info[%q].Interval=%d, want %d", tests[i].nameKey, got.Interval, tests[i].wantIntvl)
			}
			if got.Flag != tests[i].wantFlag {
				t.Errorf("info[%q].Flag=%d, want %d", tests[i].nameKey, got.Flag, tests[i].wantFlag)
			}
		})
	}
}
