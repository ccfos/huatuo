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

// Mode selects the profiling strategy for a type and language combination.
type Mode string

const (
	ModeUnknown       Mode = ""
	ModeObjectAlloc   Mode = "object_alloc"
	ModeObjectUsage   Mode = "object_usage"
	ModeVirtualAlloc  Mode = "virtual_alloc"
	ModePhysicalAlloc Mode = "physical_alloc"
	ModePhysicalUsage Mode = "physical_usage"
	ModeOnCPU         Mode = "oncpu"
	ModeOffCPU        Mode = "offcpu"
)

// OffCPUPhase selects which part of a deschedule interval is accumulated.
type OffCPUPhase string

const (
	OffCPUPhaseUnknown  OffCPUPhase = ""
	OffCPUPhaseAll      OffCPUPhase = "all"
	OffCPUPhaseBlocked  OffCPUPhase = "blocked"
	OffCPUPhaseRunqueue OffCPUPhase = "runqueue"
)

// ParseMode parses a public profiling mode value.
func ParseMode(value string) (Mode, error) {
	mode := Mode(value)
	for i := range capabilities {
		capability := &capabilities[i]
		if slices.Contains(capability.Modes, mode) {
			return mode, nil
		}
	}
	return ModeUnknown, fmt.Errorf("unsupported profiling mode %q", value)
}

func ParseOffCPUPhase(value string) (OffCPUPhase, error) {
	phase := OffCPUPhase(value)
	if phase == OffCPUPhaseAll || phase == OffCPUPhaseBlocked || phase == OffCPUPhaseRunqueue {
		return phase, nil
	}
	return OffCPUPhaseUnknown, fmt.Errorf("unsupported off-CPU phase %q", value)
}
