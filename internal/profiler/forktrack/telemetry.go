// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"encoding/binary"
	"fmt"
)

const (
	PIDMapName   = "fork_pid_map"
	StatsMapName = "fork_stats"
	StatsSize    = 12 * 8
)

// DecodeStats translates the stable eBPF map ABI into Go. The BPF objects are
// loaded on the same host, so the caller supplies native byte order explicitly.
func DecodeStats(data []byte, order binary.ByteOrder) (Stats, error) {
	if order == nil {
		return Stats{}, fmt.Errorf("byte order is required")
	}
	if len(data) < StatsSize {
		return Stats{}, fmt.Errorf("fork stats payload is %d bytes, want at least %d", len(data), StatsSize)
	}
	values := make([]uint64, 12)
	for i := range values {
		values[i] = order.Uint64(data[i*8 : (i+1)*8])
	}
	return Stats{
		Active:            values[0],
		Accepted:          values[1],
		Duplicate:         values[2],
		UpdateFailures:    values[3],
		Exited:            values[4],
		RejectedLimit:     values[5],
		RejectedRate:      values[6],
		WindowStartNS:     values[7],
		WindowEvents:      values[8],
		DeepestGeneration: values[9],
		ExecMigrations:    values[10],
		RootExited:        values[11],
	}, nil
}

// EncodeStats is useful for map fixtures and for preserving the ABI in tests.
func EncodeStats(stats Stats, order binary.ByteOrder) ([]byte, error) {
	if order == nil {
		return nil, fmt.Errorf("byte order is required")
	}
	values := [...]uint64{
		stats.Active,
		stats.Accepted,
		stats.Duplicate,
		stats.UpdateFailures,
		stats.Exited,
		stats.RejectedLimit,
		stats.RejectedRate,
		stats.WindowStartNS,
		stats.WindowEvents,
		stats.DeepestGeneration,
		stats.ExecMigrations,
		stats.RootExited,
	}
	data := make([]byte, StatsSize)
	for i, value := range values {
		order.PutUint64(data[i*8:(i+1)*8], value)
	}
	return data, nil
}

type Health string

const (
	HealthDisabled Health = "disabled"
	HealthHealthy  Health = "healthy"
	HealthLimited  Health = "limited"
	HealthIdle     Health = "idle"
)

// Assess summarizes whether limits affected coverage. Rejections are surfaced
// even if later windows recovered because losing a short-lived process can
// create an otherwise invisible gap in the profile.
func Assess(enabled bool, stats Stats) Health {
	if !enabled {
		return HealthDisabled
	}
	if stats.UpdateFailures > 0 || stats.RejectedLimit > 0 || stats.RejectedRate > 0 {
		return HealthLimited
	}
	if stats.Active == 0 && stats.Accepted == 0 {
		return HealthIdle
	}
	return HealthHealthy
}

// Summary is concise enough for profiler shutdown logs and detailed enough to
// distinguish normal process exits from coverage loss caused by protection.
func Summary(enabled bool, stats Stats) string {
	return fmt.Sprintf(
		"health=%s active=%d accepted=%d exited=%d duplicate=%d update_failures=%d rejected_limit=%d rejected_rate=%d deepest_generation=%d exec_migrations=%d root_exited=%t",
		Assess(enabled, stats), stats.Active, stats.Accepted, stats.Exited,
		stats.Duplicate, stats.UpdateFailures, stats.RejectedLimit, stats.RejectedRate,
		stats.DeepestGeneration, stats.ExecMigrations, stats.RootExited != 0,
	)
}
