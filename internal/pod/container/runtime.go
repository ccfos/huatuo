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

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockerclient "github.com/docker/docker/client"
	criapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	k8sremote "k8s.io/cri-client/pkg"

	"github.com/ccfos/huatuo/internal/pidfile"
)

func DockerRoot(ctx context.Context, apiVersion string) (string, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithVersion(apiVersion))
	if err != nil {
		return "", fmt.Errorf("create docker client: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	info, err := client.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("get docker info: %w", err)
	}

	return info.DockerRootDir, nil
}

func ConnectContainerd(ctx context.Context, endpoint string) (criapi.RuntimeService, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	// The CRI constructor has no context parameter and retains its own
	// five-second bound. Subsequent RPCs use the discovery context.
	client, err := k8sremote.NewRemoteRuntimeService(endpoint, 5*time.Second, nil, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create containerd client: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	status, err := client.Status(ctx, true)
	if err != nil {
		return nil, "", fmt.Errorf("get containerd status: %w", err)
	}

	config := struct {
		StateDir string `json:"stateDir"`
	}{}
	if err := json.Unmarshal([]byte(status.Info["config"]), &config); err != nil {
		return nil, "", fmt.Errorf("unmarshal containerd config: %w", err)
	}

	return client, path.Dir(config.StateDir), nil
}

// RuntimeInfo is containerd's verbose CRI status. For Kata, PID is
// the Host hypervisor PID, not the container's init PID inside the Guest.
type RuntimeInfo struct {
	PID         int    `json:"pid"`
	RuntimeType string `json:"runtimeType"`
	SandboxID   string `json:"sandboxID"`
}

func ReadContainerdRuntimeInfo(ctx context.Context, client criapi.RuntimeService, containerID string) (*RuntimeInfo, error) {
	if client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := client.ContainerStatus(ctx, containerID, true)
	if err != nil {
		return nil, fmt.Errorf("get containerd container status: %w", err)
	}
	status := response.GetStatus()
	if status.GetId() != containerID || status.GetState() != runtimeapi.ContainerState_CONTAINER_RUNNING {
		return nil, fmt.Errorf("container %s has no matching running CRI status", containerID)
	}
	info := &RuntimeInfo{}
	if err := json.Unmarshal([]byte(response.GetInfo()["info"]), info); err != nil {
		return nil, fmt.Errorf("decode containerd container info: %w", err)
	}
	if info.RuntimeType == "" {
		return nil, fmt.Errorf("container %s has no CRI runtime type", containerID)
	}
	return info, nil
}

func ReadDockerPID(rootDir, containerID string) (int, error) {
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

func ReadContainerdPID(stateDir, containerID string) (int, error) {
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
