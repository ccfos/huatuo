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
	"os"
	"path/filepath"
	"strings"

	extcgroups "github.com/containerd/cgroups/v3"
	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"
)

func isKataRuntime(runtimeType string) bool {
	return runtimeType == "io.containerd.kata.v2" ||
		(strings.HasPrefix(runtimeType, "io.containerd.kata-") && strings.HasSuffix(runtimeType, ".v2"))
}

// HostKataSandboxID identifies containers backed by a shared Host VM cgroup.
// Guest-discovered containers do not carry the Host runtime metadata.
func (c *Container) HostKataSandboxID() string {
	if isKataRuntime(c.RuntimeType) {
		return c.SandboxID
	}
	return ""
}

func populateKataContainer(container *Container, info *containerdRuntimeInfo) error {
	if info.PID <= 0 {
		return fmt.Errorf("invalid Kata hypervisor pid %d for container %s", info.PID, container.ID)
	}
	if info.SandboxID == "" {
		return fmt.Errorf("Kata container %s has no sandbox ID", container.ID)
	}
	cgroupPath, err := kataCgroupPath(procfs.Path(fmt.Sprint(info.PID), "cgroup"), cgroups.CgroupMode())
	if err != nil {
		return fmt.Errorf("resolve Kata hypervisor cgroup: %w", err)
	}
	css, err := parseCgroupCPUCSS(cgroupPath)
	if err != nil {
		return fmt.Errorf("resolve Kata hypervisor CPU CSS: %w", err)
	}
	container.InitPid = info.PID
	container.SandboxID = info.SandboxID
	container.CgroupPath = cgroupPath
	// The VM's net namespace is not the Guest container's net namespace.
	container.CgroupCss = css
	return nil
}

func kataCgroupPath(procCgroupFile string, mode cgroups.Mode) (string, error) {
	legacy, unified, err := extcgroups.ParseCgroupFileUnified(procCgroupFile)
	if err != nil {
		return "", err
	}
	cgroupPath := unified
	root := cgroups.RootfsDefaultPath()
	files := []string{"cpu.stat", "cpu.max"}
	switch mode {
	case cgroups.Unified:
	case cgroups.Legacy, cgroups.Hybrid:
		cgroupPath = legacy["cpu"]
		// Container currently has one cgroup path shared by CPU and memory.
		// Do not silently apply one controller's path to a different hierarchy.
		for _, controller := range []string{"cpuacct", "memory"} {
			if legacy[controller] != cgroupPath {
				return "", fmt.Errorf("Kata %s cgroup path differs from CPU cgroup path", controller)
			}
		}
		root = cgroups.RootFsFilePath("cpu")
		files = []string{"cpuacct.stat", "cpuacct.usage", "cpu.cfs_quota_us", "cpu.cfs_period_us"}
	default:
		return "", fmt.Errorf("unsupported Kata cgroup mode %d", mode)
	}
	if !filepath.IsAbs(cgroupPath) || cgroupPath == "/" || filepath.Clean(cgroupPath) != cgroupPath {
		return "", fmt.Errorf("invalid Kata cgroup path %q", cgroupPath)
	}
	for _, name := range files {
		if _, err := os.ReadFile(filepath.Join(root, cgroupPath, name)); err != nil {
			return "", err
		}
	}
	return cgroupPath, nil
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
		} else if matched.Value != "" || matched.ValueFrom.FieldRef == nil || matched.ValueFrom.FieldRef.FieldPath != identity.field || (matched.ValueFrom.FieldRef.APIVersion != "" && matched.ValueFrom.FieldRef.APIVersion != "v1") {
			return false
		}
	}
	return true
}

// keepKataRepresentative prefers business containers, then retains the first
// discovered live member of a sandbox.
// New members have a later SyncedAt, so they cannot displace it. The ID breaks
// ties without depending on map iteration order. No separate lifetime cache is
// needed: removing the representative naturally selects the next live member.
func keepKataRepresentative(current, candidate *Container) bool {
	if current == nil || current.HostKataSandboxID() == "" ||
		current.HostKataSandboxID() != candidate.HostKataSandboxID() {
		return false
	}
	if current.Type.IsNormal() != candidate.Type.IsNormal() {
		return current.Type.IsNormal()
	}
	if current.SyncedAt.Equal(candidate.SyncedAt) {
		return current.ID < candidate.ID
	}
	return current.SyncedAt.Before(candidate.SyncedAt)
}
