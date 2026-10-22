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
	"context"
	"fmt"

	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
)

func newNativeBPFConstants(pid int, cssAddr uint64, threadGroup bool) map[string]any {
	return map[string]any{
		"profiler_filter_css":     cssAddr,
		"profiler_filter_pid":     uint32(pid),
		"profiler_filter_threads": threadGroup,
	}
}

func taskCommString(comm [bpf.TaskCommLen]byte) string {
	length := bytes.IndexByte(comm[:], 0)
	if length == -1 {
		length = len(comm)
	}
	return string(comm[:length])
}

// resolveContainerCgroupCss retrieves the cgroup subsystem state (CSS) address for a container.
// It first attempts to get CSS via huatuo-bamai API, and falls back to local BPF-based
// method if the API is unavailable. The subsysName parameter specifies the cgroup subsystem
// (e.g., "memory", "cpu").
func resolveContainerCgroupCss(ctx context.Context, cfg *Config, subsysName string) (uint64, error) {
	if cfg.ContainerID == "" {
		return 0, nil
	}

	// Try API method first
	cssAddr, err := resolveContainerCgroupCssByAPI(
		ctx,
		cfg.ServerAddress,
		cfg.ContainerID,
		subsysName,
	)
	if err == nil {
		return cssAddr, nil
	}

	log.Warn("API method failed, falling back to local method", "error", err, "container_id", cfg.ContainerID, "subsystem", subsysName)

	// Fallback to local BPF-based method
	cssAddr, err = resolveContainerCgroupCssByLocal(cfg.ContainerID, subsysName)
	if err != nil {
		return 0, fmt.Errorf("both API and local methods failed for subsystem %s: %w", subsysName, err)
	}

	return cssAddr, nil
}

// resolveContainerCgroupCssByAPI attempts to get CSS address via huatuo-bamai API.
func resolveContainerCgroupCssByAPI(
	ctx context.Context,
	serverAddr string,
	containerID string,
	subsysName string,
) (uint64, error) {
	nodeClient, err := client.NewNode(&client.NodeConfig{})
	if err != nil {
		return 0, fmt.Errorf("initialize node client: %w", err)
	}
	container, err := nodeClient.FetchContainer(
		ctx,
		client.NodeAddress{HostPort: serverAddr},
		containerID,
	)
	if err != nil {
		return 0, fmt.Errorf("fetch container metadata: %w", err)
	}

	cssAddr, ok := kernaddr.Parse(container.CgroupCSS[subsysName])
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
