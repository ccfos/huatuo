// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"reflect"
	"testing"

	"huatuo-bamai/internal/bpf"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/forktrack"
)

func TestNativeForkConfig(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		PIDs:             []int{123},
		FollowForks:      true,
		ForkMaxProcesses: 99,
		ForkRate:         12,
		ForkBurst:        4,
	}
	got, err := nativeForkConfig(pctx)
	if err != nil {
		t.Fatal(err)
	}
	want := forktrack.Config{Enabled: true, RootPID: 123, MaxTracked: 99, Rate: 12, Burst: 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nativeForkConfig() = %+v, want %+v", got, want)
	}
}

func TestApplyNativeForkTracking(t *testing.T) {
	baseConstants := map[string]any{"profiler_filter_pid": uint32(123)}
	baseOpts := []bpf.AttachOption{{ProgramName: "sample", Symbol: "hook"}}
	config := forktrack.Config{Enabled: true, RootPID: 123, MaxTracked: 9, Rate: 2, Burst: 1}
	constants, opts, err := applyNativeForkTracking(baseConstants, baseOpts, config)
	if err != nil {
		t.Fatal(err)
	}
	if constants["profiler_follow_forks"] != true || constants["profiler_fork_max_pids"] != uint32(9) {
		t.Fatalf("unexpected constants: %#v", constants)
	}
	wantOpts := []bpf.AttachOption{
		{ProgramName: programProfilerExit, Symbol: symbolProfilerExit},
		{ProgramName: programProfilerExec, Symbol: symbolProfilerExec},
		{ProgramName: programProfilerFork, Symbol: symbolProfilerFork},
		{ProgramName: "sample", Symbol: "hook"},
	}
	if !reflect.DeepEqual(opts, wantOpts) {
		t.Fatalf("opts = %#v, want %#v", opts, wantOpts)
	}
	if len(baseOpts) != 1 || len(baseConstants) != 1 {
		t.Fatal("applyNativeForkTracking mutated its inputs")
	}
}

func TestApplyNativeForkTrackingDisabled(t *testing.T) {
	config := forktrack.Config{MaxTracked: forktrack.DefaultMaxTracked, Rate: forktrack.DefaultRate, Burst: forktrack.DefaultBurst}
	_, opts, err := applyNativeForkTracking(nil, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Fatalf("disabled config attached lifecycle programs: %#v", opts)
	}
}

func TestNativeForkMapSizes(t *testing.T) {
	tests := []struct {
		name   string
		config forktrack.Config
		want   uint32
	}{
		{name: "disabled", config: forktrack.Config{}, want: 1},
		{name: "enabled", config: forktrack.Config{Enabled: true, MaxTracked: 123}, want: 123},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sizes := nativeForkMapSizes(test.config)
			if got := sizes[forktrack.PIDMapName]; got != test.want {
				t.Fatalf("PID map size = %d, want %d", got, test.want)
			}
			if len(sizes) != 1 {
				t.Fatalf("map overrides = %#v", sizes)
			}
		})
	}
}
