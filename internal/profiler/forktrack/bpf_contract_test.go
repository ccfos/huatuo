// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readProfilerHeader(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "bpf", "include", "bpf_profiler.h"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestBPFContractContainsLifecycleHooksAndMaps(t *testing.T) {
	header := readProfilerHeader(t)
	for _, required := range []string{
		`SEC("raw_tracepoint/sched_process_fork")`,
		`SEC("raw_tracepoint/sched_process_exit")`,
		`SEC("tracepoint/sched/sched_process_exec")`,
		`} fork_pid_map SEC(".maps")`,
		`} fork_stats SEC(".maps")`,
		`} fork_rate_map SEC(".maps")`,
		`COMPAT_BPF_F_NO_PREALLOC`,
		`child_tgid = BPF_CORE_READ(child_task, tgid)`,
		`live = BPF_CORE_READ(task, signal, live.counter)`,
		`old_pid = ctx->old_pid`,
	} {
		if !strings.Contains(header, required) {
			t.Errorf("bpf_profiler.h is missing %q", required)
		}
	}
}

func TestBPFContractUsesV1CompatibleAtomics(t *testing.T) {
	header := readProfilerHeader(t)
	// The project compiles with -mcpu=v1. Consuming an XADD return value asks
	// LLVM for a v3 atomic-fetch instruction and breaks the supported build.
	for _, forbidden := range []string{
		`= __sync_fetch_and_add`,
		`__sync_fetch_and_sub`,
	} {
		if strings.Contains(header, forbidden) {
			t.Errorf("BPF v1-incompatible returned atomic found: %q", forbidden)
		}
	}
}

func TestBPFStatsABIFieldOrder(t *testing.T) {
	header := readProfilerHeader(t)
	start := strings.Index(header, "struct profiler_fork_stats_t {")
	if start < 0 {
		t.Fatal("stats struct not found")
	}
	end := strings.Index(header[start:], "};")
	if end < 0 {
		t.Fatal("stats struct terminator not found")
	}
	block := header[start : start+end]
	fields := []string{
		"active", "accepted", "duplicate", "update_failures", "exited", "rejected_limit",
		"rejected_rate", "window_start_ns", "window_events",
		"deepest_generation", "exec_migrations", "root_exited",
	}
	position := -1
	for _, field := range fields {
		next := strings.Index(block, "u64 "+field+";")
		if next <= position {
			t.Fatalf("stats ABI field %q missing or out of order", field)
		}
		position = next
	}
}
