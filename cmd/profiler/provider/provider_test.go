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

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestNewSelectsAggregationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		implementation profiling.Implementation
		typ            profiling.Type
		mode           profiling.Mode
		aggregation    pipeline.AggregationMode
	}{
		{
			name:           "native oncpu",
			implementation: profiling.ImplementationNative,
			typ:            profiling.TypeCPU,
			mode:           profiling.ModeOnCPU,
		},
		{
			name:           "native offcpu",
			implementation: profiling.ImplementationNative,
			typ:            profiling.TypeCPU,
			mode:           profiling.ModeOffCPU,
		},
		{
			name:           "native virtual allocation",
			implementation: profiling.ImplementationNative,
			typ:            profiling.TypeMemory,
			mode:           profiling.ModeVirtualAlloc,
		},
		{
			name:           "native physical allocation",
			implementation: profiling.ImplementationNative,
			typ:            profiling.TypeMemory,
			mode:           profiling.ModePhysicalAlloc,
		},
		{
			name:           "native physical usage",
			implementation: profiling.ImplementationNative,
			typ:            profiling.TypeMemory,
			mode:           profiling.ModePhysicalUsage,
			aggregation:    pipeline.AggregationModeOneShot,
		},
		{
			name:           "java cpu",
			implementation: profiling.ImplementationJava,
			typ:            profiling.TypeCPU,
			aggregation:    pipeline.AggregationModeOneShot,
		},
		{
			name:           "java memory",
			implementation: profiling.ImplementationJava,
			typ:            profiling.TypeMemory,
			mode:           profiling.ModeObjectUsage,
			aggregation:    pipeline.AggregationModeOneShot,
		},
		{
			name:           "python cpu",
			implementation: profiling.ImplementationPython,
			typ:            profiling.TypeCPU,
			aggregation:    pipeline.AggregationModeOneShot,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				Type:                tc.typ,
				Mode:                tc.mode,
				Freq:                100,
				Duration:            time.Second,
				AggregationInterval: time.Second,
			}
			before := cfg
			task, err := New(tc.implementation, &cfg)
			if err != nil {
				t.Fatal(err)
			}

			if task.AggregationMode != tc.aggregation {
				t.Fatalf("mode = %v, want %v", task.AggregationMode, tc.aggregation)
			}

			if !reflect.DeepEqual(cfg, before) {
				t.Fatalf("construction modified configuration: %+v", cfg)
			}
		})
	}
}

func TestNewOwnsConfigurationAndCollectorState(t *testing.T) {
	cfg := Config{
		Type:        profiling.TypeCPU,
		Mode:        profiling.ModeOnCPU,
		PIDs:        []int{123},
		CPUIDs:      []int{0},
		Freq:        100,
		ContainerID: "first",
	}
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
		aggr      pipeline.AggregationConfig
		container string
	}{
		{
			name:      "first",
			aggr:      first.AggregationConfig,
			container: "first",
		}, {
			name:      "second",
			aggr:      second.AggregationConfig,
			container: "second",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := tc.aggr
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
		{
			name: "unknown implementation",
			impl: "unknown",
			typ:  profiling.TypeCPU,
		},
		{
			name: "unsupported type",
			impl: profiling.ImplementationPython,
			typ:  profiling.TypeMemory,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.impl, &Config{Type: tc.typ}); err == nil {
				t.Fatal("expected unsupported provider error")
			}
		})
	}
}
