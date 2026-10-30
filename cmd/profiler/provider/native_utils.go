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
	"encoding/binary"
	"fmt"

	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"

	"golang.org/x/sys/unix"
)

type nativeCgroupTarget struct {
	CSS uint64
	ID  uint64
}

func newNativeBPFConstants(pid int, target nativeCgroupTarget, threadGroup bool) map[string]any {
	return map[string]any{
		"profiler_filter_css":       target.CSS,
		"profiler_filter_cgroup_id": target.ID,
		"profiler_filter_pid":       uint32(pid),
		"profiler_filter_threads":   threadGroup,
	}
}

func resolveContainerCgroupTarget(ctx context.Context, cfg *Config, subsys string) (nativeCgroupTarget, error) {
	if cfg.ContainerID == "" {
		return nativeCgroupTarget{}, nil
	}
	path, err := pod.ContainerCgroupPathBySubsys(cfg.ContainerID, subsys)
	if err != nil {
		return nativeCgroupTarget{}, fmt.Errorf("resolve target %s cgroup: %w", subsys, err)
	}
	id, err := nativeCgroupID(path)
	if err != nil {
		return nativeCgroupTarget{}, err
	}
	css, err := resolveContainerCgroupCss(ctx, cfg, subsys)
	if err != nil {
		return nativeCgroupTarget{}, err
	}
	// CSS discovery may take time; never bind a replacement at the same path.
	currentID, err := nativeCgroupID(path)
	if err != nil {
		return nativeCgroupTarget{}, err
	}
	if currentID != id {
		return nativeCgroupTarget{}, fmt.Errorf("target cgroup %q changed during profiler startup", path)
	}
	if css == 0 {
		return nativeCgroupTarget{}, fmt.Errorf("container %q has no %s CSS address", cfg.ContainerID, subsys)
	}
	return nativeCgroupTarget{CSS: css, ID: id}, nil
}

func nativeCgroupID(path string) (uint64, error) {
	handle, _, err := unix.NameToHandleAt(unix.AT_FDCWD, path, unix.AT_SYMLINK_FOLLOW)
	if err != nil {
		return 0, fmt.Errorf("read target cgroup identity %q: %w", path, err)
	}
	if handle.Size() != 8 {
		return 0, fmt.Errorf("target cgroup %q has unsupported file handle size %d", path, handle.Size())
	}
	id := binary.NativeEndian.Uint64(handle.Bytes())
	if id == 0 {
		return 0, fmt.Errorf("target cgroup %q has zero instance ID", path)
	}
	return id, nil
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
