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

package pod

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sync"
	"time"

	dockerclient "github.com/docker/docker/client"
	k8sremote "k8s.io/cri-client/pkg"
)

// containerProvider identifies the container runtime in use on the node.
type containerProvider string

const (
	containerProviderNoop       containerProvider = ""
	containerProviderDocker     containerProvider = "docker"
	containerProviderContainerd containerProvider = "containerd"
)

var (
	initMu                sync.Mutex
	currContainerProvider containerProvider

	dockerRootDir      string
	containerdStateDir string
	dockerAPIVersion   string
)

// containerProviderFrom parses the runtime prefix from a container ID (e.g. "docker", "containerd").
func containerProviderFrom(s string) (containerProvider, error) {
	p := containerProvider(s)
	switch p {
	case containerProviderDocker, containerProviderContainerd:
		return p, nil
	default:
		return containerProviderNoop, fmt.Errorf("invalid container provider: %s", s)
	}
}

// initContainerProviderEnv initializes the runtime environment on first call.
// On failure the provider remains unset so a subsequent call with a valid prefix can succeed.
// After successful init, mismatched prefixes are rejected to tolerate stale PodStatus entries.
func initContainerProviderEnv(provider containerProvider, apiVersion string) error {
	initMu.Lock()
	defer initMu.Unlock()

	if currContainerProvider != containerProviderNoop {
		if provider == currContainerProvider {
			return nil
		}
		return fmt.Errorf("container provider mismatch: initialized as %q, rejecting stale %q prefix", currContainerProvider, provider)
	}

	switch provider {
	case containerProviderDocker:
		return initDockerProviderEnv(apiVersion)
	case containerProviderContainerd:
		return initContainerdProviderEnv()
	default:
		return fmt.Errorf("invalid container provider: %q", provider)
	}
}

func initDockerProviderEnv(apiVersion string) error {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithVersion(apiVersion))
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("get docker info: %w", err)
	}

	dockerRootDir = info.DockerRootDir
	currContainerProvider = containerProviderDocker
	return nil
}

func initContainerdProviderEnv() error {
	client, err := k8sremote.NewRemoteRuntimeService(kubeletRuntimeSnapshot().runtimeEndpoint, 5*time.Second, nil, nil)
	if err != nil {
		return fmt.Errorf("create containerd client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.Status(ctx, true)
	if err != nil {
		return fmt.Errorf("get containerd status: %w", err)
	}

	config := struct {
		StateDir string `json:"stateDir"`
	}{}
	if err := json.Unmarshal([]byte(status.Info["config"]), &config); err != nil {
		return fmt.Errorf("unmarshal containerd config: %w", err)
	}

	containerdStateDir = path.Dir(config.StateDir)
	currContainerProvider = containerProviderContainerd
	return nil
}
