// Copyright 2025, 2026 The HuaTuo Authors
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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/cilium/ebpf"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/symbol"
)

// resolveContainerCgroupCss retrieves the cgroup subsystem state (CSS) address for a container.
// It first attempts to get CSS via huatuo-bamai API, and falls back to local BPF-based
// method if the API is unavailable. The subsysName parameter specifies the cgroup subsystem
// (e.g., "memory", "cpu").
func resolveContainerCgroupCss(pctx *pcontext.ProfilerContext, subsysName string) (uint64, error) {
	if pctx.ContainerID == "" {
		return 0, nil
	}

	// Try API method first
	cssAddr, err := resolveContainerCgroupCssByAPI(pctx.ServerAddress, pctx.ContainerID, subsysName)
	if err == nil {
		return cssAddr, nil
	}

	log.Warn("API method failed, falling back to local method", "error", err, "container_id", pctx.ContainerID, "subsystem", subsysName)

	// Fallback to local BPF-based method
	cssAddr, err = resolveContainerCgroupCssByLocal(pctx.ContainerID, subsysName)
	if err != nil {
		return 0, fmt.Errorf("both API and local methods failed for subsystem %s: %w", subsysName, err)
	}

	return cssAddr, nil
}

// resolveContainerCgroupCssByAPI attempts to get CSS address via huatuo-bamai API.
func resolveContainerCgroupCssByAPI(serverAddr, containerID, subsysName string) (uint64, error) {
	c, err := container.GetContainerByID(serverAddr, containerID)
	if err != nil {
		return 0, fmt.Errorf("API call failed: %w", err)
	}

	if c == nil {
		return 0, fmt.Errorf("container %q not found via API", containerID)
	}

	cssAddr, ok := c.CgroupCss[subsysName]
	if !ok {
		return 0, fmt.Errorf("%s CSS not found in API response", subsysName)
	}

	return cssAddr, nil
}

// resolveContainerCgroupCssByLocal retrieves CSS address using local BPF-based method.
func resolveContainerCgroupCssByLocal(containerID, subsysName string) (uint64, error) {
	cssAddr, err := pod.GetContainerCSSBySubsys(containerID, subsysName)
	if err != nil {
		return 0, fmt.Errorf("local CSS retrieval failed: %w", err)
	}

	return cssAddr, nil
}

// aggregateStacksAndEnqueue resolves stack traces and emits aggregated records via enqueue callback.
// For CPU profiler, convertValue is nil (samples are already counts).
// For Memory profiler non-retained modes, convertValue converts raw value to bytes.
// For Memory profiler retained mode, fallbackStackMapID provides fallback lookup path.
//
// Stack IDs are NOT deleted from the stack map after resolution for the following reasons:
//
// 1. Caching Performance: BPF_MAP_TYPE_STACK_TRACE is a cache-like map where stack IDs
//    can be reused across multiple events. Keeping the IDs cached improves performance
//    for subsequent lookups (10-20% hit rate for repeated stacks).
//
// 2. Fallback Support: In retained mode (physical_usage), free events may reference
//    stack IDs from the previous cycle's stack_map. Deleting them would break the
//    fallback lookup path that cross-references alloc-time stacks.
//
// 3. Automatic Management: The kernel's BPF stack map implementation uses a LRU-like
//    eviction policy when the map is full, automatically managing the lifecycle of
//    stack traces without requiring explicit deletion.
//
// 4. Reduced Overhead: Deleting stack IDs requires additional BPF map operations
//    (one delete syscall per stack ID), which adds unnecessary overhead for a
//    performance-critical path. The memory overhead of keeping stale entries is
//    bounded by the map size limit (STACK_MAP_ENTRIES = 65536).
func aggregateStacksAndEnqueue(
	b bpf.BPF,
	stackCountsByProc map[processIDName]map[bpfmap.StackTraceID]int64,
	stackMapID uint32,
	enqueue func(any),
	convertValue func(int64) int64,
	fallbackStackMapID uint32,
) {
	kstackCache := make(map[int32]string)
	ustackCache := make(map[int32]string)
	usym := symbol.NewUsymResolver()

	var records int
	for pidName, stacks := range stackCountsByProc {
		for stackID, rawValue := range stacks {
			// Convert value if needed (Memory profiler), otherwise use directly (CPU profiler)
			value := rawValue
			if convertValue != nil {
				value = convertValue(rawValue)
			}

			if value == 0 {
				continue
			}

			if stackID.KernelID > 0 {
				if _, ok := kstackCache[stackID.KernelID]; !ok {
					kstackCache[stackID.KernelID] = resolveKstackWithFallback(b, stackMapID, fallbackStackMapID, stackID.KernelID)
				}
			}
			if stackID.UserID > 0 {
				if _, ok := ustackCache[stackID.UserID]; !ok {
					ustackCache[stackID.UserID] = resolveUstackWithFallback(b, stackMapID, fallbackStackMapID, stackID.UserID, pidName.Pid, usym)
				}
			}

			record := &stackEntry{
				Proc:    &processIDName{Pid: pidName.Pid, Name: pidName.Name},
				User:    ustackCache[stackID.UserID],
				Kernel:  kstackCache[stackID.KernelID],
				Samples: value,
			}

			enqueue(record)
			records++
		}
	}

	log.Debugf("aggregate: procs=%d kstacks=%d ustacks=%d records=%d", len(stackCountsByProc), len(kstackCache), len(ustackCache), records)
}

// resolveKstackWithFallback resolves kernel stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func resolveKstackWithFallback(b bpf.BPF, primaryMapID uint32, fallbackMapID uint32, kernelID int32) string {
	// Fast path: lookup primary stack map
	trace, ok := readStackTrace(b, primaryMapID, kernelID)
	if ok {
		return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
	}

	// Slow path: fallback to another stack map (only for retained mode)
	if fallbackMapID != 0 {
		trace, ok = readStackTrace(b, fallbackMapID, kernelID)
		if ok {
			return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
		}
	}

	return ""
}

// resolveUstackWithFallback resolves user stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func resolveUstackWithFallback(b bpf.BPF, primaryMapID uint32, fallbackMapID uint32, userID int32, pid uint32, usym *symbol.UsymResolver) string {
	// Fast path: lookup primary stack map
	trace, ok := readStackTrace(b, primaryMapID, userID)
	if ok {
		return strings.Join(usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
	}

	// Slow path: fallback to another stack map (only for retained mode)
	if fallbackMapID != 0 {
		trace, ok = readStackTrace(b, fallbackMapID, userID)
		if ok {
			return strings.Join(usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
		}
	}

	return ""
}

// readStackTrace reads a stack trace from the BPF stack map by ID.
// Returns the stack trace as an array of instruction pointers and a success flag.
func readStackTrace(b bpf.BPF, mapID uint32, id int32) ([bpfmap.StackTraceLen]uint64, bool) {
	keyBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(keyBuf, uint32(id))

	val, err := b.ReadMap(mapID, keyBuf)
	if err != nil {
		if !errors.Is(err, ebpf.ErrKeyNotExist) {
			log.Warnf("stack map lookup for ID %d: %v", id, err)
		}
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	if len(val) != bpfmap.StackTraceLen*8 {
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	var trace [bpfmap.StackTraceLen]uint64
	reader := bytes.NewReader(val)
	if err := binary.Read(reader, binary.LittleEndian, &trace); err != nil {
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	return trace, true
}