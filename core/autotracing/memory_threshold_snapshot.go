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

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
)

const (
	memoryThresholdSnapshotTracer = "memory_threshold_snapshot"
	arbitrationDelay              = 20 * time.Millisecond
)

type memoryThresholdSnapshot struct {
	captureOps  *actionBatchOps
	lastAttempt time.Time
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
	source, err := newCgroupSource()
	if err != nil {
		return handleWatchError(ctx, err)
	}
	watcher, err := newMemoryThresholdWatcher(ctx, memoryWatchOptions{
		ThresholdPercent: cfg.ThresholdPercent,
	})
	if err != nil {
		return handleWatchError(ctx, err)
	}
	subscription, err := pod.SubscribeContainers(ctx)
	if err != nil {
		return handleWatchError(ctx, errors.Join(err, watcher.Close()))
	}
	tracker := newCgroupTracker(watcher)
	log.Info("memory threshold snapshot watcher initialized")
	return handleWatchError(ctx, s.mainAction(ctx, config, source, watcher, tracker, subscription))
}

func handleWatchError(ctx context.Context, err error) error {
	if !isResourceExhaustion(err) && !errors.Is(err, errMemoryWatchEventOverflow) &&
		!errors.Is(err, pod.ErrContainerManagerDisabled) && !errors.Is(err, errCgroupRegistrationConflict) {
		return err
	}
	log.WithError(err).Error("memory threshold snapshot stopped; check container cgroup isolation, pod manager availability and host resources before restarting huatuo-bamai")
	// Terminal configuration and resource failures require operator recovery.
	<-ctx.Done()
	return nil
}

func (s *memoryThresholdSnapshot) mainAction(ctx context.Context,
	config *Config, source *cgroupSource, watcher *memoryThresholdWatcher, tracker *cgroupTracker, subscription *pod.ContainerSubscription,
) (retErr error) {
	defer func() {
		tracker.Close()
		// Preserve cleanup failures even when the caller canceled the run.
		if retErr == context.Canceled { //nolint:errorlint // Suppress only bare cancellation, preserving joined failures.
			retErr = nil
		}
		retErr = errors.Join(retErr, watcher.Close())
	}()
	actions := newActionRunner(ctx, config, source, s.captureOps, &s.lastAttempt)
	arbitration := time.NewTimer(time.Hour)
	arbitration.Stop()
	defer func() {
		arbitration.Stop()
		subscription.Close()
		actions.Close()
	}()

	var arbitrate <-chan time.Time
	var invalidated []memoryWatchRegistrationID
	isUnavailable := false

	processEvents := func() error {
		events, err := watcher.ProcessEvents(ctx)
		if err != nil {
			return err
		}
		containers, err := subscription.DrainEvents()
		if err != nil && !errors.Is(err, pod.ErrContainersUnavailable) {
			return err
		}
		if err != nil {
			if !isUnavailable {
				log.WithError(err).Warn("memory snapshot waiting for a complete container view")
			}
			isUnavailable = true
			actions.Cancel()
		} else {
			isUnavailable = false
		}
		update, err := tracker.ProcessContainerEvents(ctx, containers)
		if err != nil {
			return err
		}
		// Preserve lifecycle IDs before memory events reuse the tracker buffer.
		invalidated = append(invalidated[:0], update.Invalidated...)

		options := memoryEventOptions{
			AcceptPending: !isUnavailable && len(events) != 0 && actions.Allowed(time.Now()),
		}
		update, err = tracker.ProcessMemoryEvents(ctx, events, options)
		if err != nil {
			return err
		}
		invalidated = append(invalidated, update.Invalidated...)
		actions.CancelIfInvalidated(invalidated)

		if isUnavailable {
			tracker.ClearPending()
		}
		return nil
	}
	if err := processEvents(); err != nil {
		return err
	}
	for {
		if tracker.PendingCount() != 0 && arbitrate == nil && actions.Done() == nil {
			arbitration.Reset(arbitrationDelay)
			arbitrate = arbitration.C
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-arbitrate:
			arbitrate = nil
			targets := tracker.DrainPending()
			if len(targets) != 0 {
				if err := actions.Submit(targets); err != nil {
					return err
				}
			}
		case <-actions.Done():
			actions.Finish()
			if !actions.Allowed(time.Now()) {
				tracker.ClearPending()
			}
		case <-watcher.Notifications():
			if err := processEvents(); err != nil {
				return err
			}
		case <-subscription.Notify():
			if err := processEvents(); err != nil {
				return err
			}
		}
	}
}
