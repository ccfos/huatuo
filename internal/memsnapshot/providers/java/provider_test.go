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

package java

import (
	"errors"
	"os"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestJavaFailureClassification(t *testing.T) {
	identity, err := memsnapshot.ReadProcessInstance(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := memsnapshot.Request{Process: identity, TopK: 10}
	snapshot, err := New().Snapshot(t.Context(), request)
	if err != nil || snapshot == nil || snapshot.Status != memsnapshot.StatusUnavailable {
		t.Fatalf("non-JVM result: %+v, %v", snapshot, err)
	}
	request.Process.StartTimeTicks++
	snapshot, err = New().Snapshot(t.Context(), request)
	if err == nil || snapshot != nil {
		t.Fatalf("stale identity result: %+v, %v", snapshot, err)
	}
}

func TestSnapshotReadFailure(t *testing.T) {
	missingPID := int(^uint(0) >> 1)
	result, err := New().Snapshot(t.Context(), memsnapshot.Request{
		Process: memsnapshot.ProcessInstance{TGID: missingPID, StartTimeTicks: 1}, TopK: 10,
	})
	if result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot = %+v, %v, want no snapshot and missing process", result, err)
	}
}
