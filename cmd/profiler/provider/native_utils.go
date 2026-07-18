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

	"github.com/cilium/ebpf"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
)

func newNativeBPFConstants(pid int, cssAddr uint64, threadGroup bool) map[string]any {
	return map[string]any{
		"profiler_filter_css":     cssAddr,
		"profiler_filter_pid":     uint32(pid),
		"profiler_filter_threads": threadGroup,
	}
}

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
	cssAddr, err := pod.ContainerCSSBySubsys(containerID, subsysName)
	if err != nil {
		return 0, fmt.Errorf("local CSS retrieval failed: %w", err)
	}

	return cssAddr, nil
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
