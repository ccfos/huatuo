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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const targetSelectionTimeout = time.Second

var (
	errActionRunnerBusy   = errors.New("memory snapshot action is active; finish it before submitting another batch")
	errActionRunnerClosed = errors.New("memory snapshot action runner is closed")
)

// actionRunner methods belong to the scheduler goroutine. The worker publishes
// finished through done; Close also synchronizes with worker exit.
type actionRunner struct {
	config      *Config
	source      *cgroupSource
	ops         *actionBatchOps
	lastAttempt *time.Time // Snapshot-owned so cooldown survives runner replacement.
	interval    time.Duration
	ctx         context.Context
	stop        context.CancelFunc
	jobs        chan actionBatch
	done        chan struct{}
	workerDone  chan struct{}
	active      map[memoryWatchRegistrationID]struct{}
	batchCancel context.CancelFunc
	finished    time.Time
	isClosed    bool
}

func newActionRunner(ctx context.Context, config *Config, source *cgroupSource,
	ops *actionBatchOps, lastAttempt *time.Time,
) *actionRunner {
	if ops == nil {
		ops = &actionBatchOps{
			selectTarget: selectTarget, validate: validateTarget,
			collect: collector.Run, save: tracing.Save, validateContainer: pod.ValidateContainerRef,
		}
	}

	ctx, stop := context.WithCancel(ctx)
	r := &actionRunner{
		config: config, source: source, ops: ops,
		lastAttempt: lastAttempt,
		interval:    time.Duration(config.MemoryThresholdSnapshot.IntervalTracing) * time.Second,
		ctx:         ctx, stop: stop,
		jobs:       make(chan actionBatch, 1),
		done:       make(chan struct{}, 1),
		workerDone: make(chan struct{}),
		active:     make(map[memoryWatchRegistrationID]struct{}),
	}
	go r.runActionBatches(ctx)
	return r
}

// Allowed checks cooldown; Submit separately enforces one active batch.
func (r *actionRunner) Allowed(now time.Time) bool {
	return r.lastAttempt.IsZero() || now.Sub(*r.lastAttempt) >= r.interval
}

