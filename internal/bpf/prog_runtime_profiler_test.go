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

package bpf

import (
	"errors"
	"math"
	"slices"
	"sync"
	"testing"

	"huatuo-bamai/internal/bpf/abi"
)

type fakeProgRuntimeProfile struct {
	stats      progRuntimeStats
	readErr    error
	closeErr   error
	closeCount int
}

func (f *fakeProgRuntimeProfile) Read() (progRuntimeStats, error) {
	return f.stats, f.readErr
}

func (f *fakeProgRuntimeProfile) Close() error {
	f.closeCount++
	return f.closeErr
}

func TestDefaultBPFCloseStopsProfiles(t *testing.T) {
	originalManager := progRuntime
	manager := newProgRuntimeManager(func(uint32, string) (progRuntimeProfile, error) {
		return &fakeProgRuntimeProfile{}, nil
	})
	progRuntime = manager
	t.Cleanup(func() { progRuntime = originalManager })

	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure profiler: %v", err)
	}
	profile := &fakeProgRuntimeProfile{}
	manager.newProfile = func(uint32, string) (progRuntimeProfile, error) { return profile, nil }
	instances := manager.start([]ProgramInfo{{ID: 1, Name: "test"}}, progRuntimeObjectName)
	b := &defaultBPF{progProfiles: instances}

	if err := b.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if profile.closeCount != 1 {
		t.Fatalf("profile close count = %d, want 1", profile.closeCount)
	}
	metrics := manager.getData()
	if len(metrics) != 1 || metrics[0].Up {
		t.Fatalf("metrics after Close() = %+v, want one inactive metric", metrics)
	}
}

func TestDefaultBPFCloseReturnsProfileCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	manager := newProgRuntimeManager(func(uint32, string) (progRuntimeProfile, error) {
		return &fakeProgRuntimeProfile{closeErr: closeErr}, nil
	})
	originalManager := progRuntime
	progRuntime = manager
	t.Cleanup(func() { progRuntime = originalManager })
	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure profiler: %v", err)
	}
	instances := manager.start([]ProgramInfo{{ID: 1, Name: "test"}}, progRuntimeObjectName)

	b := &defaultBPF{progProfiles: instances}
	if err := b.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want %v", err, closeErr)
	}
}

func TestNewBPFProgRuntimeByIDValidatesArguments(t *testing.T) {
	tests := []struct {
		name       string
		programID  uint32
		objectPath string
	}{
		{name: "zero program ID", objectPath: progRuntimeObjectName},
		{name: "empty object path", programID: 337},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newBPFProgRuntimeByID(tt.programID, tt.objectPath); err == nil {
				t.Fatal("newBPFProgRuntimeByID() error = nil, want non-nil")
			}
		})
	}
}

