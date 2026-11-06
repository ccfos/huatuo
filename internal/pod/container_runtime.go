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

	corev1 "k8s.io/api/core/v1"
)

// populateContainerRuntime selects the runtime before reading any PID file or
// deriving cgroup paths. The caller publishes the container only on success.
func populateContainerRuntime(ctx context.Context, container *Container, pod *corev1.Pod) error {
	provider, _, _ := containerRuntimeState()
	if provider == containerProviderContainerd {
		info, err := readContainerdRuntimeInfo(ctx, container.ID)
		if err != nil {
			return err
		}
		container.RuntimeType = info.RuntimeType
		if isKataRuntime(info.RuntimeType) {
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