// Submit takes ownership of targets on success and never waits for capture.
func (r *actionRunner) Submit(targets []memoryEventObservation) error {
	if r.isClosed {
		return errActionRunnerClosed
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.batchCancel != nil {
		return errActionRunnerBusy
	}

	ctx, cancel := context.WithCancel(r.ctx)
	r.batchCancel = cancel
	r.finished = time.Time{}
	for i := range targets {
		r.active[targets[i].RegistrationID] = struct{}{}
	}
	batch := newActionBatch(ctx, r.config, r.source, targets, r.ops)
	// Finish must acknowledge the previous batch, so the one-slot queue is empty.
	r.jobs <- batch
	return nil
}

// Done is nil while idle; cancellation keeps the batch active until Finish.
func (r *actionRunner) Done() <-chan struct{} {
	if r.batchCancel == nil {
		return nil
	}
	return r.done
}

// Finish follows a receive from Done, or worker exit in Close.
func (r *actionRunner) Finish() time.Time {
	if r.batchCancel == nil {
		return time.Time{}
	}
	r.batchCancel()
	r.batchCancel = nil
	clear(r.active)
	if !r.finished.IsZero() {
		*r.lastAttempt = r.finished
	}
	return r.finished
}

func (r *actionRunner) CancelIfInvalidated(ids []memoryWatchRegistrationID) {
	for _, id := range ids {
		if _, exists := r.active[id]; exists {
			r.Cancel()
			return
		}
	}
}

func (r *actionRunner) Cancel() {
	if r.batchCancel != nil {
		r.batchCancel()
	}
}

// Close joins the worker and returns any completion not acknowledged by Finish.
func (r *actionRunner) Close() time.Time {
	if r.isClosed {
		return time.Time{}
	}
	r.isClosed = true
	r.stop()
	r.Cancel()
	<-r.workerDone
	// Cancellation may have stopped the worker before it received the batch.
	r.jobs = nil
	return r.Finish()
}

func (r *actionRunner) runActionBatches(ctx context.Context) {
	defer close(r.workerDone)
	for {
		select {
		case <-ctx.Done():
			return
		case batch := <-r.jobs:
			r.finished = batch.Run()
			// The single active batch leaves room even if the scheduler is exiting.
			r.done <- struct{}{}
		}
	}
}

// actionBatch is a single-use job; its context and dependencies are fixed before enqueue.
type actionBatch struct {
	ctx     context.Context
	config  *Config
	source  *cgroupSource
	targets []memoryEventObservation
	ops     *actionBatchOps
}

func newActionBatch(ctx context.Context, config *Config, source *cgroupSource,
	targets []memoryEventObservation, ops *actionBatchOps,
) actionBatch {
	return actionBatch{ctx: ctx, config: config, source: source, targets: targets, ops: ops}
}

type memcgCandidate struct {
	observation *memoryEventObservation // Borrows the batch's immutable observations.
	current     uint64
	max         uint64
	ratio       float64
}

// Victim fields retain their persisted JSON names for existing consumers.
// They describe the selected capture target, which may never be killed.
type memoryThresholdSnapshotData struct {
	CgroupPath         string                     `json:"cgroup_path"`
	MemoryCurrent      uint64                     `json:"memory_current"`
	MemoryMax          uint64                     `json:"memory_max"`
	MemoryUsagePercent float64                    `json:"memory_usage_percent"`
	VictimPID          int                        `json:"victim_pid"`
	VictimProcessName  string                     `json:"victim_process_name"`
	VictimOOMScoreAdj  int                        `json:"victim_oom_score_adj"`
	Language           memsnapshot.Language       `json:"language"`
	Snapshot           *memsnapshot.Snapshot      `json:"snapshot"`
	ProcessMemory      *memsnapshot.ProcessMemory `json:"process_memory"`
}

// Run returns zero for skipped or canceled attempts, so they do not start cooldown.
func (b *actionBatch) Run() time.Time {
	ctx := b.ctx
	ordered, err := b.rankCgroupTargets(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.WithError(err).Debug("memory threshold snapshot pressure event skipped")
		}
		return time.Time{}
	}
	if len(ordered) == 0 {
		return time.Time{}
	}

	for i := 1; i < len(ordered); i++ {
		c := &ordered[i]
		log.WithField("rank", i+1).WithFields(logrus.Fields{
			"container":     c.observation.Container.Key.ID,
			"cgroup":        c.observation.Cgroup.Path,
			"usage_bytes":   c.current,
			"limit_bytes":   c.max,
			"usage_percent": c.ratio * 100,
		}).Info("memory threshold snapshot candidate not selected")
	}

	c := &ordered[0]
	if c.ratio < float64(b.config.MemoryThresholdSnapshot.ThresholdPercent)/100 {
		return time.Time{}
	}
	err = b.captureCandidate(ctx, c)
	if errors.Is(err, context.Canceled) {
		return time.Time{}
	}
	if err != nil {
		log.WithField("cgroup", c.observation.Cgroup.Path).
			WithError(err).Warn("memory threshold snapshot skipped")
	}
	return time.Now()
}

