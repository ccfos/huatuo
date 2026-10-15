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

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/runmode"
)

func setupPodManager(d *Daemon) (func(context.Context) error, error) {
	if d.opts.Mode == runmode.Sandbox {
		if err := pod.InitGuestManager(pod.GuestScannerOptions{ProcRoot: procfs.DefaultPath(), CgroupRoot: filepath.Join(procfs.DefaultPathByType("sys"), "fs", "cgroup"), PodName: d.opts.GuestIdentity.PodName, PodNamespace: d.opts.GuestIdentity.PodNamespace}); err != nil {
			return nil, err
		}
		return func(context.Context) error { pod.ReleaseManager(); return nil }, nil
	}
	if d.opts.DisableKubelet {
		log.Infof("kubelet pod sync disabled by --disable-kubelet")
		return nil, nil
	}

	mgrCtx := pod.ManagerCtx{
		PodReadOnlyPort:   config.Get().Pod.KubeletReadOnlyPort,
		PodAuthorizedPort: config.Get().Pod.KubeletAuthorizedPort,
		PodClientCertPath: config.Get().Pod.KubeletClientCertPath,
		DockerAPIVersion:  config.Get().Pod.DockerAPIVersion,
	}

	if err := pod.InitManager(&mgrCtx); err != nil {
		return nil, fmt.Errorf("init podlist and sync module: %w", err)
	}

	return func(context.Context) error {
		pod.ReleaseManager()
		return nil
	}, nil
}
