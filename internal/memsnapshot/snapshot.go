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

package memsnapshot

// ProcessIdentity prevents reading a different process after PID reuse.
type ProcessIdentity struct {
	TGID           int    `json:"tgid"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
}

// Request contains the identity and bounds needed while reading a process.
type Request struct {
	Identity     ProcessIdentity `json:"identity"`
	SamplingSeed uint64          `json:"sampling_seed"`
	TopK         int             `json:"top_k"`
}

type Status string

const (
	StatusComplete    Status = "complete"
	StatusPartial     Status = "partial"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
)

// Entry is the only histogram record emitted in production JSON.
type Entry struct {
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Bytes        uint64   `json:"bytes"`
	Objects      uint64   `json:"objects"`
	AverageBytes float64  `json:"average_bytes,omitempty"`
	Stack        []string `json:"stack,omitempty"`
}

// Snapshot is embedded directly into tracer_data.
type Snapshot struct {
	RuntimeVersion  string  `json:"runtime_version,omitempty"`
	Status          Status  `json:"status"`
	Reason          string  `json:"reason,omitempty"`
	DurationMS      uint64  `json:"duration_ms"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
	Entries         []Entry `json:"entries,omitempty"`
}

// ProcessMemory is a language-independent /proc/<pid>/status sample.
// Nil counters are unavailable, not measured zeros. All counters are bytes.
type ProcessMemory struct {
	Status         Status  `json:"status"`
	Reason         string  `json:"reason,omitempty"`
	VirtualBytes   *uint64 `json:"virtual_bytes,omitempty"`
	RSSBytes       *uint64 `json:"rss_bytes,omitempty"`
	RSSAnonBytes   *uint64 `json:"rss_anon_bytes,omitempty"`
	RSSFileBytes   *uint64 `json:"rss_file_bytes,omitempty"`
	RSSShmemBytes  *uint64 `json:"rss_shmem_bytes,omitempty"`
	SwapBytes      *uint64 `json:"swap_bytes,omitempty"`
	PageTableBytes *uint64 `json:"page_table_bytes,omitempty"`
}

func Unavailable(reason string) *Snapshot {
	return &Snapshot{Status: StatusUnavailable, Reason: reason}
}

func Failed(reason string) *Snapshot {
	return &Snapshot{Status: StatusFailed, Reason: reason}
}
