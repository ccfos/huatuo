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

package subsystem

// Subsystem names for cgroup v1/v2.
const (
	SubsystemCPU       = "cpu"
	SubsystemCPUAcct   = "cpuacct"
	SubsystemCPUSet    = "cpuset"
	SubsystemMemory    = "memory"
	SubsystemBlkIO     = "blkio"
	SubsystemDevices   = "devices"
	SubsystemHugetlb   = "hugetlb"
	SubsystemFreezer   = "freezer"
	SubsystemPids      = "pids"
	SubsystemNetCLS    = "net_cls"
	SubsystemNetPrio   = "net_prio"
	SubsystemPerfEvent = "perf_event"
	SubsystemRdma      = "rdma"
)
