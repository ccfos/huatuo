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

package stats

// All members are measured in microseconds
type CpuUsage struct {
	Usage  uint64
	User   uint64
	System uint64
}

type CpuQuota struct {
	Quota             uint64
	Period            uint64
	EffectiveCPUCount uint64
}

type MemoryUsage struct {
	Usage      uint64
	MaxLimited uint64
}

// LoadStats contains instantaneous task state counts for a cgroup.
type LoadStats struct {
	NrSleeping        uint64
	NrRunning         uint64
	NrStopped         uint64
	NrUninterruptible uint64
	NrIoWait          uint64
}
