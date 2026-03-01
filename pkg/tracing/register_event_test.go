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
	"errors"
	"sync"
	"testing"

	pkgtypes "huatuo-bamai/pkg/types"
)

func resetRegisterState() {
	factories = make(map[string]func() (*EventTracingAttr, error))
	tracingEventAttrCache = make(map[string]*EventTracingAttr)
	tracingOnceCache = sync.Once{}
}

func TestNewRegister(t *testing.T) {
	tests := []struct {
		name        string
		blackListed []string
		setup       func()
		validate    func(*testing.T, map[string]*EventTracingAttr, error)
	}{
		{
			name:        "valid event",
			blackListed: nil,
			setup: func() {
				RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
					return &EventTracingAttr{Flag: FlagTracing, Interval: 1, TracingData: nil}, nil
				})
			},
			validate: func(t *testing.T, got map[string]*EventTracingAttr, err error) {
				if err != nil {
					t.Errorf("NewRegister() error=%v, want nil", err)
				}
				if len(got) != 1 {
					t.Errorf("NewRegister() len=%d, want 1", len(got))
				}
			},
		},
		{
			name:        "blacklisted event",
			blackListed: []string{"trace-2026"},
			setup: func() {
				RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
					return &EventTracingAttr{Flag: FlagTracing, Interval: 1, TracingData: nil}, nil
				})
			},
			validate: func(t *testing.T, got map[string]*EventTracingAttr, err error) {
				if err != nil {
					t.Errorf("NewRegister() error=%v, want nil", err)
				}
				if len(got) != 0 {
					t.Errorf("NewRegister() len=%d, want 0", len(got))
				}
			},
		},
		{
			name:        "not supported event skipped",
			blackListed: nil,
			setup: func() {
				RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
					return nil, pkgtypes.ErrNotSupported
				})
			},
			validate: func(t *testing.T, got map[string]*EventTracingAttr, err error) {
				if err != nil {
					t.Errorf("NewRegister() error=%v, want nil", err)
				}
				if len(got) != 0 {
					t.Errorf("NewRegister() len=%d, want 0", len(got))
				}
			},
		},
		{
			name:        "factory returns normal error",
			blackListed: nil,
			setup: func() {
				RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
					return nil, errors.New("factory failed")
				})
			},
			validate: func(t *testing.T, got map[string]*EventTracingAttr, err error) {
				if err == nil {
					t.Errorf("NewRegister() error=nil, want non-nil")
				}
				if len(got) != 0 {
					t.Errorf("NewRegister() len=%d, want 0", len(got))
				}
			},
		},
		{
			name:        "invalid flag",
			blackListed: nil,
			setup: func() {
				RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
					return &EventTracingAttr{Flag: 0, Interval: 1, TracingData: nil}, nil
				})
			},
			validate: func(t *testing.T, got map[string]*EventTracingAttr, err error) {
				if err == nil {
					t.Errorf("NewRegister() error=nil, want non-nil")
				}
				if len(got) != 0 {
					t.Errorf("NewRegister() len=%d, want 0", len(got))
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			resetRegisterState()
			tests[i].setup()

			got, err := NewRegister(tests[i].blackListed)
			tests[i].validate(t, got, err)
		})
	}
}

func TestNewRegisterSyncOnce(t *testing.T) {
	resetRegisterState()

	RegisterEventTracing("trace-2026", func() (*EventTracingAttr, error) {
		return &EventTracingAttr{Flag: FlagTracing, Interval: 1, TracingData: nil}, nil
	})
	first, err := NewRegister(nil)
	if err != nil {
		t.Errorf("first NewRegister() error=%v", err)
	}
	if len(first) != 1 {
		t.Errorf("first NewRegister() len=%d, want 1", len(first))
	}

	RegisterEventTracing("trace-2027", func() (*EventTracingAttr, error) {
		return &EventTracingAttr{Flag: FlagTracing, Interval: 1, TracingData: nil}, nil
	})
	second, err := NewRegister(nil)
	if err != nil {
		t.Errorf("second NewRegister() error=%v", err)
	}
	if len(second) != 1 {
		t.Errorf("second NewRegister() len=%d, want 1 due to sync.Once", len(second))
	}
	if _, ok := second["trace-2027"]; ok {
		t.Errorf("second NewRegister() should not include trace-2027 because map is initialized only once")
	}
}
