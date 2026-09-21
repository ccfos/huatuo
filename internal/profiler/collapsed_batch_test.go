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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCollapsedBatchPreservesPopulatedOutputs(t *testing.T) {
	pid := os.Getpid()
	tests := []struct {
		name    string
		outputs []SampleOutput
		total   int64
	}{
		{"empty first", []SampleOutput{{PID: pid}, {PID: pid, Output: "root;leaf 7\n"}}, 7},
		{"empty last", []SampleOutput{{PID: pid, Output: "root;leaf 7\n"}, {PID: pid}}, 7},
		{"all empty", []SampleOutput{{PID: pid}, {PID: pid}}, 0},
		{"empty batch", []SampleOutput{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.outputs)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseCollapsedData(t.Context(), &ParseInput{StartTime: time.Now(), ProfileType: ProfileTypeMemSample, Data: data, PID: pid})
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for _, s := range got.Profile.Sample {
				total += s.Value[0]
			}
			if total != tt.total {
				t.Errorf("sample total = %d, want %d", total, tt.total)
			}
		})
	}
}

func TestCollapsedBatchRetainsExitedProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	exitedPID := cmd.Process.Pid
	data, err := json.Marshal([]SampleOutput{
		{PID: exitedPID, Output: "exited_root;captured_work 7\n"},
		{PID: os.Getpid(), Output: "live_root;other_work 3\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCollapsedData(t.Context(), &ParseInput{StartTime: time.Now(), ProfileType: ProfileTypeMemSample, Data: data})
	if err != nil {
		t.Fatalf("captured samples must survive process exit: %v", err)
	}
	var total int64
	for _, s := range got.Profile.Sample {
		total += s.Value[0]
	}
	if total != 10 {
		t.Errorf("sample total = %d, want 10", total)
	}
	want := fmt.Sprintf("process %d", exitedPID)
	found := false
	for _, s := range got.Profile.StringTable {
		if s == want {
			found = true
		}
	}
	if !found {
		t.Errorf("profile must retain exited process identity %q", want)
	}
}

func TestCollapsedBatchCancellationAndInvalidPID(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := ParseCollapsedData(ctx, &ParseInput{ProfileType: ProfileTypeMemSample, Data: []byte(`[{"pid":1,"output":"root 7\n"}]`)})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want cancellation", err)
	}
	for _, pid := range []int{0, -1} {
		data := []byte(fmt.Sprintf(`[{"pid":%d,"output":"root 7\n"}]`, pid))
		_, err := ParseCollapsedData(t.Context(), &ParseInput{ProfileType: ProfileTypeMemSample, Data: data})
		if err == nil || !strings.Contains(err.Error(), "PID") {
			t.Errorf("invalid PID %d: %v", pid, err)
		}
	}
}
