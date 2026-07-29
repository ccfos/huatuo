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
	"os/exec"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/pkg/types"
)

type EvidenceProtocol string

const (
	EvidenceProtocolNVMe EvidenceProtocol = "nvme"
	EvidenceProtocolSCSI EvidenceProtocol = "scsi"

	CollectionReasonTargetUnresolved  = "target_unresolved"
	CollectionReasonToolUnavailable   = "tool_unavailable"
	CollectionReasonTargetUnsupported = "target_unsupported"
	CollectionReasonTimeout           = "timeout"
	CollectionReasonExecError         = "exec_error"
	CollectionReasonOutputTooLarge    = "output_too_large"
	CollectionReasonParseError        = "parse_error"

	evidenceCooldown = 60 * time.Second
)

// EvidenceRequest describes one event-triggered health collection. Target is
// the command device name and metric key (for example "nvme0" or "sda"), not a
// path. An empty Target is accepted only as an unsupported attempt; the
// Trigger device is then used for de-duplication and error accounting.
type EvidenceRequest struct {
	Trigger     types.IOHealthEvent
	Target      string
	Protocol    EvidenceProtocol
	TriggeredAt time.Time
	Reason      string
}

// EvidenceResult is delivered exactly once for every request accepted by
// Submit. Event already contains collection_status and the bounded evidence.
// Reasons is de-duplicated and contains only the public bounded reason enum.
type EvidenceResult struct {
	Target      string
	TriggeredAt time.Time
	Event       types.IOHealthEvent
	Reasons     []string
}

type EvidenceWorkerOptions struct {
	OnResult func(EvidenceResult)
}

type queuedEvidenceRequest struct {
	request EvidenceRequest
	key     string
	target  string
}

// EvidenceWorker owns one serial external-command queue. In-flight and
// cooldown state admit at most one request per target, so repeated events for
// one disk cannot grow the queue. Serial execution intentionally avoids adding
// command load to several unhealthy storage paths at once. One worker lives
// for the collector run, so its state remains intact across BPF session
// retries.
type EvidenceWorker struct {
	onResult func(EvidenceResult)

	mu            sync.Mutex
	queue         []queuedEvidenceRequest
	inflight      map[string]struct{}
	cooldownUntil map[string]time.Time
	wake          chan struct{}
	done          chan struct{}
	started       bool
	stopped       bool
	ctx           context.Context

	now            func() time.Time
	lookupPath     func(string) (string, error)
	runCommand     commandRunner
	cooldown       time.Duration
	commandTimeout time.Duration
	maxOutputBytes int
}

func NewEvidenceWorker(options EvidenceWorkerOptions) *EvidenceWorker {
	onResult := options.OnResult
	if onResult == nil {
		onResult = func(EvidenceResult) {}
	}
	return &EvidenceWorker{
		onResult:       onResult,
		inflight:       make(map[string]struct{}),
		cooldownUntil:  make(map[string]time.Time),
		wake:           make(chan struct{}, 1),
		done:           make(chan struct{}),
		now:            time.Now,
		lookupPath:     exec.LookPath,
		runCommand:     runEvidenceCommand,
		cooldown:       evidenceCooldown,
		commandTimeout: evidenceCommandTimeout,
		maxOutputBytes: evidenceOutputLimit,
	}
}

// Start starts the worker goroutine. Repeated calls do not create additional
// workers.
func (w *EvidenceWorker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.ctx = ctx
	w.mu.Unlock()

	go w.loop(ctx)
}

// Submit is non-blocking and safe for concurrent perf-reader callers. It
// returns true only when this trigger becomes a new queued attempt.
func (w *EvidenceWorker) Submit(request EvidenceRequest) bool {
	request, target, key, ok := w.prepare(request)
	if !ok {
		return false
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started || w.stopped || w.ctx.Err() != nil {
		return false
	}
	if _, ok := w.inflight[key]; ok {
		return false
	}
	if w.now().Before(w.cooldownUntil[key]) {
		return false
	}

	w.inflight[key] = struct{}{}
	w.queue = append(w.queue, queuedEvidenceRequest{
		request: request,
		key:     key,
		target:  target,
	})
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return true
}

func (w *EvidenceWorker) Wait() {
	<-w.done
}

func (w *EvidenceWorker) loop(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
		close(w.done)
	}()

	for {
		if request, ok := w.take(); ok {
			result := w.collectEvidence(ctx, request.request, request.target)
			w.onResult(result)
			w.finish(request.key)
			continue
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			// No new submissions are accepted after cancellation. Requests
			// already accepted are drained with the cancelled context so each
			// still receives exactly one callback.
			if !w.hasQueued() {
				return
			}
		}
	}
}

func (w *EvidenceWorker) take() (queuedEvidenceRequest, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return queuedEvidenceRequest{}, false
	}

	request := w.queue[0]
	w.queue[0] = queuedEvidenceRequest{}
	w.queue = w.queue[1:]
	w.cooldownUntil[request.key] = w.now().Add(w.cooldown)
	return request, true
}

func (w *EvidenceWorker) finish(key string) {
	w.mu.Lock()
	delete(w.inflight, key)
	w.mu.Unlock()
}

func (w *EvidenceWorker) hasQueued() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue) != 0
}

func (w *EvidenceWorker) prepare(
	request EvidenceRequest,
) (EvidenceRequest, string, string, bool) {
	if request.Trigger.Device == "" {
		request.Trigger.Device = "unknown"
	}
	request.Trigger.CollectionStatus = ""
	request.Trigger.NVMe = nil
	request.Trigger.SCSI = nil

	if request.TriggeredAt.IsZero() {
		request.TriggeredAt = w.now()
	}
	if request.Reason != "" &&
		request.Reason != CollectionReasonTargetUnresolved &&
		request.Reason != CollectionReasonTargetUnsupported {
		return EvidenceRequest{}, "", "", false
	}

	target, validTarget := commandTarget(request.Target)
	if !validTarget {
		request.Target = ""
		if request.Reason == "" {
			request.Reason = CollectionReasonTargetUnresolved
		}
	} else {
		request.Target = target
	}
	if request.Reason == "" &&
		request.Protocol != EvidenceProtocolNVMe &&
		request.Protocol != EvidenceProtocolSCSI {
		request.Reason = CollectionReasonTargetUnsupported
	}

	resultTarget := request.Target
	if resultTarget == "" {
		resultTarget = request.Trigger.Device
	}
	key := resultTarget
	if strings.TrimSpace(key) == "" {
		key = "unknown"
		resultTarget = key
	}
	return request, resultTarget, key, true
}

func commandTarget(target string) (string, bool) {
	if target == "" || target == "." || target == ".." {
		return "", false
	}
	if strings.ContainsAny(target, `/\`) {
		return "", false
	}
	for _, r := range target {
		if r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return "", false
	}
	return target, true
}
