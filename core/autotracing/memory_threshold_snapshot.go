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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	memoryThresholdSnapshotTracer = "memory_threshold_snapshot"
	arbitrationDelay              = 20 * time.Millisecond
	targetSelectionTimeout        = time.Second
)

type memoryThresholdSnapshot struct {
	captureOps  *memoryThresholdSnapshotOps
	cgroup      cgroups.Cgroup
	lastAttempt time.Time
}

type memcgCandidate struct {
	containerID string
	cgroupPath  string
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

func init() {
	tracing.RegisterEventTracing(memoryThresholdSnapshotTracer, newMemoryThresholdSnapshot)
}

func newMemoryThresholdSnapshot() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryThresholdSnapshot{},
		Interval:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (s *memoryThresholdSnapshot) Start(ctx context.Context) (retErr error) {
	if err := ctx.Err(); err != nil {
		return nil
	}
	config := configSnapshot()
	cfg := &config.MemoryThresholdSnapshot
	log.WithField("threshold_percent", cfg.ThresholdPercent).
		WithField("interval_tracing_seconds", cfg.IntervalTracing).
		WithField("max_memory_object_entries", cfg.MaxMemoryObjectEntries).
		WithField("run_tracing_tool_timeout_seconds", cfg.RunTracingToolTimeout).
		Info("memory threshold snapshot watcher starting")
	defer func() {
		log.WithError(retErr).
			WithField("context_error", ctx.Err()).
			Info("memory threshold snapshot watcher stopped")
	}()
	if err := validateMemoryThresholdSnapshotConfig(config); err != nil {
		return fmt.Errorf("invalid memory threshold snapshot config: %w", err)
	}
	if s.cgroup == nil {
		cgroup, err := cgroups.NewManager()
		if err != nil {
			return fmt.Errorf("create cgroup manager: %w", err)
		}
		s.cgroup = cgroup
	}
	watcher, err := newPressureWatcher(s.cgroup, cfg.ThresholdPercent)
	if err != nil {
		return handleWatchError(ctx, err)
	}
	log.WithField("cgroup_mode", cgroups.CgroupMode()).
		Info("memory threshold snapshot watcher initialized")
	return handleWatchError(ctx, s.watchAndCapture(ctx, config, watcher))
}

func (s *memoryThresholdSnapshot) watchAndCapture(ctx context.Context,
	config *Config, watcher *pressureWatcher,
) error {
	watchCtx, cancel := context.WithCancel(ctx)
	events, watcherDone := watcher.Run(watchCtx)
	defer func() {
		cancel()
		// A restart must not overlap the previous watcher's FD ownership.
		<-watcherDone
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcherDone:
			return err
		case event, ok := <-events:
			if !ok {
				return <-watcherDone
			}
			now := time.Now()
			if !s.captureAllowed(config, now) {
				continue
			}
			candidate, ok, err := s.bestCaptureCandidate(ctx, config, events, event)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				log.WithError(err).
					Debug("memory threshold snapshot pressure event skipped")
				continue
			}
			if !ok {
				continue
			}
			err = s.captureCandidate(ctx, config, &candidate)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// Every completed attempt starts the same node-wide tracing interval.
			s.lastAttempt = time.Now()
			if err != nil {
				log.WithField("cgroup", candidate.cgroupPath).
					WithError(err).
					Warn("memory threshold snapshot skipped")
			}
		}
	}
}

func (s *memoryThresholdSnapshot) captureAllowed(config *Config,
	now time.Time,
) bool {
	interval := time.Duration(config.MemoryThresholdSnapshot.IntervalTracing) * time.Second
	return s.lastAttempt.IsZero() || now.Sub(s.lastAttempt) >= interval
}

func (s *memoryThresholdSnapshot) bestCaptureCandidate(ctx context.Context,
	config *Config, events <-chan memoryPressureEvent,
	first memoryPressureEvent,
) (memcgCandidate, bool, error) {
	pending := map[string]memoryPressureEvent{first.cgroupPath: first}
	timer := time.NewTimer(arbitrationDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return memcgCandidate{}, false, ctx.Err()
		case event, ok := <-events:
			if !ok {
				return s.highestPressureCandidate(config, pending)
			}
			pending[event.cgroupPath] = event
		case <-timer.C:
			return s.highestPressureCandidate(config, pending)
		}
	}
}

func (s *memoryThresholdSnapshot) highestPressureCandidate(config *Config,
	events map[string]memoryPressureEvent,
) (memcgCandidate, bool, error) {
	var selected memcgCandidate
	var lastErr error
	found := false
	for _, event := range events {
		candidate, ok, err := s.pressureCandidate(event)
		if err != nil {
			lastErr = err
			continue
		}
		if !ok || candidate.containerID == "" || isUnlimitedLimit(candidate.max) {
			continue
		}
		candidate.ratio = float64(candidate.current) / float64(candidate.max)
		if candidate.ratio < float64(config.MemoryThresholdSnapshot.ThresholdPercent)/100 {
			continue
		}
		if !found || higherPressure(candidate, selected) {
			selected = candidate
			found = true
		}
	}
	if found {
		return selected, true, nil
	}
	return memcgCandidate{}, false, lastErr
}

