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
	"fmt"
	"sync"

	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

	criapi "k8s.io/cri-api/pkg/apis"
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
	containerdClient   criapi.RuntimeService
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
func initContainerProviderEnv(ctx context.Context, provider containerProvider, apiVersion string) error {
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
		return initDockerProviderEnv(ctx, apiVersion)
	case containerProviderContainerd:
		return initContainerdProviderEnv(ctx)
	default:
		return fmt.Errorf("invalid container provider: %q", provider)
	}
}

func initDockerProviderEnv(ctx context.Context, apiVersion string) error {
	root, err := containerdiscovery.DockerRoot(ctx, apiVersion)
	if err != nil {
		return err
	}
	dockerRootDir = root
	currContainerProvider = containerProviderDocker
	return nil
}

func initContainerdProviderEnv(ctx context.Context) error {
	client, state, err := containerdiscovery.ConnectContainerd(ctx, kubeletRuntimeEndpoint)
	if err != nil {
		return err
	}
	containerdClient, containerdStateDir = client, state
	currContainerProvider = containerProviderContainerd
	return nil
}

type containerdRuntimeInfo = containerdiscovery.RuntimeInfo

func readContainerdRuntimeInfo(ctx context.Context, containerID string) (*containerdRuntimeInfo, error) {
	initMu.Lock()
	client := containerdClient
	initMu.Unlock()
	return containerdiscovery.ReadContainerdRuntimeInfo(ctx, client, containerID)
}
