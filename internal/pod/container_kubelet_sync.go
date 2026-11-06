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
	"os"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

var (
	kubeletPodCgroupDriver   = "cgroupfs"
	kubeletRuntimeEndpoint   = "unix:///run/containerd/containerd.sock"
	kubeletDefaultConfigPath = []string{
		"/var/lib/kubelet/config.yaml",
		"/var/lib/kubelet/ack-managed-config.yaml",
		"/etc/kubernetes/kubelet/config.json",
		"/host/etc/kubernetes/kubelet/config.json",
	}
)

type ManagerCtx struct {
	PodReadOnlyPort   uint32
	PodAuthorizedPort uint32
	PodClientCertPath string
	DockerAPIVersion  string
}

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

func kubeletConfigFileDefault() (containerdiscovery.KubeletConfiguration, error) {
	empty := containerdiscovery.KubeletConfiguration{}

	for _, name := range kubeletDefaultConfigPath {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}

		config := containerdiscovery.KubeletConfiguration{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			continue
		}

		return config, nil
	}

	return empty, fmt.Errorf("not found kubelet config")
}

// kubeletConfigCacheMustUpdate updates the kubelet configuration cache.
//
// This function MUST succeed: if the kubelet configz endpoint and all
// default config file paths are unavailable, it panics because downstream
// services that depend on kubelet pod information would be broken.
//
// Updated cache vars:
//   - CgroupDriver
//   - ContainerRuntimeEndpoint
func kubeletConfigCacheMustUpdate(requestCtx context.Context, client *containerdiscovery.Kubelet) error {
	var (
		config containerdiscovery.KubeletConfiguration
		err    error
	)

	defer func() {
		if config.CgroupDriver != "" {
			kubeletPodCgroupDriver = config.CgroupDriver
		}
		if config.ContainerRuntimeEndpoint != "" {
			kubeletRuntimeEndpoint = config.ContainerRuntimeEndpoint
		}

		log.Debugf("kubelet config cache updated, cgroup driver: %s, runtime: %s",
			kubeletPodCgroupDriver, kubeletRuntimeEndpoint)
	}()

	config, err = client.Configuration(requestCtx)
	if err == nil {
		return nil
	}

	if err := requestCtx.Err(); err != nil {
		return err
	}

	log.Debugf("kubelet config port is not available, try to read config files: %v", kubeletDefaultConfigPath)

	config, err = kubeletConfigFileDefault()
	if err != nil {
		panic(fmt.Sprintf(
			"we cannot find any cgroup driver of kubelet after requesting configz and default files: %v",
			err,
		))
	}

	return nil
}
