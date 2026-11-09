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
	"errors"
	"fmt"

	podmodel "github.com/ccfos/huatuo/internal/pod"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"

	corev1 "k8s.io/api/core/v1"
)

// It owns no cache or scheduler; the Manager commits each complete discovery.
type containerSource struct {
	dockerVersion string
	client        *Kubelet
	ready         bool
	fetch         func(context.Context) (corev1.PodList, error)
	resolve       func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*podmodel.Pod, error)
}

type Options struct {
	PodReadOnlyPort   uint32
	PodAuthorizedPort uint32
	PodClientCertPath string
	DockerAPIVersion  string
}

// NewSource prepares discovery without I/O or starting a Manager.
// Zero kubelet ports retain the existing disabled discovery configuration.
func NewSource(config *Options) (podmodel.Source, error) {
	if config == nil {
		return nil, errors.New("container discovery configuration is required")
	}
	if config.PodReadOnlyPort == 0 && config.PodAuthorizedPort == 0 {
		return nil, nil
	}
	client, err := NewKubelet(KubeletOptions{
		ReadOnlyPort: config.PodReadOnlyPort, AuthorizedPort: config.PodAuthorizedPort,
		ClientCertPath: config.PodClientCertPath,
	})
	if err != nil {
		return nil, err
	}
	return &containerSource{
		client: client, fetch: client.Pods, resolve: resolveContainerPod,
		dockerVersion: config.DockerAPIVersion,
	}, nil
}

func (*containerSource) Type() string { return podmodel.TypeContainer }

func (s *containerSource) Discover(ctx context.Context) (podmodel.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return podmodel.Discovery{}, err
	}
	if s.client != nil && !s.ready {
		if err := s.client.Initialize(ctx); err != nil {
			return podmodel.Discovery{}, err
		}
		if err := kubeletConfigCacheMustUpdate(ctx, s.client); err != nil {
			return podmodel.Discovery{}, err
		}
		dockerAPIVersion = s.dockerVersion
		s.ready = true
	}
	list, err := s.fetch(ctx)
	if err != nil {
		return podmodel.Discovery{}, err
	}
	discovery := podmodel.Discovery{Pods: make(map[string]*podmodel.Pod), Complete: true}
	var resultErr error
	for i := range list.Items {
		p := &list.Items[i]
		// Completed init and ephemeral debug containers are outside the view.
		// Restartable init sidecars are included before the podmodel.Pod is Running.
		for _, group := range []struct {
			specs    []corev1.Container
			statuses []corev1.ContainerStatus
			init     bool
		}{
			{p.Spec.Containers, p.Status.ContainerStatuses, false},
			{p.Spec.InitContainers, p.Status.InitContainerStatuses, true},
		} {
			for j := range group.statuses {
				if err := ctx.Err(); err != nil {
					return podmodel.Discovery{}, err
				}
				status := &group.statuses[j]
				if status.State.Running == nil {
					continue
				}
				var spec *corev1.Container
				for k := range group.specs {
					if group.specs[k].Name == status.Name {
						spec = &group.specs[k]
						break
					}
				}
				if spec == nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("running container %q has no pod spec", status.Name))
					discovery.Complete = false
					continue
				}
				if group.init && (spec.RestartPolicy == nil || *spec.RestartPolicy != corev1.ContainerRestartPolicyAlways) {
					continue
				}
				id, err := parseContainerIDInPodStatus(ctx, status.ContainerID)
				if err != nil {
					resultErr = errors.Join(resultErr, err)
					discovery.Complete = false
					continue
				}
				if _, exists := discovery.Pods[id]; exists {
					discovery.Pods[id] = nil
					discovery.Complete = false
					resultErr = errors.Join(resultErr, fmt.Errorf("duplicate running container ID %q in kubelet list", id))
					continue
				}
				workload, err := s.resolve(ctx, id, spec, status, p)
				// Nil retains a known member without advancing successful sync time.
				discovery.Pods[id] = workload
				if err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("resolve container %q: %w", id, err))
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return podmodel.Discovery{}, err
	}
	return discovery, resultErr
}

func resolveContainerPod(ctx context.Context, id string, spec *corev1.Container, status *corev1.ContainerStatus, p *corev1.Pod) (*podmodel.Pod, error) {
	container, err := kubeletContainer(ctx, id, spec, status, p)
	if err != nil {
		return nil, err
	}
	workload := &podmodel.Pod{
		ID: container.ID, Type: podmodel.TypeContainer, Name: container.Name,
		StartedAt: container.StartedAt, Labels: container.Labels,
		Process: &podmodel.HostProcess{PID: container.InitPid},
		Container: &podmodel.ContainerInfo{
			Hostname: container.Hostname, Type: container.Type, Qos: container.Qos,
			RuntimeType: container.RuntimeType, SandboxID: container.SandboxID,
		},
	}
	workload.Container.CgroupPath = container.CgroupPath
	workload.Cgroups = podmodel.CgroupBinding{}
	workload.Network = &podmodel.HostNetwork{IPAddress: container.IPAddress}
	if container.HostKataSandboxID() == "" {
		workload.Network.PID = container.InitPid
	}
	// Preserve the existing check that PID and cgroup reads describe one process.
	process, err := procfs.NewProc(container.InitPid)
	if err != nil {
		return workload, err
	}
	before, err := process.Stat()
	if err != nil {
		return workload, err
	}
	paths, err := cgroups.PathsForPID(container.InitPid)
	if err != nil {
		return workload, err
	}
	after, err := process.Stat()
	if err != nil {
		return nil, err
	}
	if before.Starttime != after.Starttime {
		return nil, fmt.Errorf("container %q process changed during discovery", id)
	}
	workload.Cgroups = podmodel.CgroupBinding{Unified: paths.Unified, Controllers: paths.Controllers}
	return workload, nil
}
