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

import containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

// InitManager starts one shared producer before any initial container lookup.
// Transient startup failures are visible to subscribers and retried by the
// Manager; getters never perform network I/O or start background work.
func InitManager(config *ManagerCtx) error {
	workloadManagerMu.Lock()
	defer workloadManagerMu.Unlock()
	if activeManager != nil {
		return nil
	}
	if config.PodReadOnlyPort == 0 && config.PodAuthorizedPort == 0 {
		return nil
	}
	client, err := containerdiscovery.NewKubelet(containerdiscovery.KubeletOptions{
		ReadOnlyPort: config.PodReadOnlyPort, AuthorizedPort: config.PodAuthorizedPort,
		ClientCertPath: config.PodClientCertPath,
	})
	if err != nil {
		return err
	}

	dockerAPIVersion = config.DockerAPIVersion
	source := &containerSource{client: client, fetch: client.Pods, resolve: resolveContainerPod}
	manager, err := newSourceManager(containerView, source, ManagerOptions{})
	if err != nil {
		return err
	}
	manager.initialize = initializeWorkloadResources
	activeManager = manager
	manager.start()
	return nil
}
