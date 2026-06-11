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

package profiler

import (
	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

const (
	ProfileTypeCpuSample       = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	ProfileTypeMemSample       = "memory:alloc_space:bytes:space:bytes"
	ProfileTypeLockCountSample = "process_lock:lock:count:lock:count"
	ProfileTypeLockTimeSample  = "process_lock:lock:nanoseconds:lock:nanoseconds"
)

type ProfileData struct {
	ProfileType string `json:"profile_type,omitempty"`
	Profile     ptree.Profile `json:"profile,omitempty"`
}

type ParseOption struct {
	SampleRate int64
}

type TreeItem struct {
	Stack [][]byte `json:"stack,omitempty"`
	Value uint64   `json:"value,omitempty"`
}

const NoSampleRate int64 = 0
