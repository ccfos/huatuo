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

package container

import (
	"context"
	"fmt"
	"strings"
	"time"

	podmodel "github.com/ccfos/huatuo/internal/pod"

	"github.com/ccfos/huatuo/internal/log"

	corev1 "k8s.io/api/core/v1"
)

var (
	kubeletPodCgroupDriver = "cgroupfs"
	kubeletRuntimeEndpoint = "unix:///run/containerd/containerd.sock"
)

func kubeletContainer(ctx context.Context, containerID string, container *corev1.Container, containerStatus *corev1.ContainerStatus, pod *corev1.Pod) (*podmodel.Container, error) {
	// container type
	containerType, err := parseContainerType(container, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type: %w", err)
	}

	containerQos := podmodel.ParseContainerQos(string(pod.Status.QOSClass))

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

	result := &podmodel.Container{
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
		result.Type = podmodel.ContainerTypeSidecar
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
	if err := podmodel.ValidateContainerID(parts[1]); err != nil {
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
func kubeletConfigCacheMustUpdate(ctx context.Context, client *Kubelet) error {
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

// hasGuestAgentIdentity recognizes the existing Guest deployment contract,
// without guessing a role from container or image names.
func hasGuestAgentIdentity(container *corev1.Container, pod *corev1.Pod) bool {
	for _, identity := range []struct{ name, field, value string }{
		{"BAMAI_POD_NAME", "metadata.name", pod.Name},
		{"BAMAI_POD_NAMESPACE", "metadata.namespace", pod.Namespace},
		{"BAMAI_NODE_NAME", "spec.nodeName", pod.Spec.NodeName},
	} {
		var matched *corev1.EnvVar
		for i := range container.Env {
			env := &container.Env[i]
			if env.Name != identity.name {
				continue
			}
			if matched != nil {
				return false
			}
			matched = env
		}
		if matched == nil || identity.value == "" {
			return false
		}
		if matched.ValueFrom == nil {
			if matched.Value != identity.value {
				return false
			}
		} else if matched.Value != "" ||
			matched.ValueFrom.FieldRef == nil ||
			matched.ValueFrom.FieldRef.FieldPath != identity.field ||
			(matched.ValueFrom.FieldRef.APIVersion != "" &&
				matched.ValueFrom.FieldRef.APIVersion != "v1") {
			return false
		}
	}
	return true
}
