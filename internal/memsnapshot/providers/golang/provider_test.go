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

package golang

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestSnapshotResult(t *testing.T) {
	readErr := fmt.Errorf("read profile: %w", os.ErrPermission)
	for _, test := range []struct {
		name       string
		input      *snapshot
		readErr    error
		wantStatus memsnapshot.Status
	}{
		{
			name: "complete", input: &snapshot{RateKnown: true, SampleRate: 1},
			wantStatus: memsnapshot.StatusComplete,
		},
		{
			name: "partial", input: &snapshot{RateKnown: true, SampleRate: 1, PartialReason: "read budget reached"},
			wantStatus: memsnapshot.StatusPartial,
		},
		{
			name: "unsupported runtime", readErr: fmt.Errorf("discover: %w", errUnsupportedRuntime),
			wantStatus: memsnapshot.StatusUnavailable,
		},
		{
			name: "missing profile symbols", readErr: errMBucketsSymbolNotFound,
			wantStatus: memsnapshot.StatusUnavailable,
		},
		{name: "read failure", readErr: readErr},
		{name: "reader returned no snapshot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := snapshotResult(test.input, test.readErr)
			if test.wantStatus == "" {
				if result != nil || err == nil {
					t.Fatalf("capture = %+v, %v, want no snapshot and an error", result, err)
				}
				if test.readErr != nil && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("capture error lost read cause: %v", err)
				}
				return
			}
			if err != nil || result == nil || result.Status != test.wantStatus {
				t.Fatalf("capture = %+v, %v, want status %s", result, err, test.wantStatus)
			}
			if test.wantStatus != memsnapshot.StatusComplete && result.Reason == "" {
				t.Fatal("degraded capture has no reason")
			}
		})
	}
}

func TestSnapshotReadFailure(t *testing.T) {
	p := &Provider{reader: newReader(t.TempDir())}
	result, err := p.Snapshot(t.Context(), memsnapshot.Request{
		Process: memsnapshot.ProcessInstance{TGID: 42, StartTimeTicks: 1}, TopK: 10,
	})
	if result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture = %+v, %v, want no snapshot and missing process", result, err)
	}
}