func TestAggregateProgRuntimeStats(t *testing.T) {
	tests := []struct {
		name   string
		values []abi.BPFProgRuntimeValue
		want   progRuntimeStats
	}{
		{
			name: "no CPUs",
		},
		{
			name: "one CPU",
			values: []abi.BPFProgRuntimeValue{
				{StartTimeNS: 99, RunTimeNS: 100, RunCount: 2},
			},
			want: progRuntimeStats{RunTimeNS: 100, RunCount: 2},
		},
		{
			name: "all CPUs",
			values: []abi.BPFProgRuntimeValue{
				{RunTimeNS: 100, RunCount: 2},
				{RunTimeNS: 250, RunCount: 3},
				{RunTimeNS: 50, RunCount: 1},
			},
			want: progRuntimeStats{RunTimeNS: 400, RunCount: 6},
		},
		{
			name: "saturates overflow",
			values: []abi.BPFProgRuntimeValue{
				{RunTimeNS: math.MaxUint64, RunCount: math.MaxUint64},
				{RunTimeNS: 1, RunCount: 1},
			},
			want: progRuntimeStats{
				RunTimeNS: math.MaxUint64,
				RunCount:  math.MaxUint64,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggregateProgRuntimeStats(tt.values)
			if got != tt.want {
				t.Fatalf("aggregateProgRuntimeStats() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSetProgRuntimeProfilerFromOptions(t *testing.T) {
	t.Cleanup(func() {
		if err := progRuntime.configure(false, false, nil); err != nil {
			t.Errorf("reset BPF program runtime profiler: %v", err)
		}
	})

	tests := []struct {
		name           string
		opts           ProgRuntimeOptions
		wantEnabled    bool
		wantAll        bool
		wantTargetName string
	}{
		{name: "disabled"},
		{
			name:        "empty targets profile all",
			opts:        ProgRuntimeOptions{Enabled: true},
			wantEnabled: true,
			wantAll:     true,
		},
		{
			name: "targets select allowlist",
			opts: ProgRuntimeOptions{
				Enabled: true,
				Targets: []string{" read "},
			},
			wantEnabled:    true,
			wantTargetName: "read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := setProgRuntimeProfiler(tt.opts); err != nil {
				t.Fatalf("configureProgRuntime() error = %v", err)
			}

			progRuntime.Lock()
			defer progRuntime.Unlock()
			if progRuntime.enabled != tt.wantEnabled || progRuntime.all != tt.wantAll {
				t.Fatalf("policy = enabled:%v all:%v, want enabled:%v all:%v", progRuntime.enabled, progRuntime.all, tt.wantEnabled, tt.wantAll)
			}
			if tt.wantTargetName != "" {
				if _, ok := progRuntime.targets[tt.wantTargetName]; !ok {
					t.Fatalf("targets = %v, want %q", progRuntime.targets, tt.wantTargetName)
				}
			}
		})
	}
}

func TestNormalizeProgRuntimeTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		want    []string
		wantErr bool
	}{
		{name: "empty profiles all"},
		{
			name:    "trims names",
			targets: []string{" read ", "write"},
			want:    []string{"read", "write"},
		},
		{name: "empty name", targets: []string{""}, wantErr: true},
		{
			name:    "duplicate name",
			targets: []string{"read", " read "},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeProgRuntimeTargets(tt.targets)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeProgRuntimeTargets() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("normalizeProgRuntimeTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProgRuntimeManagerAllowlistAndNewGeneration(t *testing.T) {
	attachErr := errors.New("attach failed")
	profiles := make(map[uint32]*fakeProgRuntimeProfile)
	attachCalls := make(map[uint32]int)
	manager := newProgRuntimeManager(func(programID uint32, objectPath string) (progRuntimeProfile, error) {
		attachCalls[programID]++
		if objectPath != progRuntimeObjectName {
			t.Fatalf("objectPath = %q", objectPath)
		}
		if programID == 2 {
			return nil, attachErr
		}
		profile := &fakeProgRuntimeProfile{stats: progRuntimeStats{RunCount: uint64(programID)}}
		profiles[programID] = profile
		return profile, nil
	})
	if err := manager.configure(true, false, []string{"read", "write"}); err != nil {
		t.Fatalf("configure() error = %v", err)
	}

	instances := manager.start([]ProgramInfo{
		{ID: 1, Name: "read"},
		{ID: 2, Name: "write"},
		{ID: 3, Name: "ignored"},
	}, progRuntimeObjectName)
	if len(instances) != 2 {
		t.Fatalf("start() returned %d instances, want 2", len(instances))
	}
	if attachCalls[1] != 1 || attachCalls[2] != 1 || attachCalls[3] != 0 {
		t.Fatalf("attach calls = %v", attachCalls)
	}

	metrics := manager.getData()
	if len(metrics) != 2 {
		t.Fatalf("metrics() returned %d programs, want 2", len(metrics))
	}
	if metrics[0].ProgramName != "read" || !metrics[0].Up || metrics[0].RunCount != 1 {
		t.Fatalf("read metric = %+v", metrics[0])
	}
	if metrics[1].ProgramName != "write" || metrics[1].Up || metrics[1].AttachFailures != 1 {
		t.Fatalf("write metric = %+v", metrics[1])
	}
	_ = manager.getData()
	if attachCalls[2] != 1 {
		t.Fatalf("failed instance attach calls = %d, want exactly 1", attachCalls[2])
	}

	if err := manager.stop(instances); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	if profiles[1].closeCount != 1 {
		t.Fatalf("read close count = %d, want 1", profiles[1].closeCount)
	}

	// A new load is a new instance and is evaluated once even when the old ID failed.
	reloaded := manager.start([]ProgramInfo{{ID: 4, Name: "write"}}, progRuntimeObjectName)
	if attachCalls[4] != 1 {
		t.Fatalf("new generation attach calls = %d, want 1", attachCalls[4])
	}
	metrics = manager.getData()
	if !metrics[1].Up || metrics[1].AttachFailures != 1 || metrics[1].RunCount != 4 {
		t.Fatalf("reloaded write metric = %+v", metrics[1])
	}
	if err := manager.stop(reloaded); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
}

func TestProgRuntimeManagerAllProgramsAndAttachIsolation(t *testing.T) {
	manager := newProgRuntimeManager(func(programID uint32, _ string) (progRuntimeProfile, error) {
		if programID == 2 {
			return nil, errors.New("attach failed")
		}
		return &fakeProgRuntimeProfile{stats: progRuntimeStats{RunCount: 3}}, nil
	})
	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	instances := manager.start([]ProgramInfo{
		{ID: 1, Name: "same"},
		{ID: 2, Name: "same"},
	}, progRuntimeObjectName)

	metrics := manager.getData()
	if len(metrics) != 1 || metrics[0].Up || metrics[0].AttachFailures != 1 || metrics[0].RunCount != 3 {
		t.Fatalf("metrics() = %+v, want partial counters and up=0", metrics)
	}
	if err := manager.stop(instances); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
}

func TestProgRuntimeManagerCounterContinuity(t *testing.T) {
	profiles := make(map[uint32]*fakeProgRuntimeProfile)
	manager := newProgRuntimeManager(func(programID uint32, _ string) (progRuntimeProfile, error) {
		profile := &fakeProgRuntimeProfile{}
		profiles[programID] = profile
		return profile, nil
	})
	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}

	first := manager.start([]ProgramInfo{{ID: 1, Name: "read"}}, progRuntimeObjectName)
	profiles[1].stats = progRuntimeStats{RunCount: 5, RunTimeNS: 500}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", Up: true, RunCount: 5, RunTimeNS: 500,
	})
	if err := manager.stop(first); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", RunCount: 5, RunTimeNS: 500,
	})

	second := manager.start([]ProgramInfo{{ID: 2, Name: "read"}}, progRuntimeObjectName)
	profiles[2].stats = progRuntimeStats{RunCount: 3, RunTimeNS: 300}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", Up: true, RunCount: 8, RunTimeNS: 800,
	})

	third := manager.start([]ProgramInfo{{ID: 3, Name: "read"}}, progRuntimeObjectName)
	profiles[3].stats = progRuntimeStats{RunCount: 2, RunTimeNS: 200}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", Up: true, RunCount: 10, RunTimeNS: 1000,
	})
	if err := manager.stop(second); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", Up: true, RunCount: 10, RunTimeNS: 1000,
	})

	profiles[3].stats = progRuntimeStats{RunCount: 4, RunTimeNS: 400}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", Up: true, RunCount: 12, RunTimeNS: 1200,
	})
	if err := manager.stop(third); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	_ = manager.stop(third) // idempotent: second call must be a no-op.
	if profiles[3].closeCount != 1 {
		t.Fatalf("third close count = %d, want 1", profiles[3].closeCount)
	}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", RunCount: 12, RunTimeNS: 1200,
	})
}

