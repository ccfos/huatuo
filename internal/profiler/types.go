// Copyright 2025 The HuaTuo Authors
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
	ProfileTypeCpuSample       = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	ProfileTypeMemSample       = "memory:alloc_space:bytes:space:bytes"
	ProfileTypeLockCountSample = "process_lock:lock:count:lock:count"
	ProfileTypeLockTimeSample  = "process_lock:lock:nanoseconds:lock:nanoseconds"
)

// MetadataCollection is the storage collection name for profiling metadata documents.
// Profiling metadata reuses tracing.DocumentStoreMapper; profile_type is queried in-place
// via the nested path tracer_data.flamedata.profile_type.
const MetadataCollection = "profiling_metadata"

// ProfileData is the data saved by the profiler.
type ProfileData struct {
	ProfileType string `json:"profile_type,omitempty"`
	// Please note:
	//
	//	In pyroscope 1.13.0, use profilev1.Profile instead of ptree.Profile, but it depends
	//	on the go1.23.0 or later.
	//
	//	Like:
	//		Profile     profilev1.Profile `json:"profile,omitempty"`
	//
	//	Currently, in go1.22.4, the profilev1.Profile is the same as ptree.Profile, so we
	//	use ptree.Profile.
	Profile ptree.Profile `json:"profile,omitempty"`
}

// ParseInput holds the input parameters for ParseCollapsedData and ParseRawData.
type ParseInput struct {
	StartTime    time.Time
	ProfileType  string
	ProfilerName string
	Data         []byte
	Opt          *ParseOption
	PID          int
	// SampleDimensions carries optional dimension values for the single-PID
	// path (ParseRawData / ParseCollapsedData with a single PID). Used together
	// with Opt.Dimensions to inject labels. Zero values are skipped. The
	// multi-PID path carries its own per-sample dimensions via SampleOutput.
	SampleDimensions SampleDimensions
}

// SampleDimensions carries the per-sample dimension values used by
// ParseCollapsedData/ParseRawData when Opt.Dimensions requests label
// injection on the single-PID path. Zero values are skipped, so existing
// callers that don't set these remain unaffected.
type SampleDimensions struct {
	TGID         int
	CgroupPath   string
	ProcessGroup string
}

// Dimensions carries optional profiling dimensions (PID/TGID/cgroup/process
// group) that, when set, are injected as pprof-style labels into the first
// frame of every stack in the parsed output. A zero-value Dimensions means
// "inject nothing" — existing output is byte-identical.
//
// The labels use the canonical pprof/Pyroscope form (e.g. `pid=123;cgroup=/...`)
// so downstream backends (Pyroscope, Parca, flamegraph) can parse them without
// per-backend branching.
type Dimensions struct {
	// PID controls injection of the PID label. Defaults to false; when true
	// the PID passed to the parser is emitted as `pid=<n>`.
	PID bool
	// TGID controls injection of the TGID label. When true and a TGID is
	// available on the sample, emitted as `tgid=<n>`.
	TGID bool
	// Cgroup controls injection of the cgroup path label. When true and the
	// sample carries a non-empty CgroupPath, emitted as `cgroup=<path>`.
	Cgroup bool
	// ProcessGroup controls injection of the process-group label. When true
	// and the sample carries a non-empty ProcessGroup, emitted as
	// `pgroup=<name>`.
	ProcessGroup bool
}

// Enabled reports whether any dimension injection is requested.
func (d Dimensions) Enabled() bool {
	return d.PID || d.TGID || d.Cgroup || d.ProcessGroup
}

// ParseOption is the option for ParseTree.
type ParseOption struct {
	// SampleRate is only used for CPU sample.
	SampleRate int64
	// Dimensions, when Enabled(), injects profiling-dimension labels into the
	// first frame of each parsed stack. Zero value = no injection (back-compat).
	Dimensions Dimensions
}

// TreeItem is the item in the tree.
type TreeItem struct {
	Stack [][]byte `json:"stack,omitempty"`
	Value uint64   `json:"value,omitempty"`
}

// NoSampleRate indicates that sampling rate is disabled, used for event-driven sampling types.
const NoSampleRate int64 = 0
