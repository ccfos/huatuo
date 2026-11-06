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
	"strings"
)

func IsKataRuntime(runtimeType string) bool {
	return runtimeType == "io.containerd.kata.v2" ||
		(strings.HasPrefix(runtimeType, "io.containerd.kata-") && strings.HasSuffix(runtimeType, ".v2"))
}

// HostKataSandboxID identifies containers backed by a shared Host VM cgroup.
// Guest-discovered containers do not carry the Host runtime metadata.
func (c *Container) HostKataSandboxID() string {
	if IsKataRuntime(c.RuntimeType) {
		return c.SandboxID
	}
	return ""
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
