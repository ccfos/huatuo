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

package provider

import (
	"bytes"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
)

func newNativeBPFConstants(pid int, cgroupCSSRef pod.CgroupCSSRef, threadGroup bool) map[string]any {
	return map[string]any{
		"profiler_filter_css_addr":  cgroupCSSRef.CSSAddr,
		"profiler_filter_cgroup_id": cgroupCSSRef.CgroupID,
		"profiler_filter_pid":       uint32(pid),
		"profiler_filter_threads":   threadGroup,
	}
}

func resolveContainerCgroupCSSRef(cfg *Config, subsys string) (pod.CgroupCSSRef, error) {
	if cfg.ContainerID == "" {
		return pod.CgroupCSSRef{}, nil
	}
	ref, err := pod.ContainerCSSBySubsys(cfg.ContainerID, subsys)
	if err != nil {
		return pod.CgroupCSSRef{}, fmt.Errorf("resolve container %q %s CSS identity locally: %w", cfg.ContainerID, subsys, err)
	}
	return ref, nil
}

func taskCommString(comm [bpf.TaskCommLen]byte) string {
	length := bytes.IndexByte(comm[:], 0)
	if length == -1 {
		length = len(comm)
	}
	return string(comm[:length])
}
