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
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/cgroups"
	podmodel "github.com/ccfos/huatuo/internal/pod"

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
	root, err := DockerRoot(ctx, apiVersion)
	if err != nil {
		return err
	}
	dockerRootDir = root
	currContainerProvider = containerProviderDocker
	return nil
}

func initContainerdProviderEnv(ctx context.Context) error {
	client, state, err := ConnectContainerd(ctx, kubeletRuntimeEndpoint)
	if err != nil {
		return err
	}
	containerdClient, containerdStateDir = client, state
	currContainerProvider = containerProviderContainerd
	return nil
}

func readContainerdRuntimeInfo(ctx context.Context, containerID string) (*RuntimeInfo, error) {
	initMu.Lock()
	client := containerdClient
	initMu.Unlock()
	return ReadContainerdRuntimeInfo(ctx, client, containerID)
}

// populateContainerRuntime selects the runtime before reading any PID file or
// deriving cgroup paths. The caller publishes the container only on success.
func populateContainerRuntime(ctx context.Context, container *podmodel.Container, pod *corev1.Pod) error {
	provider, _, _ := containerRuntimeState()
	if provider == containerProviderContainerd {
		info, err := readContainerdRuntimeInfo(ctx, container.ID)
		if err != nil {
			return err
		}
		container.RuntimeType = info.RuntimeType
		if podmodel.IsKataRuntime(info.RuntimeType) {
			return populateKataContainer(container, info)
		}
	}

	initPID, err := containerInitPid(container.ID)
	if err != nil {
		return fmt.Errorf("failed to get InitPid: %w", err)
	}
	container.InitPid = initPID
	container.CgroupPath, err = containerCgroupSuffix(container.ID, pod)
	if err != nil {
		return fmt.Errorf("failed to get cgroup path: %w", err)
	}
	return nil
}

const (
	defaultDockerRootDir      = "/var/lib/docker"
	defaultContainerdStateDir = "/run/containerd"
)

func containerInitPid(containerID string) (int, error) {
	provider, dockerRoot, containerdState := containerRuntimeState()
	switch provider {
	case containerProviderDocker:
		return ReadDockerPID(dockerRoot, containerID)
	case containerProviderContainerd:
		return ReadContainerdPID(containerdState, containerID)
	default:
		return -1, fmt.Errorf("container provider not initialized")
	}
}

func containerInitPIDByID(containerID string) (int, error) {
	if err := podmodel.ValidateContainerID(containerID); err != nil {
		return -1, err
	}

	if container, _ := podmodel.ContainerByID(containerID); container != nil && container.HostKataSandboxID() != "" {
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
		return ReadDockerPID(dockerRoot, containerID)
	case containerProviderContainerd:
		return ReadContainerdPID(containerdState, containerID)
	}

	dockerPID, dockerErr := ReadDockerPID(defaultDockerRootDir, containerID)
	if dockerErr == nil {
		return dockerPID, nil
	}
	containerdPID, containerdErr := ReadContainerdPID(defaultContainerdStateDir, containerID)
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

// CSSBySubsys preserves local profiling without starting workload discovery.
func CSSBySubsys(containerID, subsystem string) (uint64, error) {
	if containerID == "" {
		return 0, nil
	}
	paths, err := containerCgroupPathsByID(containerID)
	if err != nil {
		return 0, err
	}
	membership, err := cgroupMembershipForSubsystem(paths, cgroups.CgroupMode(), subsystem)
	if err != nil {
		return 0, fmt.Errorf("resolve container %q CSS membership: %w", containerID, err)
	}
	return podmodel.CgroupCSSByPath(subsystem, membership)
}

// ValidateContainerRef combines the runtime identity with common kernel checks.
func ValidateContainerRef(ref podmodel.ContainerRef) error {
	current, err := podmodel.ContainerRefByID(ref.Key.ID)
	if err != nil {
		return err
	}
	if current != ref {
		return fmt.Errorf("container %q instance is no longer current", ref.Key.ID)
	}
	workload, err := podmodel.PodByID(ref.Key.ID)
	if err != nil {
		return err
	}
	if workload == nil {
		return fmt.Errorf("container %q is no longer present", ref.Key.ID)
	}
	if workload.Container == nil || !workload.Container.Guest {
		pid, err := containerInitPIDByID(ref.Key.ID)
		if err != nil {
			return err
		}
		if pid != ref.InitPID {
			return fmt.Errorf("container %q init pid changed", ref.Key.ID)
		}
	}
	return podmodel.ValidateContainerResources(ref)
}
