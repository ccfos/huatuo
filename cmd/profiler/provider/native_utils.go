// Copyright 2025 The HuaTuo Authors
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
	"fmt"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
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

// activeRing represents a frozen ring buffer that is ready to be drained.
// It contains the reader for the ring buffer and the index to track sample counts.
type activeRing struct {
	reader         bpf.PerfEventReader
	stackMapID     uint32
	sampleCountIdx uint32
}

// advanceSwapParity increments the BPF write-parity counter so the kernel
// switches to the other buffer pair, then returns the now-frozen (drainable)
// ring along with the sample-count index used to track how many events the
// BPF side wrote. The caller reads and resets that count while draining.
//
// This function is shared between CPU and Memory profilers to handle
// the A/B ring buffer parity swap mechanism consistently.
func advanceSwapParity(
	b bpf.BPF,
	readerA, readerB bpf.PerfEventReader,
	stateMapID uint32,
	stackMapAName, stackMapBName string,
) (activeRing, error) {
	val, err := bpfmap.ReadUint64(b, stateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return activeRing{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring activeRing
	if val%2 == 0 {
		ring = activeRing{
			reader:         readerA,
			stackMapID:     b.MapIDByName(stackMapAName),
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
	} else {
		ring = activeRing{
			reader:         readerB,
			stackMapID:     b.MapIDByName(stackMapBName),
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
	}

	if err := bpfmap.WriteUint64(b, stateMapID, bpfmap.TransferCountIdx, val+1); err != nil {
		return activeRing{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
}
