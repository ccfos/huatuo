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
	"errors"
	"testing"
	"time"

	pkgtypes "huatuo-bamai/pkg/types"
)

type traceEventStub struct {
	startFunc func(ctx context.Context) error
}

func (s *traceEventStub) Start(ctx context.Context) error {
	return s.startFunc(ctx)
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestNewTracingEvent(t *testing.T) {
	attr := &EventTracingAttr{
		Interval: 2,
		Flag:     FlagTracing,
		TracingData: &traceEventStub{
			startFunc: func(ctx context.Context) error { return nil },
		},
	}

	te := NewTracingEvent(attr, "cpu")
	if te == nil {
		t.Errorf("NewTracingEvent() should not return nil")
		return
	}
	if te.name != "cpu" {
		t.Errorf("name=%q, want %q", te.name, "cpu")
	}
	if te.interval != 2 {
		t.Errorf("interval=%d, want 2", te.interval)
	}
	if te.flag != FlagTracing {
		t.Errorf("flag=%d, want %d", te.flag, FlagTracing)
	}
}

func TestEventTracingDoStart(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *EventTracing
		validate func(*testing.T, *EventTracing)
	}{
		{
			name: "not supported sets exit",
			setup: func() *EventTracing {
				return &EventTracing{
					ic: &traceEventStub{
						startFunc: func(ctx context.Context) error { return pkgtypes.ErrNotSupported },
					},
					name: "trace-do-start",
				}
			},
			validate: func(t *testing.T, te *EventTracing) {
				if !te.exit {
					t.Errorf("exit=%v, want %v", te.exit, true)
				}
			},
		},
		{
			name: "cancel context does not set exit",
			setup: func() *EventTracing {
				return &EventTracing{
					ic: &traceEventStub{
						startFunc: func(ctx context.Context) error { return pkgtypes.ErrExitByCancelCtx },
					},
					name: "trace-do-start",
				}
			},
			validate: func(t *testing.T, te *EventTracing) {
				if te.exit {
					t.Errorf("exit=%v, want %v", te.exit, false)
				}
			},
		},
		{
			name: "normal error does not set exit",
			setup: func() *EventTracing {
				return &EventTracing{
					ic: &traceEventStub{
						startFunc: func(ctx context.Context) error { return errors.New("boom") },
					},
					name: "trace-do-start",
				}
			},
			validate: func(t *testing.T, te *EventTracing) {
				if te.exit {
					t.Errorf("exit=%v, want %v", te.exit, false)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			te := tests[i].setup()
			te.doStart()
			tests[i].validate(t, te)
		})
	}
}

func TestEventTracingStartStopAndInfo(t *testing.T) {
	te := &EventTracing{
		ic: &traceEventStub{
			startFunc: func(ctx context.Context) error {
				<-ctx.Done()
				return pkgtypes.ErrExitByCancelCtx
			},
		},
		name:     "trace-2026",
		interval: 1,
		flag:     FlagTracing,
	}

	err := te.Start()
	if err != nil {
		t.Errorf("Start() error=%v", err)
		return
	}

	ready := waitUntil(500*time.Millisecond, func() bool { return te.cancelCtx != nil && te.isRunning })
	if !ready {
		t.Errorf("tracing did not enter running state in time")
		return
	}

	info := te.Info()
	if info.Name != "trace-2026" || !info.Running || info.Interval != 1 || info.Flag != FlagTracing {
		t.Errorf("Info() mismatch: %+v", info)
	}

	te.Stop()
	stopped := waitUntil(500*time.Millisecond, func() bool { return !te.isRunning })
	if !stopped {
		t.Errorf("tracing did not stop in time")
	}
}
