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
	"os"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
)

func (d *Daemon) setupCgroup() error {
	if d.opts.DisableCgroup {
		log.Infof("self cgroup resource limit disabled by --disable-cgroup")
		return nil
	}

	cgr, err := cgroups.NewManager()
	if err != nil {
		return err
	}

	if err := cgr.NewRuntime(
		appName,
		cgroups.ToSpec(
			config.Get().RuntimeCgroup.LimitInitCPU,
			config.Get().RuntimeCgroup.LimitMem,
		),
	); err != nil {
		return fmt.Errorf("new runtime cgroup: %w", err)
	}

	if err := cgr.AddProc(uint64(os.Getpid())); err != nil {
		return fmt.Errorf("cgroup add pid to cgroup.procs: %w", err)
	}

	d.cgroup = cgr

	return nil
}

func (d *Daemon) applyCgroupCPUQuota() error {
	if d.cgroup == nil {
		return nil
	}
	if err := d.cgroup.UpdateRuntime(cgroups.ToSpec(config.Get().RuntimeCgroup.LimitCPU, 0)); err != nil {
		return fmt.Errorf("update runtime: %w", err)
	}

	return nil
}
