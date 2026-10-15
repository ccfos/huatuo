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

package provider

import (
	"reflect"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregator"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestNewSelectsAggregationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name            string
		implementation  profiling.Implementation
		typ             profiling.Type
		mode            profiling.Mode
		aggregation     aggregator.AggregationMode
		managesDuration bool
	}{
		{name: "native oncpu", implementation: profiling.ImplementationNative, typ: profiling.TypeCPU, mode: profiling.ModeOnCPU},
		{name: "native offcpu", implementation: profiling.ImplementationNative, typ: profiling.TypeCPU, mode: profiling.ModeOffCPU},
		{name: "native virtual allocation", implementation: profiling.ImplementationNative, typ: profiling.TypeMemory, mode: profiling.ModeVirtualAlloc},
		{name: "native physical allocation", implementation: profiling.ImplementationNative, typ: profiling.TypeMemory, mode: profiling.ModePhysicalAlloc},
		{name: "native physical usage", implementation: profiling.ImplementationNative, typ: profiling.TypeMemory, mode: profiling.ModePhysicalUsage, aggregation: aggregator.AggregationModeOneShot},
		{name: "java cpu", implementation: profiling.ImplementationJava, typ: profiling.TypeCPU, aggregation: aggregator.AggregationModeOneShot},
		{name: "java memory", implementation: profiling.ImplementationJava, typ: profiling.TypeMemory, mode: profiling.ModeObjectUsage, aggregation: aggregator.AggregationModeOneShot},
		{name: "python cpu", implementation: profiling.ImplementationPython, typ: profiling.TypeCPU, aggregation: aggregator.AggregationModeOneShot, managesDuration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Type: tc.typ, Mode: tc.mode, Freq: 100, Duration: time.Second, AggregationInterval: time.Second}
			before := cfg
			task, err := New(tc.implementation, &cfg)
			if err != nil {
				t.Fatal(err)
			}
			if task.AggregationMode != tc.aggregation {
				t.Fatalf("mode = %v, want %v", task.AggregationMode, tc.aggregation)
			}
			_, manages := task.Impl.(interface{ ManagesDuration() })
			if manages != tc.managesDuration {
				t.Fatalf("manages duration = %v, want %v", manages, tc.managesDuration)
			}
			if !reflect.DeepEqual(cfg, before) {
				t.Fatalf("construction modified configuration: %+v", cfg)
			}
		})
	}
}

func TestNewOwnsConfigurationAndCollectorState(t *testing.T) {
	cfg := Config{Type: profiling.TypeCPU, Mode: profiling.ModeOnCPU, PIDs: []int{123}, CPUIDs: []int{0}, Freq: 100, ContainerID: "first"}
	first, err := New(profiling.ImplementationNative, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PIDs[0] = 456
	cfg.CPUIDs[0] = 1
	cfg.ContainerID = "second"
	second, err := New(profiling.ImplementationNative, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := first.Impl.(*cpuNativeProfiler)
	if !ok {
		t.Fatalf("collector type=%T", first.Impl)
	}
	b, ok := second.Impl.(*cpuNativeProfiler)
	if !ok {
		t.Fatalf("collector type=%T", second.Impl)
	}
	if a == b || a.config.PID() != 123 || b.config.PID() != 456 || a.config.CPUIDs[0] != 0 {
		t.Fatalf("tasks share collector or configuration: first=%+v second=%+v", a.config, b.config)
	}
	for _, tc := range []struct {
		name      string
		aggr      aggregator.Aggregator
		container string
	}{
		{"first", first.Aggregator, "first"}, {"second", second.Aggregator, "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window, err := tc.aggr.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if window.ContainerID != tc.container {
				t.Fatalf("container = %q, want %q", window.ContainerID, tc.container)
			}
		})
	}
}

func TestNewRejectsUnsupportedProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		impl profiling.Implementation
		typ  profiling.Type
	}{
		{"unknown implementation", "unknown", profiling.TypeCPU},
		{"unsupported type", profiling.ImplementationPython, profiling.TypeMemory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.impl, &Config{Type: tc.typ}); err == nil {
				t.Fatal("expected unsupported provider error")
			}
		})
	}
}
