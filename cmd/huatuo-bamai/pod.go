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
	"context"
	"fmt"
	"path/filepath"
	"time"

	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/runmode"
)

func setupPodManager(d *Daemon) (func(context.Context) error, error) {
	var source pod.Source
	var options pod.ManagerOptions
	var err error
	if d.opts.Mode == runmode.Sandbox {
		source, err = pod.NewGuestSource(pod.GuestScannerOptions{
			ProcRoot:     procfs.DefaultPath(),
			CgroupRoot:   filepath.Join(procfs.DefaultPathByType("sys"), "fs", "cgroup"),
			PodName:      d.opts.GuestIdentity.PodName,
			PodNamespace: d.opts.GuestIdentity.PodNamespace,
		})
		options = pod.ManagerOptions{ProcRoot: procfs.DefaultPath(), SyncInterval: 5 * time.Second}
	} else {
		if d.opts.DisableKubelet {
			log.Infof("kubelet pod sync disabled by --disable-kubelet")
			return nil, nil
		}
		source, err = containerdiscovery.NewSource(&containerdiscovery.Options{
			PodReadOnlyPort:   config.Get().Pod.KubeletReadOnlyPort,
			PodAuthorizedPort: config.Get().Pod.KubeletAuthorizedPort,
			PodClientCertPath: config.Get().Pod.KubeletClientCertPath,
			DockerAPIVersion:  config.Get().Pod.DockerAPIVersion,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("prepare workload discovery: %w", err)
	}
	if source == nil {
		return nil, nil
	}
	if err := pod.StartManager(source, options); err != nil {
		return nil, fmt.Errorf("start workload manager: %w", err)
	}
	return func(context.Context) error {
		pod.ReleaseManager()
		return nil
	}, nil
}
