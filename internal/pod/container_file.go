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

package pod

import (
	"context"
	"errors"
	"fmt"

	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"
)

const (
	defaultDockerRootDir      = "/var/lib/docker"
	defaultContainerdStateDir = "/run/containerd"
)

func containerInitPid(containerID string) (int, error) {
	provider, dockerRoot, containerdState := containerRuntimeState()
	switch provider {
	case containerProviderDocker:
		return containerdiscovery.ReadDockerPID(dockerRoot, containerID)
	case containerProviderContainerd:
		return containerdiscovery.ReadContainerdPID(containerdState, containerID)
	default:
		return -1, fmt.Errorf("container provider not initialized")
	}
}

func containerInitPIDByID(containerID string) (int, error) {
	if err := ValidateContainerID(containerID); err != nil {
		return -1, err
	}

	if container, _ := ContainerByID(containerID); container != nil && container.HostKataSandboxID() != "" {
		info, err := readContainerdRuntimeInfo(context.Background(), containerID)
		if err != nil {
			return -1, err
		}
		if info.RuntimeType != container.RuntimeType || info.SandboxID != container.SandboxID || info.PID <= 0 {
			return -1, fmt.Errorf("container %q Kata runtime binding changed", containerID)
		}
		return info.PID, nil
	}
	provider, dockerRoot, containerdState := containerRuntimeState()
	switch provider {
	case containerProviderDocker:
		return containerdiscovery.ReadDockerPID(dockerRoot, containerID)
	case containerProviderContainerd:
		return containerdiscovery.ReadContainerdPID(containerdState, containerID)
	}

	dockerPID, dockerErr := containerdiscovery.ReadDockerPID(defaultDockerRootDir, containerID)
	if dockerErr == nil {
		return dockerPID, nil
	}
	containerdPID, containerdErr := containerdiscovery.ReadContainerdPID(defaultContainerdStateDir, containerID)
	if containerdErr == nil {
		return containerdPID, nil
	}

	return -1, fmt.Errorf(
		"resolve container %q init PID from local runtime state: %w",
		containerID,
		errors.Join(dockerErr, containerdErr),
	)
}

func containerRuntimeState() (containerProvider, string, string) {
	initMu.Lock()
	defer initMu.Unlock()
	return currContainerProvider, dockerRootDir, containerdStateDir
}
