// Copyright 2025, 2026 The HuaTuo Authors
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
	"fmt"
	"slices"
	"sync"
)

type TracingManager struct {
	tracingEvents map[string]*EventTracing
	mu            sync.Mutex
	blackListed   []string
}

func NewManager(blackListed []string) (*TracingManager, error) {
	tracings, err := NewRegister(blackListed)
	if err != nil {
		return nil, err
	}

	tracingEvents := make(map[string]*EventTracing)
	for key, trace := range tracings {
		if trace.Flag&FlagTracing == 0 {
			continue
		}
		tracingEvents[key] = NewTracingEvent(trace, key)
	}

	return &TracingManager{tracingEvents: tracingEvents, blackListed: blackListed}, nil
}

func (mgr *TracingManager) Start() error {
	for name := range mgr.tracingEvents {
		if err := mgr.StartByName(name); err != nil {
			return err
		}
	}

	return nil
}

func (mgr *TracingManager) StartByName(name string) error {
	te, ok := mgr.tracingEvents[name]
	if !ok {
		return fmt.Errorf("%q not found", name)
	}

	if slices.Contains(mgr.blackListed, name) {
		te.isRunning = false
		return fmt.Errorf("%q blackListed", name)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if te.isRunning {
		return fmt.Errorf("%q already running", name)
	}

	return te.Start()
}

func (mgr *TracingManager) Stop() error {
	for name := range mgr.tracingEvents {
		if err := mgr.StopByName(name); err != nil {
			return err
		}
	}
	return nil
}

func (mgr *TracingManager) StopByName(name string) error {
	te, ok := mgr.tracingEvents[name]
	if !ok {
		return fmt.Errorf("%q not found", name)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if !te.isRunning {
		return fmt.Errorf("%q not running", name)
	}

	te.Stop()
	return nil
}

// Dump gets all tracer info
func (mgr *TracingManager) Dump() map[string]*EventTracingInfo {
	dump := make(map[string]*EventTracingInfo)
	for name, c := range mgr.tracingEvents {
		mgr.mu.Lock()
		dump[name] = c.Info()
		mgr.mu.Unlock()
	}
	return dump
}
