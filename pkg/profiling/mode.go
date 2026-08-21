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

package profiling

import (
	"fmt"
	"slices"
)

type MemoryMode string

const (
	MemoryModeUnknown       MemoryMode = ""
	MemoryModeObjectAlloc   MemoryMode = "object_alloc"
	MemoryModeObjectUsage   MemoryMode = "object_usage"
	MemoryModeVirtualAlloc  MemoryMode = "virtual_alloc"
	MemoryModePhysicalAlloc MemoryMode = "physical_alloc"
	MemoryModePhysicalUsage MemoryMode = "physical_usage"
)

// CPUMode selects whether native CPU profiling samples running tasks or
// attributes time spent descheduled to the stack that caused the deschedule.
type CPUMode string

const (
	CPUModeUnknown CPUMode = ""
	CPUModeOnCPU   CPUMode = "oncpu"
	CPUModeOffCPU  CPUMode = "offcpu"
)

// OffCPUPhase selects which part of a deschedule interval is accumulated.
type OffCPUPhase string

const (
	OffCPUPhaseUnknown  OffCPUPhase = ""
	OffCPUPhaseAll      OffCPUPhase = "all"
	OffCPUPhaseBlocked  OffCPUPhase = "blocked"
	OffCPUPhaseRunqueue OffCPUPhase = "runqueue"
)

func ParseMemoryMode(value string) (MemoryMode, error) {
	mode := MemoryMode(value)
	for _, capability := range capabilities {
		if slices.Contains(capability.MemoryModes, mode) {
			return mode, nil
		}
	}
	return MemoryModeUnknown, fmt.Errorf("unsupported memory mode %q", value)
}

func ParseCPUMode(value string) (CPUMode, error) {
	mode := CPUMode(value)
	if mode == CPUModeOnCPU || mode == CPUModeOffCPU {
		return mode, nil
	}
	return CPUModeUnknown, fmt.Errorf("unsupported CPU mode %q", value)
}

func ParseOffCPUPhase(value string) (OffCPUPhase, error) {
	phase := OffCPUPhase(value)
	if phase == OffCPUPhaseAll || phase == OffCPUPhaseBlocked || phase == OffCPUPhaseRunqueue {
		return phase, nil
	}
	return OffCPUPhaseUnknown, fmt.Errorf("unsupported off-CPU phase %q", value)
}
