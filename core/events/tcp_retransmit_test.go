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

package events

import (
	"bufio"
	"os/exec"
	"testing"

	"huatuo-bamai/pkg/types"
)

func TestStopTCPSharkWaitsForGracefulExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap 'exit 0' TERM; echo ready; while :; do sleep 1; done")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("helper readiness = %q, error = %v", scanner.Text(), scanner.Err())
	}
	if err := stopTCPShark(cmd, done); err != nil {
		t.Fatalf("stopTCPShark: %v", err)
	}
}

func TestStopTCPSharkAcceptsAlreadyExitedProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan error, 1)
	done <- cmd.Wait()
	if err := stopTCPShark(cmd, done); err != nil {
		t.Fatalf("stopTCPShark: %v", err)
	}
}

func TestHandleTCPRetransmitEventPreservesCorrelationResult(t *testing.T) {
	perfStatus := &types.DropwatchPerfStatus{
		DrainedThroughKtimeNS: 100,
		PerfLost:              1,
	}
	event := &types.TCPRetransmitTracing{
		ContainerID:  "container-id",
		DropLocation: "unknown",
		CorrelationReasons: []types.CorrelationReason{
			types.CorrelationReasonStartupHistoryIncomplete,
		},
		DropwatchPerfStatus: perfStatus,
		DropStack:           "kfree_skb/1",
	}
	if err := handleTCPRetransmitEvent(nil, event); err != nil {
		t.Fatal(err)
	}
	if event.DropLocation != "unknown" {
		t.Fatalf("DropLocation = %q, want finalized result unchanged", event.DropLocation)
	}
	if event.DropwatchPerfStatus != perfStatus {
		t.Fatal("DropwatchPerfStatus changed while saving finalized result")
	}
	if len(event.CorrelationReasons) != 1 ||
		event.CorrelationReasons[0] != types.CorrelationReasonStartupHistoryIncomplete {
		t.Fatalf("CorrelationReasons = %v, want finalized reasons unchanged", event.CorrelationReasons)
	}
	if event.DropStack != "kfree_skb/1" {
		t.Fatalf("DropStack = %q, want finalized stack unchanged", event.DropStack)
	}
}
