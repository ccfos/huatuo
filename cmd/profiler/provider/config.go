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

package provider

import (
	"time"

	"github.com/ccfos/huatuo/pkg/profiling"
)

type Config struct {
	PIDs                 []int
	Freq                 int
	Duration             time.Duration
	MaxProfilerProcesses int
	AggregationInterval  time.Duration
	CPUIDs               []int
	RequireHardwarePMU   bool

	ServerAddress             string
	ContainerID               string
	Type                      profiling.Type
	ExecPath                  string
	ThreadGroup               bool
	ToolDir                   string
	PythonMemoryStackMode     string
	PythonMemoryMergeThreads  bool
	LogBpfDebug               bool
	Mode                      profiling.Mode
	OffCPUPhase               profiling.OffCPUPhase
	OffCPUMinDurationUS       uint64
	OffCPUStatsEnabled        bool
	PhysicalMemoryProbability uint
}

func (c *Config) PID() int {
	if len(c.PIDs) == 0 {
		return 0
	}
	return c.PIDs[0]
}
