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

package autotracing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/timeutil"
)

func TestCPUIdleVMCooldownSurvivesBusinessGap(t *testing.T) {
	tracer := &cpuIdleTracing{containers: map[string]*containerCPUState{}, minTraceInterval: time.Minute}
	agent := &pod.Container{
		ID:          "agent",
		Type:        pod.ContainerTypeSidecar,
		RuntimeType: "io.containerd.kata-qemu.v2",
		SandboxID:   "orders-vm",
		SyncedAt:    time.Unix(1, 0),
		CgroupPath:  "/kata/orders",
		CgroupCss:   map[string]uint64{"cpu": 10},
	}
	business := *agent
	business.ID = "checkout"
	business.Type = pod.ContainerTypeNormal
	business.SyncedAt = time.Unix(2, 0)
	members := map[string]*pod.Container{agent.ID: agent, business.ID: &business}
	tracer.reconcileContainerStates(members)
	state := tracer.containers["kata:orders-vm"]
	if len(tracer.containers) != 1 || state == nil || state.containerID != business.ID {
		t.Fatalf("unexpected VM state: %+v", tracer.containers)
	}
	at := time.Unix(100, 0)
	state.lastTraceAt = at
	state.hasUsage = true
	state.hasPercent = true
	delete(members, business.ID)
	tracer.reconcileContainerStates(members)
	if tracer.containers["kata:orders-vm"] != state || state.containerID != "" || !state.lastTraceAt.Equal(
		at,
	) || state.hasUsage {
		t.Fatalf("business gap lost VM cooldown: %+v", state)
	}
	if err := tracer.updateContainerCPUStates(at.Add(time.Second)); err != nil {
		t.Errorf("inactive VM sampled: %v", err)
	}
	replacement := business
	replacement.ID = "orders-worker"
	members[replacement.ID] = &replacement
	tracer.reconcileContainerStates(members)
	if state.containerID != replacement.ID || tracer.containers["kata:orders-vm"] != state {
		t.Fatal("replacement did not reuse VM state")
	}
	state.hasPercent = true
	state.currentPercent.user = 90
	state.percentDelta.user = 90
	state.lastSampleAt = at.Add(20 * time.Second)
	if tracer.selectTraceTarget(state.lastSampleAt) != nil {
		t.Error("replacement bypassed VM cooldown")
	}
	delete(members, agent.ID)
	delete(members, replacement.ID)
	tracer.reconcileContainerStates(members)
	if len(tracer.containers) != 0 {
		t.Error("removed VM retained state")
	}
}

func TestCPUIdleReservesCooldownBeforePerfFailure(t *testing.T) {
	original := internalconfig.CoreBinDir
	internalconfig.CoreBinDir = t.TempDir()
	t.Cleanup(func() { internalconfig.CoreBinDir = original })
	count := filepath.Join(t.TempDir(), "perf-executions")
	t.Setenv("HUATUO_CPU_IDLE_EXECUTIONS", count)
	script := "#!/bin/sh\necho invocation >> \"$HUATUO_CPU_IDLE_EXECUTIONS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(internalconfig.CoreBinDir, "perf"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(internalconfig.CoreBinDir, "perf"), 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0)
	state := &containerCPUState{
		containerID:    "checkout",
		sandboxID:      "orders-vm",
		hasPercent:     true,
		currentPercent: cpuUsageBreakdown[int64]{user: 90},
		percentDelta:   cpuUsageBreakdown[int64]{user: 60},
		lastSampleAt:   at,
	}
	tracer := &cpuIdleTracing{
		containers:       map[string]*containerCPUState{"kata:orders-vm": state},
		minTraceInterval: time.Minute,
	}
	if tracer.selectTraceTarget(at) != state {
		t.Fatal("CPU spike was not selected")
	}
	if _, err := runPerfCommand(
		t.Context(),
		perfRequest{duration: time.Second, containerID: state.containerID},
	); err == nil {
		t.Fatal("expected actual perf command failure")
	}
	state.lastSampleAt = at.Add(20 * time.Second)
	if tracer.selectTraceTarget(state.lastSampleAt) != nil {
		t.Error("perf failure allowed another VM capture")
	}
	data, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "invocation"); got != 1 {
		t.Errorf("perf executions=%d, want 1", got)
	}
	state.lastSampleAt = at.Add(time.Minute)
	if tracer.selectTraceTarget(state.lastSampleAt) != state {
		t.Error("VM did not become eligible after cooldown")
	}
}

func TestCPUIdleReservesCooldownBeforeInvalidResult(t *testing.T) {
	at := time.Unix(100, 0)
	state := &containerCPUState{
		containerID:    "checkout",
		hasPercent:     true,
		currentPercent: cpuUsageBreakdown[int64]{user: 90},
		percentDelta:   cpuUsageBreakdown[int64]{user: 60},
		lastSampleAt:   at,
	}
	tracer := &cpuIdleTracing{containers: map[string]*containerCPUState{"checkout": state}, minTraceInterval: time.Minute}
	if tracer.selectTraceTarget(at) != state {
		t.Fatal("CPU spike was not selected")
	}
	if err := tracer.saveCPUIdleTrace(state, timeutil.Timestamp{Time: at}, []byte("invalid")); err == nil {
		t.Fatal("expected malformed result error")
	}
	state.lastSampleAt = at.Add(time.Second)
	if tracer.selectTraceTarget(state.lastSampleAt) != nil {
		t.Error("invalid result allowed another capture")
	}
}

func TestCPUIdleIndependentVMCooldowns(t *testing.T) {
	at := time.Unix(100, 0)
	first := &containerCPUState{
		containerID:    "checkout",
		hasPercent:     true,
		currentPercent: cpuUsageBreakdown[int64]{user: 90},
		percentDelta:   cpuUsageBreakdown[int64]{user: 60},
		lastSampleAt:   at,
	}
	second := *first
	second.containerID = "payments"
	tracer := &cpuIdleTracing{
		containers:       map[string]*containerCPUState{"kata:orders-vm": first, "kata:payments-vm": &second},
		minTraceInterval: time.Minute,
	}
	if tracer.selectTraceTarget(at) != first {
		t.Fatal("expected deterministic first business target")
	}
	if tracer.selectTraceTarget(at) != &second {
		t.Error("one VM cooldown blocked an independent VM")
	}
	if tracer.selectTraceTarget(at) != nil {
		t.Error("a VM was selected twice in its window")
	}
}
