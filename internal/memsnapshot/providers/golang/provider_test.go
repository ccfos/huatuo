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
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestSnapshotReadFailure(t *testing.T) {
	p := New()
	result, err := p.Snapshot(t.Context(), memsnapshot.Request{
		Process:                memsnapshot.ProcessInstanceID{TGID: int(^uint32(0) >> 1), StartTimeTicks: 1},
		MaxMemoryObjectEntries: 10,
	})
	if result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot = %+v, %v, want no snapshot and missing process", result, err)
	}
}

func TestSnapshotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := New().Snapshot(ctx, memsnapshot.Request{MaxMemoryObjectEntries: 1})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot = %+v, %v", result, err)
	}
}

func TestSnapshotIdentityChanged(t *testing.T) {
	identity, err := memsnapshot.ReadProcessInstanceID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity.StartTimeTicks++
	result, err := New().Snapshot(t.Context(), memsnapshot.Request{Process: identity, MaxMemoryObjectEntries: 1})
	if result != nil || err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("identity mismatch: %+v %v", result, err)
	}
}

func TestSnapshotDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancel()
	result, err := New().Snapshot(ctx, memsnapshot.Request{MaxMemoryObjectEntries: 1})
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired snapshot = %+v, %v", result, err)
	}
}
