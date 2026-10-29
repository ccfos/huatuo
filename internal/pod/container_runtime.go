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
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/utils/netutil"
)

// populateContainerRuntime selects the runtime before reading any PID file or
// deriving cgroup paths. The caller publishes the container only on success.
func populateContainerRuntime(container *Container, pod *corev1.Pod) error {
	provider, _, _ := containerRuntimeState()
	if provider == containerProviderContainerd {
		info, err := readContainerdRuntimeInfo(container.ID)
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
	container.NetNamespaceInum, err = netutil.NetNamespaceInumByPID(initPID)
	if err != nil {
		return fmt.Errorf("failed to get net namespace inode by pid: %w", err)
	}
	container.NetNamespaceCookie, err = netutil.NetNamespaceCookieByPID(initPID)
	if err != nil {
		log.Debugf("failed to get net namespace cookie for pid %d: %v", initPID, err)
	}
	container.CgroupCss, err = parseContainerCSS(container.ID)
	if err != nil {
		return fmt.Errorf("failed to parse container css: %w", err)
	}
	container.CgroupPath, err = containerCgroupSuffix(container.ID, pod)
	if err != nil {
		return fmt.Errorf("failed to get cgroup path: %w", err)
	}
	return nil
}
