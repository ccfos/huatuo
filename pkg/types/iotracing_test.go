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

	"github.com/google/go-cmp/cmp"
)

func TestIOTracingReportJSONContract(t *testing.T) {
	tests := []struct {
		name     string
		report   IOTracingReport
		wantJSON string
	}{
		{
			name:     "empty report preserves top-level field names",
			report:   IOTracingReport{},
			wantJSON: `{"process_file_io_stats":null,"io_schedule_timeout_stacks":null}`,
		},
		{
			name: "populated report preserves nested wire schema",
			report: IOTracingReport{
				Processes: []ProcessFileIOStats{{
					Pid:            42,
					Comm:           "worker",
					TotalFileCount: 1,
					TotalFiles: []FileIOStats{{
						DevName: "sda",
						Path:    "/data/file",
					}},
				}},
				StallStacks: []IOScheduleEvent{{Pid: 42, Comm: "worker", Stack: []string{"io_schedule"}}},
			},
			wantJSON: `{"process_file_io_stats":[{"pid":42,"comm":"worker","container_hostname":"","total_fs_read_bps":0,"total_fs_write_bps":0,"total_disk_read_bps":0,"total_disk_write_bps":0,"total_files":[{"major":0,"minor":0,"dev_name":"sda","inode":0,"path":"/data/file","is_direct":false,"fs_read_bps":0,"fs_write_bps":0,"disk_read_bps":0,"disk_write_bps":0,"q2c_us":0,"d2c_us":0,"max_q2c_us":0,"max_d2c_us":0}],"total_file_count":1}],"io_schedule_timeout_stacks":[{"pid":42,"comm":"worker","container_hostname":"","schedule_latency_us":0,"stack":["io_schedule"]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.report)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var gotJSON map[string]any
			if err := json.Unmarshal(encoded, &gotJSON); err != nil {
				t.Fatalf("json.Unmarshal() output error = %v", err)
			}
			var wantJSON map[string]any
			if err := json.Unmarshal([]byte(tt.wantJSON), &wantJSON); err != nil {
				t.Fatalf("json.Unmarshal() expected JSON error = %v", err)
			}
			if diff := cmp.Diff(wantJSON, gotJSON); diff != "" {
				t.Errorf("JSON mismatch (-want +got):\n%s", diff)
			}

			var gotReport IOTracingReport
			if err := json.Unmarshal(encoded, &gotReport); err != nil {
				t.Fatalf("json.Unmarshal() round-trip error = %v", err)
			}
			if diff := cmp.Diff(tt.report, gotReport); diff != "" {
				t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
