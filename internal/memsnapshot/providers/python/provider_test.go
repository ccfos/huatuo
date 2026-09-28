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

package python

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestSnapshotResult(t *testing.T) {
	readErr := fmt.Errorf("read object: %w", os.ErrPermission)
	for _, test := range []struct {
		name       string
		input      *memsnapshot.Snapshot
		readErr    error
		wantStatus memsnapshot.Status
	}{
		{
			name: "complete", input: &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete},
			wantStatus: memsnapshot.StatusComplete,
		},
		{
			name: "partial", input: &memsnapshot.Snapshot{
				Status: memsnapshot.StatusPartial, Reason: "read budget reached",
			},
			wantStatus: memsnapshot.StatusPartial,
		},
		{
			name: "unsupported runtime", readErr: unsupportedRuntime("unknown layout"),
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

func TestSnapshotErrorBoundsPreserveCause(t *testing.T) {
	cause := &os.PathError{
		Op: "read", Path: strings.Repeat("界", maxReasonBytes), Err: os.ErrPermission,
	}
	result, err := snapshotResult(nil, cause)
	if result != nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("capture = %+v, %v, want no snapshot and permission error", result, err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr != cause {
		t.Fatalf("capture error lost path details: %v", err)
	}
	if len(err.Error()) > maxReasonBytes || !utf8.ValidString(err.Error()) {
		t.Fatalf("invalid bounded error: %q", err.Error())
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
