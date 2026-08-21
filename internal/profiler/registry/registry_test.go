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

package registry

import (
	"context"
	"strings"
	"testing"

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/pkg/profiling"
)

// fakeProfiler satisfies Profiler with no behavior. Registry tests exercise
// the lookup tables, not sampling, so a stub is enough.
type fakeProfiler struct{}

func (fakeProfiler) Start(*pcontext.ProfilerContext) error         { return nil }
func (fakeProfiler) ReadDataLoop(context.Context, func(any)) error { return nil }
func (fakeProfiler) Stop(*pcontext.ProfilerContext) error          { return nil }

// fakeAggregator satisfies aggregator.Aggregator with no behavior.
type fakeAggregator struct{}

func (fakeAggregator) Aggregate(any)                                   {}
func (fakeAggregator) Snapshot(*pcontext.ProfilerContext) (any, error) { return nil, nil }
func (fakeAggregator) Reset()                                          {}
func (fakeAggregator) OutputFormatter() output.Formatter               { return nil }

func resetRegistry(t *testing.T) {
	t.Helper()

	saved := profilerRegistry
	profilerRegistry = map[profiling.Implementation]map[profiling.Type]ProfilerMeta{}

	t.Cleanup(func() { profilerRegistry = saved })
}

func newMeta(implementation profiling.Implementation, typ profiling.Type) ProfilerMeta {
	return ProfilerMeta{
		Type:           typ,
		Implementation: implementation,
		Description:    "fake",
		Impl:           fakeProfiler{},
		NewAggregator:  func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return fakeAggregator{}, nil },
	}
}

func TestRegisterThenGet(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	got, err := Get(profiling.ImplementationNative, profiling.TypeCPU)
	if err != nil {
		t.Fatalf(`Get("native", "cpu") error = %v, want nil`, err)
	}

	if got.Implementation != profiling.ImplementationNative || got.Type != profiling.TypeCPU {
		t.Errorf(`Get("native", "cpu") = (%q, %q), want ("native", "cpu")`,
			got.Implementation, got.Type)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal(`Register duplicate ("native", "cpu") did not panic`)
		}

		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate") {
			t.Errorf(`Register panic = %v, want message containing "duplicate"`, r)
		}
	}()

	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))
}

func TestGetFallbackMissesWhenTypeAbsent(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	// The native implementation has no memory provider registered in this test.
	if _, err := Get(profiling.ImplementationNative, profiling.TypeMemory); err == nil {
		t.Fatal(`Get("native", "memory") error = nil, want non-nil`)
	}
}

func TestGetUnknownImplementation(t *testing.T) {
	resetRegistry(t)

	if _, err := Get("unknown", profiling.TypeCPU); err == nil {
		t.Fatal(`Get("unknown", "cpu") error = nil, want non-nil`)
	}
}
