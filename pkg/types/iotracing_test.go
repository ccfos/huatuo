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

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIOTracingSnapshotJSON(t *testing.T) {
	tests := []struct {
		name     string
		snapshot IOTracingSnapshot
		wantJSON string
	}{
		{
			name:     "empty snapshot",
			snapshot: IOTracingSnapshot{},
			wantJSON: `{
				"process_file_io_stats": null,
				"io_schedule_timeout_stacks": null
			}`,
		},
		{
			name: "all fields",
			snapshot: IOTracingSnapshot{
				Processes: []ProcessFileIOStats{{
					PID:               42,
					Comm:              "worker",
					ContainerHostname: "container-1",
					TotalFsReadBps:    100,
					TotalFsWriteBps:   200,
					TotalDiskReadBps:  300,
					TotalDiskWriteBps: 400,
					TotalFiles: []FileIOStats{{
						Major:        8,
						Minor:        1,
						DevName:      "sda1",
						Inode:        123,
						Path:         "/data/file",
						IsDirect:     true,
						FsReadBps:    10,
						FsWriteBps:   20,
						DiskReadBps:  30,
						DiskWriteBps: 40,
						Q2CUs:        50,
						D2CUs:        60,
						MaxQ2CUs:     70,
						MaxD2CUs:     80,
					}},
					TotalFileCount: 1,
				}},
				StallStacks: []IOScheduleEvent{{
					PID:               43,
					TID:               44,
					CPU:               2,
					Comm:              "kworker",
					ContainerHostname: "container-2",
					ScheduleLatencyUS: 90,
					Stack:             []string{"io_schedule", "worker_thread"},
				}},
			},
			wantJSON: `{
				"process_file_io_stats": [{
					"pid": 42,
					"comm": "worker",
					"container_hostname": "container-1",
					"total_fs_read_bps": 100,
					"total_fs_write_bps": 200,
					"total_disk_read_bps": 300,
					"total_disk_write_bps": 400,
					"total_files": [{
						"major": 8,
						"minor": 1,
						"dev_name": "sda1",
						"inode": 123,
						"path": "/data/file",
						"is_direct": true,
						"fs_read_bps": 10,
						"fs_write_bps": 20,
						"disk_read_bps": 30,
						"disk_write_bps": 40,
						"q2c_us": 50,
						"d2c_us": 60,
						"max_q2c_us": 70,
						"max_d2c_us": 80
					}],
					"total_file_count": 1
				}],
				"io_schedule_timeout_stacks": [{
					"pid": 43,
					"tid": 44,
					"cpu": 2,
					"comm": "kworker",
					"container_hostname": "container-2",
					"schedule_latency_us": 90,
					"stack": ["io_schedule", "worker_thread"]
				}]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.snapshot)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(encoded))

			var decoded IOTracingSnapshot
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, tt.snapshot, decoded)
		})
	}
}
