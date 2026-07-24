// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func TestStatsRoundTrip(t *testing.T) {
	want := Stats{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		data, err := EncodeStats(want, order)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeStats(data, order)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}

func TestDecodeStatsRejectsMalformedPayload(t *testing.T) {
	if _, err := DecodeStats(make([]byte, StatsSize-1), binary.LittleEndian); err == nil {
		t.Fatal("short payload accepted")
	}
	if _, err := DecodeStats(make([]byte, StatsSize), nil); err == nil {
		t.Fatal("nil byte order accepted")
	}
}

func TestAssessAndSummary(t *testing.T) {
	if got := Assess(false, Stats{}); got != HealthDisabled {
		t.Fatalf("disabled health = %q", got)
	}
	if got := Assess(true, Stats{}); got != HealthIdle {
		t.Fatalf("idle health = %q", got)
	}
	if got := Assess(true, Stats{Accepted: 1}); got != HealthHealthy {
		t.Fatalf("healthy status = %q", got)
	}
	stats := Stats{Accepted: 9, RejectedRate: 2, DeepestGeneration: 3}
	if got := Assess(true, stats); got != HealthLimited {
		t.Fatalf("limited status = %q", got)
	}
	if summary := Summary(true, stats); !strings.Contains(summary, "rejected_rate=2") || !strings.Contains(summary, "deepest_generation=3") {
		t.Fatalf("unexpected summary: %s", summary)
	}
}

func TestAssessLimitReasons(t *testing.T) {
	for _, stats := range []Stats{
		{UpdateFailures: 1},
		{RejectedLimit: 1},
		{RejectedRate: 1},
		{RejectedLimit: 1, RejectedRate: 1},
	} {
		if got := Assess(true, stats); got != HealthLimited {
			t.Errorf("Assess(true, %+v) = %q, want %q", stats, got, HealthLimited)
		}
	}
}

func TestSummaryReportsRootLifecycle(t *testing.T) {
	stats := Stats{Active: 2, Accepted: 4, Exited: 2, RootExited: 1}
	summary := Summary(true, stats)
	for _, part := range []string{
		"health=healthy",
		"active=2",
		"accepted=4",
		"exited=2",
		"root_exited=true",
	} {
		if !strings.Contains(summary, part) {
			t.Errorf("Summary() = %q, missing %q", summary, part)
		}
	}
}
