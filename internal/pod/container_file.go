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

package pod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/pidfile"

	dockertypes "github.com/docker/docker/api/types"
	dockerclient "github.com/docker/docker/client"
	k8sremote "k8s.io/cri-client/pkg"
)

const (
	containerEnvNoop = iota
	containerEnvDocker
	containerEnvContainerd
)

var (
	// detect whether the current environment is Docker or containerd.
	currContainerEnv int

	// Docker Root Dir from `docker info`
	dockerRootDir string
	// Containerd State Dir. More information see https://github.com/containerd/containerd/blob/main/docs/cri/config.md
	containerdStateDir           string
	initContainerProviderEnvOnce sync.Once
)

func initContainerProviderEnv(containerEnv string) {
	initContainerProviderEnvOnce.Do(func() {
		var err error
		switch containerEnv {
		case "docker":
			err = initDockerProviderEnv()
		case "containerd":
			err = initContainerdProviderEnv()
		default:
			err = fmt.Errorf("invalid the container provider: %s", containerEnv)
		}

		if err != nil {
			panic(err)
		}
	})
}

func initDockerProviderEnv() error {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithVersion(conf.Get().Pod.DockerAPIVersion))
	if err != nil {
		return fmt.Errorf("create docker client, err: %w", err)
	}
	defer client.Close()

	// timeout: 5s
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("get docker info, err: %w", err)
	}

	dockerRootDir = info.DockerRootDir
	currContainerEnv = containerEnvDocker
	return nil
}

func initContainerdProviderEnv() error {
	// timeout: 5s
	client, err := k8sremote.NewRemoteRuntimeService("unix:///run/containerd/containerd.sock", 5*time.Second, nil, nil)
	if err != nil {
		return fmt.Errorf("create containerd client, err: %w", err)
	}

	// timeout: 5s
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.Status(ctx, true)
	if err != nil {
		return fmt.Errorf("get containerd status, err: %w", err)
	}

	config := struct {
		StateDir string `json:"stateDir"`
	}{}
	if err := json.Unmarshal([]byte(status.Info["config"]), &config); err != nil {
		return fmt.Errorf("unmarshal containerd config, err: %w", err)
	}

	containerdStateDir = path.Dir(config.StateDir)
	currContainerEnv = containerEnvContainerd
	return nil
}

func containerInitPid(containerID string) (int, error) {
	switch currContainerEnv {
	case containerEnvDocker:
		return containerInitPidInDocker(containerID)
	case containerEnvContainerd:
		return containerInitPidInContainerd(containerID)
	default:
		return -1, fmt.Errorf("invalid container env")
	}
}

func containerInitPidInDocker(containerID string) (int, error) {
	configPath := filepath.Join(dockerRootDir, "containers", containerID, "config.v2.json")
	content, err := os.ReadFile(configPath)
	if err != nil {
		return -1, err
	}

	container := dockertypes.ContainerJSON{}
	if err := json.Unmarshal(content, &container); err != nil {
		return -1, err
	}

	if container.State.Pid == 0 {
		return -1, fmt.Errorf("invalid pid for container %s", containerID)
	}
	return container.State.Pid, nil
}

func containerInitPidInContainerd(containerID string) (int, error) {
	// pid: $state/io.containerd.runtime.v2.task/k8s.io/$container/init.pid
	// runtime runc v2?
	// kata ?
	filePath := filepath.Join(containerdStateDir, "io.containerd.runtime.v2.task", "k8s.io", containerID, "init.pid")

	return pidfile.Read(filePath)
}
