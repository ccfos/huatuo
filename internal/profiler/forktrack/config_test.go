// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"errors"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr error
	}{
		{name: "disabled", config: Config{}},
		{name: "valid", config: Config{Enabled: true, RootPID: 42, MaxTracked: 10, Rate: 5, Burst: 2}},
		{name: "unlimited rate", config: Config{Enabled: true, RootPID: 42, MaxTracked: 10}},
		{name: "root required", config: Config{Enabled: true, MaxTracked: 10}, wantErr: ErrRootRequired},
		{name: "maximum required", config: Config{Enabled: true, RootPID: 42}, wantErr: ErrMaxTrackedZero},
		{name: "maximum too large", config: Config{Enabled: true, RootPID: 42, MaxTracked: HardMaxTracked + 1}, wantErr: ErrMaxTrackedTooLarge},
		{name: "rate too large", config: Config{Enabled: true, RootPID: 42, MaxTracked: 10, Rate: MaxRate + 1}, wantErr: ErrRateTooLarge},
		{name: "burst too large", config: Config{Enabled: true, RootPID: 42, MaxTracked: 10, Burst: MaxBurst + 1}, wantErr: ErrBurstTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestConfigConstantsAndMerge(t *testing.T) {
	config := Config{Enabled: true, RootPID: 7, MaxTracked: 23, Rate: 11, Burst: 3}
	base := map[string]any{"profiler_filter_pid": uint32(7)}
	merged, err := config.MergeConstants(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 5 {
		t.Fatalf("len(merged) = %d, want 5", len(merged))
	}
	if got := merged["profiler_follow_forks"]; got != true {
		t.Fatalf("profiler_follow_forks = %v", got)
	}
	if got := merged["profiler_fork_max_pids"]; got != uint32(23) {
		t.Fatalf("profiler_fork_max_pids = %v", got)
	}
	merged["profiler_filter_pid"] = uint32(99)
	if base["profiler_filter_pid"] != uint32(7) {
		t.Fatal("MergeConstants mutated its input")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig(99)
	if config.Enabled || config.RootPID != 99 || config.MaxTracked != DefaultMaxTracked || config.Rate != DefaultRate || config.Burst != DefaultBurst {
		t.Fatalf("unexpected default config: %+v", config)
	}
	if got, want := config.EffectiveAllowance(), uint64(DefaultRate+DefaultBurst); got != want {
		t.Fatalf("EffectiveAllowance() = %d, want %d", got, want)
	}
}

func TestConfigNormalize(t *testing.T) {
	config := Config{Enabled: true, RootPID: 88, Rate: 0, Burst: 7}.Normalize()
	if config.MaxTracked != DefaultMaxTracked {
		t.Fatalf("Normalize().MaxTracked = %d, want %d", config.MaxTracked, DefaultMaxTracked)
	}
	if config.Rate != 0 || config.Burst != 7 {
		t.Fatalf("Normalize changed explicit rate settings: %+v", config)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("normalized config is invalid: %v", err)
	}
}

func TestConfigDescription(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "disabled", config: Config{}, want: "disabled"},
		{
			name:   "limited",
			config: Config{Enabled: true, RootPID: 12, MaxTracked: 34, Rate: 56, Burst: 7},
			want:   "root=12 max=34 rate=56/s + burst 7",
		},
		{
			name:   "unlimited rate",
			config: Config{Enabled: true, RootPID: 12, MaxTracked: 34},
			want:   "root=12 max=34 rate=unlimited",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.config.Description(); got != test.want {
				t.Fatalf("Description() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConstantsRejectInvalidEnabledConfig(t *testing.T) {
	if constants, err := (Config{Enabled: true, RootPID: 1}).Constants(); !errors.Is(err, ErrMaxTrackedZero) || constants != nil {
		t.Fatalf("Constants() = %#v, %v; want nil, ErrMaxTrackedZero", constants, err)
	}
}
