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

package main

import (
	"fmt"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/pod"
)

func (d *Daemon) setupPodManager() error {
	mgrInitCtx := pod.ManagerInitCtx{
		PodReadOnlyPort:      config.Get().Pod.KubeletReadOnlyPort,
		PodAuthorizedPort:    config.Get().Pod.KubeletAuthorizedPort,
		PodClientCertPath:    config.Get().Pod.KubeletClientCertPath,
		PodContainerDisabled: d.opts.DisableKubelet,
		DockerAPIVersion:     config.Get().Pod.DockerAPIVersion,
	}

	if err := pod.ManagerInit(&mgrInitCtx); err != nil {
		return fmt.Errorf("init podlist and sync module: %w", err)
	}
	d.podReady = true

	return nil
}
