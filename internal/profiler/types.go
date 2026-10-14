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

package profiler

import (
	"time"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

const (
	// ProfileTypeCpuSample is the profile type for CPU sample.
	ProfileTypeCpuSample        = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	ProfileTypeOffCpuSample     = "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"
	ProfileTypeMemSample        = "memory:alloc_space:bytes:space:bytes"
	ProfileTypeLockCountSample  = "process_lock:lock:count:lock:count"
	ProfileTypeLockTimeSample   = "process_lock:lock:nanoseconds:lock:nanoseconds"
	ProfileTypeIRQTracingSample = "irqtracing:irq:count:irq:count"
)

// ProfileData holds the parser's intermediate tree profile. NewWindow converts
// it into the common wire and output model before any formatting or storage.
type ProfileData struct {
	ProfileType string
	Profile     ptree.Profile
}

// ParseInput holds the input parameters for ParseCollapsedData and ParseRawData.
type ParseInput struct {
	StartTime    time.Time
	ProfileType  string
	ProfilerName string
	Data         []byte
	Opt          *ParseOption
	PID          int
}

// ParseOption is the option for ParseTree.
type ParseOption struct {
	// SampleRate is only used for CPU sample.
	SampleRate int64
}

// NoSampleRate indicates that sampling rate is disabled, used for event-driven sampling types.
const NoSampleRate int64 = 0
