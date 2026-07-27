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

package autotracing

import (
	"testing"

	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/toolstream/transport"
	"huatuo-bamai/pkg/types"
)

func TestHandleIotracingEventAcknowledgesPendingReason(t *testing.T) {
	const taskID = "iotracing-test-task"

	pending := &pendingIotracingReason{
		reason:  &ReasonSnapshot{Type: ioReasonUtil.String()},
		handled: make(chan struct{}, 1),
	}
	pendingReasons.Store(taskID, pending)
	t.Cleanup(func() {
		pendingReasons.Delete(taskID)
	})

	err := handleIotracingEvent(
		&toolstream.Session{
			Session: &transport.Session{
				TaskID: taskID,
			},
		},
		&types.IOTracingReport{},
	)
	if err != nil {
		t.Fatalf("handleIotracingEvent() error = %v", err)
	}

	select {
	case <-pending.handled:
	default:
		t.Fatal("handleIotracingEvent() did not acknowledge the pending reason")
	}
	if _, ok := pendingReasons.Load(taskID); ok {
		t.Fatal("handleIotracingEvent() left the pending reason in the registry")
	}
}
