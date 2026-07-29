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

package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"huatuo-bamai/pkg/types"
)

func TestEvidenceWorkerSerializesDeduplicatesAndAppliesCooldown(t *testing.T) {
	clock := &fakeEvidenceClock{now: time.Unix(100, 0)}
	results := make(chan EvidenceResult, 3)
	started := make(chan string, 8)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32

	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.now = clock.Now
	worker.lookupPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	worker.commandTimeout = time.Hour
	worker.runCommand = func(
		ctx context.Context,
		executable string,
		args []string,
		maxBytes int,
	) commandExecution {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- args[len(args)-1]
		if calls.Add(1) == 1 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return commandExecution{exitCode: -1, err: ctx.Err()}
			}
		}
		switch args[0] {
		case "smart-log":
			return commandExecution{
				stdout:   []byte(`{"critical_warning":0,"media_errors":0}`),
				exitCode: 0,
			}
		case "error-log":
			return commandExecution{
				stdout:   []byte(`{"errors":[]}`),
				exitCode: 0,
			}
		default:
			return commandExecution{exitCode: -1, err: errors.New("unexpected command")}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)

	request := func(target string) EvidenceRequest {
		return EvidenceRequest{
			Trigger: types.IOHealthEvent{
				Type:   "nvme_timeout",
				Device: target + "n1",
			},
			Target:      target,
			Protocol:    EvidenceProtocolNVMe,
			TriggeredAt: time.Unix(50, 0),
		}
	}
	if !worker.Submit(request("nvme0")) {
		t.Fatal("first nvme0 trigger was not accepted")
	}
	if got := <-started; got != "/dev/nvme0" {
		t.Fatalf("first command target = %q, want /dev/nvme0", got)
	}
	if worker.Submit(request("nvme0")) {
		t.Fatal("running nvme0 trigger should have been merged")
	}
	if !worker.Submit(request("nvme1")) {
		t.Fatal("nvme1 trigger was not accepted")
	}
	if worker.Submit(request("nvme1")) {
		t.Fatal("pending nvme1 trigger should have been merged")
	}
	close(releaseFirst)

	first := receiveEvidenceResult(t, results)
	second := receiveEvidenceResult(t, results)
	if first.Target != "nvme0" || second.Target != "nvme1" {
		t.Fatalf("result order = %q, %q", first.Target, second.Target)
	}
	if first.Event.CollectionStatus != "ok" ||
		second.Event.CollectionStatus != "ok" {
		t.Fatalf("collection statuses = %q, %q", first.Event.CollectionStatus, second.Event.CollectionStatus)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent commands = %d, want 1", maximum.Load())
	}
	if worker.Submit(request("nvme0")) {
		t.Fatal("nvme0 trigger inside cooldown should not be accepted")
	}

	clock.Advance(evidenceCooldown)
	if !worker.Submit(request("nvme0")) {
		t.Fatal("nvme0 trigger after cooldown was not accepted")
	}
	if result := receiveEvidenceResult(t, results); result.Target != "nvme0" {
		t.Fatalf("post-cooldown result target = %q, want nvme0", result.Target)
	}

	cancel()
	worker.Wait()
}

func TestEvidenceWorkerReportsUnsupportedAttemptWithoutCommand(t *testing.T) {
	results := make(chan EvidenceResult, 1)
	var commandCalled atomic.Bool
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.runCommand = func(
		context.Context,
		string,
		[]string,
		int,
	) commandExecution {
		commandCalled.Store(true)
		return commandExecution{exitCode: -1, err: errors.New("unexpected command")}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	if !worker.Submit(EvidenceRequest{
		Trigger: types.IOHealthEvent{
			Type:   "block_error",
			Device: "dm-0",
		},
		Protocol: EvidenceProtocolSCSI,
		Reason:   CollectionReasonTargetUnsupported,
	}) {
		t.Fatal("unsupported attempt was not accepted")
	}
	result := receiveEvidenceResult(t, results)
	if result.Target != "dm-0" ||
		result.Event.CollectionStatus != "unsupported" ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetUnsupported {
		t.Fatalf("unsupported result = %#v", result)
	}
	if commandCalled.Load() {
		t.Fatal("unsupported attempt executed a command")
	}
	cancel()
	worker.Wait()
}

type fakeEvidenceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeEvidenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeEvidenceClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func receiveEvidenceResult(
	t *testing.T,
	results <-chan EvidenceResult,
) EvidenceResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evidence result")
		return EvidenceResult{}
	}
}
