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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dockertypes "github.com/docker/docker/api/types"

	"huatuo-bamai/internal/pidfile"
)

const (
	defaultDockerRootDir      = "/var/lib/docker"
	defaultContainerdStateDir = "/run/containerd"
)

func containerInitPid(containerID string) (int, error) {
	provider, dockerRoot, containerdState := containerRuntimeState()
	switch provider {
	case containerProviderDocker:
		return containerInitPIDInDockerRoot(dockerRoot, containerID)
	case containerProviderContainerd:
		return containerInitPIDInContainerdState(containerdState, containerID)
	default:
		return -1, fmt.Errorf("container provider not initialized")
	}
}

func containerInitPIDInDockerRoot(rootDir, containerID string) (int, error) {
	containersDir := filepath.Join(rootDir, "containers")
	resolvedID, err := resolveContainerID(containersDir, containerID)
	if err != nil {
		return -1, fmt.Errorf("resolve Docker container %q: %w", containerID, err)
	}

	configPath := filepath.Join(containersDir, resolvedID, "config.v2.json")
	content, err := os.ReadFile(configPath)
	if err != nil {
		return -1, fmt.Errorf("read Docker container config %q: %w", configPath, err)
	}

	container := dockertypes.ContainerJSON{}
	if err := json.Unmarshal(content, &container); err != nil {
		return -1, fmt.Errorf("parse Docker container config %q: %w", configPath, err)
	}

	if container.ContainerJSONBase == nil || container.State == nil || container.State.Pid <= 0 {
		return -1, fmt.Errorf("Docker container %q has no running init PID", resolvedID)
	}
	return container.State.Pid, nil
}

func containerInitPIDInContainerdState(stateDir, containerID string) (int, error) {
	// pid: $state/io.containerd.runtime.v2.task/k8s.io/$container/init.pid
	// runtime runc v2?
	// kata ?
	tasksDir := filepath.Join(stateDir, "io.containerd.runtime.v2.task", "k8s.io")
	resolvedID, err := resolveContainerID(tasksDir, containerID)
	if err != nil {
		return -1, fmt.Errorf("resolve containerd container %q: %w", containerID, err)
	}

	filePath := filepath.Join(tasksDir, resolvedID, "init.pid")
	pid, err := pidfile.Read(filePath)
	if err != nil {
		return -1, fmt.Errorf("read containerd init PID %q: %w", filePath, err)
	}
	if pid <= 0 {
		return -1, fmt.Errorf("containerd container %q has no running init PID", resolvedID)
	}
	return pid, nil
}

func containerInitPIDByID(containerID string) (int, error) {
	if err := ValidateContainerID(containerID); err != nil {
		return -1, err
	}

	provider, dockerRoot, containerdState := containerRuntimeState()
	switch provider {
	case containerProviderDocker:
		return containerInitPIDInDockerRoot(dockerRoot, containerID)
	case containerProviderContainerd:
		return containerInitPIDInContainerdState(containerdState, containerID)
	}

	dockerPID, dockerErr := containerInitPIDInDockerRoot(defaultDockerRootDir, containerID)
	if dockerErr == nil {
		return dockerPID, nil
	}
	containerdPID, containerdErr := containerInitPIDInContainerdState(defaultContainerdStateDir, containerID)
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

func resolveContainerID(parentDir, containerID string) (string, error) {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return "", err
	}

	var match string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), containerID) {
			continue
		}
		if entry.Name() == containerID {
			return containerID, nil
		}
		if match != "" {
			return "", fmt.Errorf("container ID prefix is ambiguous: matches %q and %q", match, entry.Name())
		}
		match = entry.Name()
	}
	if match == "" {
		return "", fmt.Errorf("container not found under %q", parentDir)
	}
	return match, nil
}