func higherPressure(candidate, selected memcgCandidate) bool {
	if candidate.ratio != selected.ratio {
		return candidate.ratio > selected.ratio
	}
	if candidate.current != selected.current {
		return candidate.current > selected.current
	}
	return candidate.cgroupPath < selected.cgroupPath
}

func (s *memoryThresholdSnapshot) pressureCandidate(
	event memoryPressureEvent,
) (memcgCandidate, bool, error) {
	usage, err := s.cgroup.MemoryUsage(event.cgroupPath)
	if err != nil {
		return memcgCandidate{}, false, fmt.Errorf("read cgroup memory usage: %w", err)
	}
	if usage == nil {
		return memcgCandidate{}, false, nil
	}
	return memcgCandidate{
		containerID: event.containerID, cgroupPath: event.cgroupPath,
		current: usage.Usage, max: usage.MaxLimited,
	}, true, nil
}

type memoryThresholdSnapshotOps struct {
	selectTarget  func(context.Context, string, uint64) (targetCandidate, error)
	validate      func(context.Context, string, memsnapshot.ProcessIdentity) error
	collect       func(context.Context, int, collector.Options) (*collector.Result, error)
	save          func(*tracing.WriteRequest) error
	containerPath func(string) (string, error)
}

func (s *memoryThresholdSnapshot) captureCandidate(ctx context.Context,
	config *Config, candidate *memcgCandidate,
) (retErr error) {
	cfg := &config.MemoryThresholdSnapshot
	started := time.Now()
	log.WithField("container", candidate.containerID).
		WithField("cgroup", candidate.cgroupPath).
		WithField("usage_bytes", candidate.current).
		WithField("limit_bytes", candidate.max).
		WithField("usage_percent", candidate.ratio*100).
		WithField("threshold_percent", cfg.ThresholdPercent).
		Info("memory threshold snapshot capture started")
	defer func() {
		log.WithField("container", candidate.containerID).
			WithField("cgroup", candidate.cgroupPath).
			WithField("elapsed_ms", time.Since(started).Milliseconds()).
			WithError(retErr).
			Info("memory threshold snapshot capture finished")
	}()
	ops := s.captureOps
	if ops == nil {
		ops = &memoryThresholdSnapshotOps{
			selectTarget: selectTarget, validate: validateTarget,
			collect: collector.Run,
			save:    tracing.Save, containerPath: knownContainerCgroupPath,
		}
	}
	validateContainer := func() error {
		return validateContainerCgroup(candidate.containerID, candidate.cgroupPath, ops.containerPath)
	}
	if err := validateContainer(); err != nil {
		return err
	}
	selectionCtx, cancelSelection := context.WithTimeout(ctx, targetSelectionTimeout)
	selectionStarted := time.Now()
	log.WithField("container", candidate.containerID).
		WithField("timeout_ms", targetSelectionTimeout.Milliseconds()).
		Info("memory threshold snapshot target selection started")
	target, err := ops.selectTarget(selectionCtx, candidate.cgroupPath, candidate.max)
	cancelSelection()
	log.WithField("container", candidate.containerID).
		WithField("pid", target.pid).
		WithField("start_time_ticks", target.identity.StartTimeTicks).
		WithField("elapsed_ms", time.Since(selectionStarted).Milliseconds()).
		WithError(err).
		Info("memory threshold snapshot target selection finished")
	if err != nil {
		return fmt.Errorf("select target: %w", err)
	}
	captureTimeout := time.Duration(cfg.RunTracingToolTimeout) * time.Second
	_, err = ops.collect(ctx, target.pid, collector.Options{
		ExpectedIdentity: &target.identity,
		TopK:             cfg.MaxMemoryObjectEntries,
		GoTimeout:        captureTimeout,
		JavaTimeout:      captureTimeout,
		PythonTimeout:    captureTimeout,
		CheckTarget: func(checkCtx context.Context, identity memsnapshot.ProcessIdentity) error {
			return ops.validate(checkCtx, candidate.cgroupPath, identity)
		},
		Save: func(saveCtx context.Context, result *collector.Result) error {
			if err := validateContainer(); err != nil {
				return err
			}
			if err := saveCtx.Err(); err != nil {
				return err
			}
			return ops.save(&tracing.WriteRequest{
				TracerName: memoryThresholdSnapshotTracer, ContainerID: candidate.containerID,
				TracerRunType:     types.TracerRunTypeAutotracing,
				StartedTimestamp:  timeutil.Timestamp{Time: started.UTC()},
				ObservedTimestamp: timeutil.Timestamp{Time: result.CaptureTime},
				TracerData: &memoryThresholdSnapshotData{
					CgroupPath:    candidate.cgroupPath,
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
