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

// Evidence requests share one bounded serial queue while target generations
// prevent replacement devices from inheriting old suppression state.
package iohealth

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/pkg/types"
)

type EvidenceProtocol string

const (
	EvidenceProtocolNVMe EvidenceProtocol = "nvme"
	EvidenceProtocolSCSI EvidenceProtocol = "scsi"

	CollectionReasonTargetUnresolved  = "target_unresolved"
	CollectionReasonTargetChanged     = "target_changed"
	CollectionReasonToolUnavailable   = "tool_unavailable"
	CollectionReasonTargetUnsupported = "target_unsupported"
	CollectionReasonTimeout           = "timeout"
	CollectionReasonExecError         = "exec_error"
	CollectionReasonOutputTooLarge    = "output_too_large"
	CollectionReasonParseError        = "parse_error"

	evidenceCooldown      = 60 * time.Second
	evidenceQueueCapacity = 1024
)

// EvidenceRequest describes one event-triggered health collection. Target is
// the command device name and metric key (for example "nvme0" or "sda"), not a
// path. An empty Target is accepted only as an unsupported attempt; the
// Trigger device is then used for de-duplication and error accounting.
type EvidenceRequest struct {
	Trigger     types.IOHealthEvent
	Target      string
	Identity    string
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
	OnResult         func(EvidenceResult)
	ValidateIdentity func(EvidenceRequest) bool
}

type evidenceRequestKey struct {
	target   string
	identity string
}

type evidenceCooldownState struct {
	key   evidenceRequestKey
	until time.Time
}

type queuedEvidenceRequest struct {
	request EvidenceRequest
	key     evidenceRequestKey
}

// EvidenceWorker owns one serial external-command queue. In-flight and
// cooldown state admit at most one request per target generation. A new device
// generation does not inherit the old generation's suppression state, while a
// fixed queue capacity bounds a hotplug storm. Serial execution intentionally
// avoids adding command load to several unhealthy storage paths at once. One
// worker lives for the collector run, so its state remains intact across BPF
// session retries.
type EvidenceWorker struct {
	onResult         func(EvidenceResult)
	validateIdentity func(EvidenceRequest) bool

	mu            sync.Mutex
	queue         []queuedEvidenceRequest
	inflight      map[evidenceRequestKey]struct{}
	cooldownUntil map[string]evidenceCooldownState
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
	queueCapacity  int
}

func NewEvidenceWorker(options EvidenceWorkerOptions) *EvidenceWorker {
	onResult := options.OnResult
	if onResult == nil {
		onResult = func(EvidenceResult) {}
	}
	validateIdentity := options.ValidateIdentity
	if validateIdentity == nil {
		validateIdentity = func(EvidenceRequest) bool { return true }
	}
	return &EvidenceWorker{
		onResult:         onResult,
		validateIdentity: validateIdentity,
		inflight:         make(map[evidenceRequestKey]struct{}),
		cooldownUntil:    make(map[string]evidenceCooldownState),
		wake:             make(chan struct{}, 1),
		done:             make(chan struct{}),
		now:              time.Now,
		lookupPath:       exec.LookPath,
		runCommand:       runEvidenceCommand,
		cooldown:         evidenceCooldown,
		commandTimeout:   evidenceCommandTimeout,
		maxOutputBytes:   evidenceOutputLimit,
		queueCapacity:    evidenceQueueCapacity,
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
//
//nolint:gocritic // Copy the request before normalizing and queueing it.
func (w *EvidenceWorker) Submit(request EvidenceRequest) bool {
	request, target, key, ok := w.prepare(&request)
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
	if cooldown, ok := w.cooldownUntil[target]; ok &&
		cooldown.key == key && w.now().Before(cooldown.until) {
		return false
	}
	if len(w.queue) >= w.queueCapacity {
		return false
	}

	w.inflight[key] = struct{}{}
	w.queue = append(w.queue, queuedEvidenceRequest{
		request: request,
		key:     key,
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
			result, applyCooldown := w.collectEvidence(
				ctx,
				request.request,
				request.key.target,
			)
			w.onResult(result)
			w.finish(&request, applyCooldown)
			continue
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			// No new submissions are accepted after cancellation. Requests
			// already accepted are drained with the canceled context so each
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
	return request, true
}

func (w *EvidenceWorker) finish(
	request *queuedEvidenceRequest,
	applyCooldown bool,
) {
	w.mu.Lock()
	delete(w.inflight, request.key)
	if applyCooldown {
		now := w.now()
		current, exists := w.cooldownUntil[request.key.target]
		// An unresolved request has no generation identity. Keep reporting it,
		// but do not let it displace an active, verified generation.
		if request.key.identity != "" || !exists || !now.Before(current.until) {
			w.cooldownUntil[request.key.target] = evidenceCooldownState{
				key:   request.key,
				until: now.Add(w.cooldown),
			}
		}
	}
	w.mu.Unlock()
}

func (w *EvidenceWorker) hasQueued() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue) != 0
}

func (w *EvidenceWorker) prepare(
	request *EvidenceRequest,
) (EvidenceRequest, string, evidenceRequestKey, bool) {
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
		return EvidenceRequest{}, "", evidenceRequestKey{}, false
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
	if strings.TrimSpace(resultTarget) == "" {
		resultTarget = "unknown"
	}
	key := evidenceRequestKey{target: resultTarget, identity: request.Identity}
	return *request, resultTarget, key, true
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