// rankCgroupTargets retains below-threshold targets so logs include the full ranking.
func (b *actionBatch) rankCgroupTargets(ctx context.Context) ([]memcgCandidate, error) {
	var candidates []memcgCandidate
	var lastErr error
	for i := range b.targets {
		observation := &b.targets[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		usage, err := b.source.ReadMemory(ctx, observation.Cgroup)
		if err != nil {
			lastErr = err
			continue
		}
		// The limit can become unlimited after the threshold notification was queued.
		if cgroups.IsMemoryLimitUnlimited(usage.MaxLimited) {
			continue
		}
		if candidates == nil {
			candidates = make([]memcgCandidate, 0, len(b.targets))
		}
		candidates = append(candidates, memcgCandidate{
			observation: observation, current: usage.Usage, max: usage.MaxLimited,
			ratio: float64(usage.Usage) / float64(usage.MaxLimited),
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, lastErr
	}

	slices.SortFunc(candidates, func(a, b memcgCandidate) int {
		if order := cmp.Compare(b.ratio, a.ratio); order != 0 {
			return order
		}
		if order := cmp.Compare(b.current, a.current); order != 0 {
			return order
		}
		return cmp.Compare(a.observation.Cgroup.Path, b.observation.Cgroup.Path)
	})
	return candidates, nil
}

type actionBatchOps struct {
	selectTarget      func(context.Context, string, uint64) (targetCandidate, error)
	validateContainer func(pod.ContainerRef) error
	validate          func(context.Context, string, memsnapshot.ProcessIdentity) error
	collect           func(context.Context, int, collector.Options) (*collector.Result, error)
	save              func(*tracing.WriteRequest) error
}

func (b *actionBatch) captureCandidate(ctx context.Context, candidate *memcgCandidate) (retErr error) {
	cfg := &b.config.MemoryThresholdSnapshot
	observation := candidate.observation
	started := time.Now()
	log.WithField("cgroup", observation.Cgroup.Path).
		WithField("usage_bytes", candidate.current).
		WithField("limit_bytes", candidate.max).
		WithField("usage_percent", candidate.ratio*100).
		WithField("threshold_percent", cfg.ThresholdPercent).
		Info("memory threshold snapshot capture started")
	defer func() {
		log.WithField("container", observation.Container.Key.ID).
			WithField("cgroup", observation.Cgroup.Path).
			WithField("elapsed_ms", time.Since(started).Milliseconds()).
			WithError(retErr).
			Info("memory threshold snapshot capture finished")
	}()
	ops := b.ops
	if err := b.source.Validate(ctx, observation.Cgroup); err != nil {
		return err
	}
	if err := ops.validateContainer(observation.Container); err != nil {
		return err
	}
	selectionCtx, cancelSelection := context.WithTimeout(ctx, targetSelectionTimeout)
	defer cancelSelection()
	selectionStarted := time.Now()
	log.WithField("cgroup", observation.Cgroup.Path).
		WithField("timeout_ms", targetSelectionTimeout.Milliseconds()).
		Info("memory threshold snapshot target selection started")
	target, err := ops.selectTarget(selectionCtx, observation.Cgroup.Path, candidate.max)
	if err != nil {
		return fmt.Errorf("select target: %w", err)
	}
	checkTarget := func(checkCtx context.Context, identity memsnapshot.ProcessIdentity) error {
		if err := b.source.Validate(checkCtx, observation.Cgroup); err != nil {
			return err
		}
		if err := ops.validateContainer(observation.Container); err != nil {
			return err
		}
		return ops.validate(checkCtx, observation.Cgroup.Path, identity)
	}
	err = checkTarget(selectionCtx, target.identity)
	cancelSelection()
	log.WithField("cgroup", observation.Cgroup.Path).
		WithField("container", observation.Container.Key.ID).
		WithField("pid", target.pid).
		WithField("start_time_ticks", target.identity.StartTimeTicks).
		WithField("elapsed_ms", time.Since(selectionStarted).Milliseconds()).
		WithError(err).
		Info("memory threshold snapshot target selection finished")
	if err != nil {
		return fmt.Errorf("validate capture target: %w", err)
	}
	captureTimeout := time.Duration(cfg.RunTracingToolTimeout) * time.Second
	_, err = ops.collect(ctx, target.pid, collector.Options{
		ExpectedIdentity: &target.identity,
		TopK:             cfg.MaxMemoryObjectEntries,
		GoTimeout:        captureTimeout,
		JavaTimeout:      captureTimeout,
		PythonTimeout:    captureTimeout,
		CheckTarget:      checkTarget,
		Save: func(saveCtx context.Context, result *collector.Result) error {
			if err := checkTarget(saveCtx, target.identity); err != nil {
				return err
			}
			if err := saveCtx.Err(); err != nil {
				return err
			}
			return ops.save(&tracing.WriteRequest{
				TracerName: memoryThresholdSnapshotTracer, ContainerID: observation.Container.Key.ID,
				TracerRunType:     types.TracerRunTypeAutotracing,
				StartedTimestamp:  timeutil.Timestamp{Time: started.UTC()},
				ObservedTimestamp: timeutil.Timestamp{Time: result.CaptureTime},
				TracerData: &memoryThresholdSnapshotData{
					CgroupPath:    observation.Cgroup.Path,
					MemoryCurrent: candidate.current, MemoryMax: candidate.max,
					MemoryUsagePercent: candidate.ratio * 100,
					VictimPID:          target.pid, VictimProcessName: target.comm,
					VictimOOMScoreAdj: target.oomScoreAdj, Language: result.Language,
					Snapshot: result.Snapshot, ProcessMemory: result.ProcessMemory,
				},
			})
		},
	})
	return err
}
