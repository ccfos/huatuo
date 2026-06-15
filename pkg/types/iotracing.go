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

package types

// IOTracingReport is the wire schema produced by one iotracing run and
// sent to huatuo-bamai over toolstream. The trigger reason snapshot is
// attached by the daemon after receipt and is not part of this payload.
type IOTracingReport struct {
	Processes   []ProcessFileIOStats `json:"process_file_io_stats"`
	StallStacks []IOScheduleEvent    `json:"io_schedule_timeout_stacks"`
}

// IOScheduleEvent records one task that spent longer than the configured
// threshold in io_schedule, with the kernel stack captured at the stall.
type IOScheduleEvent struct {
	Pid               uint32   `json:"pid"`
	Comm              string   `json:"comm"`
	ContainerHostname string   `json:"container_hostname"`
	LatencyUs         uint64   `json:"schedule_latency_us"`
	Stack             []string `json:"stack"`
}

// ProcessFileIOStats aggregates one process's IO over the trace window.
// Total* fields are summed across all touched files; TotalFiles is a
// top-K subset for inspection, while TotalFileCount reports the
// untruncated count so callers can see when truncation occurred.
type ProcessFileIOStats struct {
	Pid               uint32        `json:"pid"`
	Comm              string        `json:"comm"`
	ContainerHostname string        `json:"container_hostname"`
	TotalFsReadBps    uint64        `json:"total_fs_read_bps"`
	TotalFsWriteBps   uint64        `json:"total_fs_write_bps"`
	TotalDiskReadBps  uint64        `json:"total_disk_read_bps"`
	TotalDiskWriteBps uint64        `json:"total_disk_write_bps"`
	TotalFiles        []FileIOStats `json:"total_files"`
	TotalFileCount    uint64        `json:"total_file_count"`
}

// FileIOStats is one per-file row inside ProcessFileIOStats. Byte fields
// are per-second rates over the trace window; latency fields are in
// microseconds.
type FileIOStats struct {
	Major        uint32 `json:"major"`
	Minor        uint32 `json:"minor"`
	Inode        uint64 `json:"inode"`
	Path         string `json:"path"`
	IsDirect     bool   `json:"is_direct"`
	FsReadBps    uint64 `json:"fs_read_bps"`
	FsWriteBps   uint64 `json:"fs_write_bps"`
	DiskReadBps  uint64 `json:"disk_read_bps"`
	DiskWriteBps uint64 `json:"disk_write_bps"`
	Q2CUs        uint64 `json:"q2c_us"`
	D2CUs        uint64 `json:"d2c_us"`
}
