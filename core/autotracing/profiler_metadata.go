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

package autotracing

// CPUIdleMetaData carries the CPU-idle thresholds and observed values that
// accompany a profiler upload. Flame data is supplied separately via
// TracerData.FlameData.
type CPUIdleMetaData struct {
	NowUser             int64 `json:"user"`
	UserThreshold       int64 `json:"user_threshold"`
	DeltaUser           int64 `json:"deltauser"`
	DeltaUserThreshold  int64 `json:"deltauser_threshold"`
	NowSys              int64 `json:"sys"`
	SysThreshold        int64 `json:"sys_threshold"`
	DeltaSys            int64 `json:"deltasys"`
	DeltaSysThreshold   int64 `json:"deltasys_threshold"`
	NowUsage            int64 `json:"usage"`
	UsageThreshold      int64 `json:"usage_threshold"`
	DeltaUsage          int64 `json:"deltausage"`
	DeltaUsageThreshold int64 `json:"deltausage_threshold"`
}

// CpuSysMetaData carries the cpu-sys thresholds and observed values that
// accompany a profiler upload. Flame data is supplied separately via
// TracerData.FlameData.
type CpuSysMetaData struct {
	NowSys            int64 `json:"sys"`
	SysThreshold      int64 `json:"sys_threshold"`
	DeltaSys          int64 `json:"deltasys"`
	DeltaSysThreshold int64 `json:"deltasys_threshold"`
}
