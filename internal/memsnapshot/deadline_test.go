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

import (
	"context"
	"testing"
	"time"
)

// The java and python snapshot providers use these helpers to stop scanning
// in time for result reduction: DeadlineWithReserve shrinks the caller's
// deadline by a reserve, and DeadlineReached gates the check on whether a
// deadline exists at all.

func TestDeadlineWithReserve(t *testing.T) {
	if deadline, ok := DeadlineWithReserve(context.Background(), time.Second); ok {
		t.Fatalf("DeadlineWithReserve(no deadline) = (%v, true), want (zero, false)", deadline)
	}

	parent := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), parent)
	defer cancel()

	const reserve = 2 * time.Second

	deadline, ok := DeadlineWithReserve(ctx, reserve)
	if !ok {
		t.Fatal("DeadlineWithReserve(with deadline) reported no deadline")
	}

	if want := parent.Add(-reserve); !deadline.Equal(want) {
		t.Fatalf("DeadlineWithReserve() = %v, want %v", deadline, want)
	}
}

func TestDeadlineReached(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		deadline time.Time
		enabled  bool
		want     bool
	}{
		{name: "disabled ignores a past deadline", deadline: now.Add(-time.Hour), enabled: false, want: false},
		{name: "disabled ignores a future deadline", deadline: now.Add(time.Hour), enabled: false, want: false},
		{name: "enabled with past deadline", deadline: now.Add(-time.Millisecond), enabled: true, want: true},
		{name: "enabled with future deadline", deadline: now.Add(time.Hour), enabled: true, want: false},
	}

	for _, tt := range tests {
		if got := DeadlineReached(tt.deadline, tt.enabled); got != tt.want {
			t.Fatalf("%s: DeadlineReached(%v, %v) = %v, want %v", tt.name, tt.deadline, tt.enabled, got, tt.want)
		}
	}
}
