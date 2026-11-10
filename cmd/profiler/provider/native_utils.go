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
	"net"
	"strings"

	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
)

func newNativeBPFConstants(pid int, target pod.CgroupCSSRef, threadGroup bool) map[string]any {
	return map[string]any{
		"profiler_filter_css_addr":  target.CSSAddr,
		"profiler_filter_kernfs_id": target.KernfsID,
		"profiler_filter_pid":       uint32(pid),
		"profiler_filter_threads":   threadGroup,
	}
}

func resolveContainerCgroupTarget(ctx context.Context, cfg *Config, subsys string) (pod.CgroupCSSRef, error) {
	if cfg.ContainerID == "" {
		return pod.CgroupCSSRef{}, nil
	}
	return resolveContainerCgroupRef(ctx, cfg.ServerAddress, cfg.ContainerID, subsys)
}

func taskCommString(comm [bpf.TaskCommLen]byte) string {
	length := bytes.IndexByte(comm[:], 0)
	if length == -1 {
		length = len(comm)
	}
	return string(comm[:length])
}

func resolveContainerCgroupRef(ctx context.Context, serverAddr, containerID, subsysName string) (pod.CgroupCSSRef, error) {
	nodeClient, err := client.NewNode(&client.NodeConfig{})
	if err != nil {
		return pod.CgroupCSSRef{}, fmt.Errorf("initialize node client: %w", err)
	}
	container, err := nodeClient.FetchContainer(
		ctx,
		client.NodeAddress{HostPort: serverAddr},
		containerID,
	)
	if err != nil {
		if !localNodeAddress(serverAddr) {
			return pod.CgroupCSSRef{}, fmt.Errorf("fetch container %q metadata: %w", containerID, err)
		}
		log.Warn("local node API unavailable; resolving complete CSS identity locally", "error", err)
		return pod.ContainerCSSBySubsys(containerID, subsysName)
	}
	if container.CgroupCSSInstances != nil {
		if instance, found := (*container.CgroupCSSInstances)[subsysName]; found {
			cssAddr, cssOK := kernaddr.Parse(instance.CSSAddr)
			kernfsID, idOK := kernaddr.Parse(instance.KernfsID)
			if !cssOK || !idOK || cssAddr == 0 || kernfsID == 0 {
				return pod.CgroupCSSRef{}, fmt.Errorf("container %q has invalid %s CSS identity in node API", containerID, subsysName)
			}
			if legacyAddr, ok := kernaddr.Parse(container.CgroupCSS[subsysName]); ok && legacyAddr != cssAddr {
				return pod.CgroupCSSRef{}, fmt.Errorf("container %q has conflicting %s CSS addresses in node API", containerID, subsysName)
			}
			return pod.CgroupCSSRef{CSSAddr: cssAddr, KernfsID: kernfsID}, nil
		}
	}
	if !localNodeAddress(serverAddr) {
		return pod.CgroupCSSRef{}, fmt.Errorf("container %q has no %s kernfs ID in remote node API", containerID, subsysName)
	}
	local, err := pod.ContainerCSSBySubsys(containerID, subsysName)
	if err != nil {
		return pod.CgroupCSSRef{}, fmt.Errorf("resolve container %q %s CSS identity locally: %w", containerID, subsysName, err)
	}
	legacyAddr, ok := kernaddr.Parse(container.CgroupCSS[subsysName])
	if !ok || legacyAddr != local.CSSAddr {
		return pod.CgroupCSSRef{}, fmt.Errorf("container %q %s CSS address differs between local cache and node API", containerID, subsysName)
	}
	return local, nil
}

func localNodeAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
