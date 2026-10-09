// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package forktrack defines the process-tree tracking contract shared by the
// profiler command, native providers, and their eBPF programs.
package forktrack

import (
	"errors"
	"fmt"
	"time"
)

const (
	DefaultMaxTracked = uint32(4096)
	DefaultRate       = uint32(1000)
	DefaultBurst      = uint32(2000)
	HardMaxTracked    = uint32(65536)
	MaxRate           = uint32(1_000_000)
	MaxBurst          = uint32(1_000_000)
	Window            = time.Second
)

var (
	ErrRootRequired       = errors.New("fork tracking requires a root PID")
	ErrMaxTrackedZero     = errors.New("maximum tracked processes must be greater than zero")
	ErrMaxTrackedTooLarge = errors.New("maximum tracked processes exceeds the BPF map capacity")
	ErrRateTooLarge       = errors.New("fork event rate limit is too large")
	ErrBurstTooLarge      = errors.New("fork event burst is too large")
)

// Config is intentionally small enough to be rewritten into BPF constants.
// A zero Rate disables event-rate limiting; MaxTracked always remains a hard
// bound so a fork storm cannot exhaust kernel memory.
type Config struct {
	Enabled    bool
	RootPID    uint32
	MaxTracked uint32
	Rate       uint32
	Burst      uint32
}

// DefaultConfig returns conservative defaults suitable for a busy service.
// Tracking is opt-in because it changes the meaning of a PID-scoped profile.
func DefaultConfig(rootPID uint32) Config {
	return Config{
		Enabled:    false,
		RootPID:    rootPID,
		MaxTracked: DefaultMaxTracked,
		Rate:       DefaultRate,
		Burst:      DefaultBurst,
	}
}

// Normalize fills omitted limits while preserving Rate=0 as an explicit way
// to disable rate limiting.
func (c Config) Normalize() Config {
	if c.MaxTracked == 0 {
		c.MaxTracked = DefaultMaxTracked
	}
	return c
}

// Validate rejects configurations that cannot be enforced safely by the BPF
// implementation. Disabled tracking accepts a zero root to support host or
// cgroup-wide profiling.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.RootPID == 0 {
		return ErrRootRequired
	}
	if c.MaxTracked == 0 {
		return ErrMaxTrackedZero
	}
	if c.MaxTracked > HardMaxTracked {
		return fmt.Errorf("%w: got %d, maximum %d", ErrMaxTrackedTooLarge, c.MaxTracked, HardMaxTracked)
	}
	if c.Rate > MaxRate {
		return fmt.Errorf("%w: got %d, maximum %d", ErrRateTooLarge, c.Rate, MaxRate)
	}
	if c.Burst > MaxBurst {
		return fmt.Errorf("%w: got %d, maximum %d", ErrBurstTooLarge, c.Burst, MaxBurst)
	}
	return nil
}

// Constants returns exactly the names consumed by bpf_profiler.h.
func (c Config) Constants() (map[string]any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return map[string]any{
		"profiler_follow_forks":  c.Enabled,
		"profiler_fork_max_pids": c.MaxTracked,
		"profiler_fork_rate":     c.Rate,
		"profiler_fork_burst":    c.Burst,
	}, nil
}

// MergeConstants adds lifecycle constants without mutating the caller's map.
func (c Config) MergeConstants(base map[string]any) (map[string]any, error) {
	forkConstants, err := c.Constants()
	if err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(base)+len(forkConstants))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range forkConstants {
		merged[key] = value
	}
	return merged, nil
}

// EffectiveAllowance reports how many fork events the first rate window can
// accept. A zero result means rate limiting is disabled.
func (c Config) EffectiveAllowance() uint64 {
	if c.Rate == 0 {
		return 0
	}
	return uint64(c.Rate) + uint64(c.Burst)
}

// Description is used in startup logs and avoids leaking implementation map
// names into user-facing diagnostics.
func (c Config) Description() string {
	if !c.Enabled {
		return "disabled"
	}
	rate := "unlimited"
	if c.Rate != 0 {
		rate = fmt.Sprintf("%d/s + burst %d", c.Rate, c.Burst)
	}
	return fmt.Sprintf("root=%d max=%d rate=%s", c.RootPID, c.MaxTracked, rate)
}
