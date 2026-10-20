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

// ProcessInstanceID identifies a process lifetime by TGID and start time.
// It distinguishes PID reuse, but does not detect exec within the same process.
type ProcessInstanceID struct {
	TGID           int    `json:"tgid"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
}

// Request selects a process instance and bounds its snapshot.
type Request struct {
	Process                ProcessInstanceID `json:"identity"`
	SamplingSeed           uint64            `json:"sampling_seed"`
	MaxMemoryObjectEntries int               `json:"max_memory_object_entries"`
}

type Status string

const (
	SnapshotStatusComplete    Status = "complete"
	SnapshotStatusPartial     Status = "partial"
	SnapshotStatusUnavailable Status = "unavailable"
	SnapshotStatusFailed      Status = "failed"
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
	StatusReason    string  `json:"status_reason,omitempty"`
	DurationMS      uint64  `json:"duration_ms"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
	Entries         []Entry `json:"entries,omitempty"`
}

// ProcessMemory is a language-independent /proc/<pid>/status sample.
// Nil counters are unavailable, not measured zeros. All counters are bytes.
type ProcessMemory struct {
	Status         Status  `json:"status"`
	StatusReason   string  `json:"status_reason,omitempty"`
	VirtualBytes   *uint64 `json:"virtual_bytes,omitempty"`
	RSSBytes       *uint64 `json:"rss_bytes,omitempty"`
	RSSAnonBytes   *uint64 `json:"rss_anon_bytes,omitempty"`
	RSSFileBytes   *uint64 `json:"rss_file_bytes,omitempty"`
	RSSShmemBytes  *uint64 `json:"rss_shmem_bytes,omitempty"`
	SwapBytes      *uint64 `json:"swap_bytes,omitempty"`
	PageTableBytes *uint64 `json:"page_table_bytes,omitempty"`
}

func Unavailable(reason string) *Snapshot {
	return &Snapshot{Status: SnapshotStatusUnavailable, StatusReason: reason}
}

func Failed(reason string) *Snapshot {
	return &Snapshot{Status: SnapshotStatusFailed, StatusReason: reason}
}
