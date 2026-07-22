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
)

func TestIOTracingReportJSONContract(t *testing.T) {
	report := IOTracingReport{
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
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"process_file_io_stats", "io_schedule_timeout_stacks"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("report JSON is missing %q: %s", field, encoded)
		}
	}

	var decoded IOTracingReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(decoded.Processes) != 1 || decoded.Processes[0].TotalFiles[0].Path != "/data/file" {
		t.Fatalf("round-trip processes = %+v, want file path", decoded.Processes)
	}
	if len(decoded.StallStacks) != 1 || decoded.StallStacks[0].Stack[0] != "io_schedule" {
		t.Fatalf("round-trip stall stacks = %+v, want stack", decoded.StallStacks)
	}
}
