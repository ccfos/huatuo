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
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

	corev1 "k8s.io/api/core/v1"
)

var (
	kubeletPodCgroupDriver = "cgroupfs"
	kubeletRuntimeEndpoint = "unix:///run/containerd/containerd.sock"
)

func kubeletContainer(ctx context.Context, containerID string, container *corev1.Container, containerStatus *corev1.ContainerStatus, pod *corev1.Pod) (*Container, error) {
	// container type
	containerType, err := parseContainerType(container, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type: %w", err)
	}

	// container qos
	containerQos, err := parseContainerQos(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse qos: %w", err)
	}

	hostname, err := parseContainerHostname(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hostname: %w", err)
	}

	labels, err := parseContainerLabels(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container labels: %w", err)
	}

	startedAt, err := time.Parse(time.RFC3339, containerStatus.State.Running.StartedAt.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to parse StartedAt %s: %w", containerStatus.State.Running.StartedAt, err)
	}

	result := &Container{
		ID:        containerID,
		Name:      container.Name,
		Hostname:  hostname,
		Type:      containerType,
		Qos:       containerQos,
		IPAddress: parseContainerIPAddress(pod),
		StartedAt: startedAt,
		SyncedAt:  time.Now(),
		Labels:    labels,
	}

	if err := populateContainerRuntime(ctx, result, pod); err != nil {
		return nil, err
	}

	// Guest agents share the VM cgroup but are not business sampling targets.
	if result.HostKataSandboxID() != "" && hasGuestAgentIdentity(container, pod) {
		result.Type = ContainerTypeSidecar
	}

	return result, nil
}

func parseContainerIDInPodStatus(ctx context.Context, data string) (string, error) {
	// containerID example:
	//
	// "containerID": "docker://06ae8891e7e9b80f353e07116980f93a357fb3f239c09894de73b2e74121c94f",
	// "containerID": "containerd://0ac95a0f051b5094551a02b584414773dc24f5b2f1e4ea768460a787f762e279"
	parts := strings.Split(strings.Trim(data, "\""), "://")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid container id: %s", data)
	}

	provider, err := containerProviderFrom(parts[0])
	if err != nil {
		return "", err
	}

	if len(parts[1]) != 64 {
		return "", fmt.Errorf("container id must contain 64 hexadecimal characters: %q", parts[1])
	}
	if err := ValidateContainerID(parts[1]); err != nil {
		return "", err
	}
	if err := initContainerProviderEnv(ctx, provider, dockerAPIVersion); err != nil {
		return "", fmt.Errorf("init container provider for containerID %q: %w", data, err)
	}

	return parts[1], nil
}

func parseContainerIPAddress(pod *corev1.Pod) string {
	return pod.Status.PodIP
}

// kubeletConfigCacheMustUpdate retains the legacy runtime settings during migration.
func kubeletConfigCacheMustUpdate(ctx context.Context, client *containerdiscovery.Kubelet) error {
	config, err := client.MustConfiguration(ctx)
	if err != nil {
		return err
	}
	if config.CgroupDriver != "" {
		kubeletPodCgroupDriver = config.CgroupDriver
	}
	if config.ContainerRuntimeEndpoint != "" {
		kubeletRuntimeEndpoint = config.ContainerRuntimeEndpoint
	}
	log.Debugf("kubelet config cache updated, cgroup driver: %s, runtime: %s",
		kubeletPodCgroupDriver, kubeletRuntimeEndpoint)
	return nil
}