func TestProgRuntimeManagerReadFailureUsesLastSnapshot(t *testing.T) {
	readErr := errors.New("read failed")
	profile := &fakeProgRuntimeProfile{stats: progRuntimeStats{RunCount: 7, RunTimeNS: 700}}
	manager := newProgRuntimeManager(func(uint32, string) (progRuntimeProfile, error) { return profile, nil })
	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	instances := manager.start([]ProgramInfo{{ID: 1, Name: "read"}}, progRuntimeObjectName)
	_ = manager.getData()

	profile.readErr = readErr
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", RunCount: 7, RunTimeNS: 700,
	})
	// The final read fails by design; stop reports it while keeping the
	// last snapshot, so only the error is asserted here.
	stopErr := manager.stop(instances)
	if stopErr == nil || !errors.Is(stopErr, readErr) {
		t.Fatalf("stop() error = %v, want wrapped readErr", stopErr)
	}
	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", RunCount: 7, RunTimeNS: 700,
	})
}

func TestProgRuntimeManagerConfiguration(t *testing.T) {
	manager := newProgRuntimeManager(func(uint32, string) (progRuntimeProfile, error) {
		return &fakeProgRuntimeProfile{}, nil
	})
	if err := manager.configure(false, false, nil); err != nil {
		t.Fatalf("disabled configure() error = %v", err)
	}
	if got := manager.start([]ProgramInfo{{ID: 1, Name: "read"}}, progRuntimeObjectName); len(got) != 0 {
		t.Fatalf("disabled start() returned %d instances", len(got))
	}

	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("enabled configure() error = %v", err)
	}
	instances := manager.start([]ProgramInfo{{ID: 1, Name: "read"}}, progRuntimeObjectName)
	if err := manager.configure(true, true, nil); err == nil {
		t.Fatal("configure() while active error = nil, want non-nil")
	}
	if err := manager.stop(instances); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
}

func TestProgRuntimeManagerConcurrentReadAndStop(t *testing.T) {
	profile := &fakeProgRuntimeProfile{stats: progRuntimeStats{RunCount: 1, RunTimeNS: 10}}
	manager := newProgRuntimeManager(func(uint32, string) (progRuntimeProfile, error) { return profile, nil })
	if err := manager.configure(true, true, nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	instances := manager.start([]ProgramInfo{{ID: 1, Name: "read"}}, progRuntimeObjectName)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.getData()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := manager.stop(instances); err != nil {
			t.Errorf("stop() error = %v", err)
		}
	}()
	wg.Wait()

	assertProgRuntimeMetric(t, manager.getData(), ProgRuntimeMetric{
		ProgramName: "read", RunCount: 1, RunTimeNS: 10,
	})
	if profile.closeCount != 1 {
		t.Fatalf("close count = %d, want 1", profile.closeCount)
	}
}

func assertProgRuntimeMetric(t *testing.T, got []ProgRuntimeMetric, want ProgRuntimeMetric) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("metrics() = %+v, want %+v", got, want)
	}
}
