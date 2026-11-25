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

package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"
)

// nativeStackResolver borrows map access and the profiler's symbol resolver.
// Callers own caches: A/B drains can cache missing stacks, while off-CPU batches
// retry missing stacks in their live map.
type nativeStackResolver struct {
	bpf                bpf.BPF
	usym               *symbol.UsymResolver
	stackMapID         uint32
	fallbackStackMapID uint32
}

// validateStackID reports whether an ID returned by bpf_get_stackid can index a stack map.
// Zero is a valid key; only negative values indicate lookup errors.
func validateStackID(stackID int32) bool {
	return stackID >= 0
}

// Only missing IDs can belong to the other retained-memory stack map.
func (r *nativeStackResolver) resolveKernelFrames(
	kernelStackID int32,
) ([]types.ProfileFrame, error) {
	stack, err := r.resolveKernelStack(r.stackMapID, kernelStackID)
	if errors.Is(err, bpfmap.ErrStackNotFound) && r.fallbackStackMapID != 0 {
		return r.resolveKernelStack(r.fallbackStackMapID, kernelStackID)
	}

	return stack, err
}

func (r *nativeStackResolver) resolveUserFrames(
	userStackID int32,
	pid uint32,
) ([]types.ProfileFrame, error) {
	stack, err := r.resolveUserStack(r.stackMapID, userStackID, pid)
	if errors.Is(err, bpfmap.ErrStackNotFound) && r.fallbackStackMapID != 0 {
		return r.resolveUserStack(r.fallbackStackMapID, userStackID, pid)
	}

	return stack, err
}

func (r *nativeStackResolver) resolveKernelStack(stackMapID uint32, stackID int32) ([]types.ProfileFrame, error) {
	if !validateStackID(stackID) {
		return nil, nil
	}

	trace, err := bpfmap.ReadStackTrace(r.bpf, stackMapID, uint32(stackID))
	if err != nil {
		return nil, err
	}

	return symbolizedFrames(trace[:], symbol.KsymStackStrsReversed(trace[:], len(trace))), nil
}

func (r *nativeStackResolver) resolveUserStack(stackMapID uint32, stackID int32, pid uint32) ([]types.ProfileFrame, error) {
	if !validateStackID(stackID) {
		return nil, nil
	}

	trace, err := bpfmap.ReadStackTrace(r.bpf, stackMapID, uint32(stackID))
	if err != nil {
		return nil, err
	}

	return symbolizedFrames(trace[:], r.usym.UsymStackStrsReversed(pid, trace[:], len(trace))), nil
}

// The symbol resolver emits one root-first name per nonzero address. Its
// documented failure strings must retain the address instead of merging all
// unresolved instructions into a single stack identity.
func symbolizedFrames(addresses []uint64, names []string) []types.ProfileFrame {
	frames := make([]types.ProfileFrame, len(names))
	for i, name := range names {
		if name == "" || name == "unknown" || strings.HasPrefix(name, "unknown ") || strings.HasPrefix(name, "[unknown]/") {
			name = fmt.Sprintf("0x%x", addresses[len(names)-1-i])
		}

		frames[i] = types.ProfileFrame{Name: name}
	}

	return frames
}
