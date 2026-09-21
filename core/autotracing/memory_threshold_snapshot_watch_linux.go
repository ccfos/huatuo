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
	"os"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
)

func isResourceExhaustion(err error) bool {
	return errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) ||
		errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.ENOMEM) ||
		errors.Is(err, memorywatch.ErrEventOverflow)
}

func handleWatchError(ctx context.Context, err error) error {
	if !isResourceExhaustion(err) {
		return err
	}
	if errors.Is(err, memorywatch.ErrEventOverflow) {
		log.WithError(err).Error("memory threshold snapshot stopped after event queue overflow; check event consumption and container churn before restarting huatuo-bamai")
	} else {
		log.WithError(err).Error("memory threshold snapshot stopped after resource exhaustion; increase process FD/inotify limits and restart huatuo-bamai to re-enable it")
	}
	// Avoid repeatedly rebuilding and scanning the hierarchy while exhausted.
	<-ctx.Done()
	return nil
}

type watchedCgroup struct {
	containerID string
	cgroupPath  string
	targetID    memorywatch.TargetID
	identity    os.FileInfo
}

type memoryPressureEvent struct {
	containerID string
	cgroupPath  string
}

type pressureWatcher struct {
	watcher           *memorywatch.Watcher
	root              string
	cgroups           map[string]*watchedCgroup
	targets           map[memorywatch.TargetID]*watchedCgroup
	containers        map[string]string
	pending           map[string]memoryPressureEvent
	lifecycle         *pod.MemoryCgroupSubscription
	changes           <-chan pod.MemoryCgroupChange
	wake              chan struct{}
	containerPath     func(string) (string, error)
	limitReported     bool
	recoveryDue       time.Time
	recoveryRequested bool
	recoveryAttempts  int
}

func newPressureWatcher(thresholdPercent int) (*pressureWatcher, error) {
	root, err := cgroups.MemoryRoot()
	if err != nil {
		return nil, err
	}
	watcher, err := memorywatch.New(memorywatch.Options{
		ThresholdPercent: thresholdPercent, MaxCgroups: maxWatchedCgroups,
	})
	if err != nil {
		return nil, err
	}
	w := &pressureWatcher{
		watcher: watcher, root: root,
		cgroups:    make(map[string]*watchedCgroup),
		targets:    make(map[memorywatch.TargetID]*watchedCgroup),
		containers: make(map[string]string),
		pending:    make(map[string]memoryPressureEvent),
		wake:       make(chan struct{}, 1), containerPath: pod.ContainerMemoryCgroupPathByID,
	}
	w.lifecycle, err = pod.SubscribeMemoryCgroups(w.signal)
	if err != nil {
		w.close()
		return nil, fmt.Errorf("subscribe memory cgroup lifecycle: %w", err)
	}
	w.changes = w.lifecycle.Changes()
	return w, nil
}

func (w *pressureWatcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *pressureWatcher) Run(ctx context.Context) (<-chan memoryPressureEvent, <-chan error) {
	events := make(chan memoryPressureEvent, 1)
	done := make(chan error, 1)
	go func() {
		runCtx, cancel := context.WithCancel(ctx)
		observations := make(chan memorywatch.Event, 1)
		readerDone := make(chan error, 1)
		go func() {
			err := w.readEvents(runCtx, observations)
			close(observations)
			readerDone <- err
			close(readerDone)
		}()
		err := w.loop(runCtx, events, observations, readerDone)
		cancel()
		w.close()
		<-readerDone
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		done <- err
		close(events)
		close(done)
	}()
	return events, done
}

func (w *pressureWatcher) readEvents(ctx context.Context, output chan<- memorywatch.Event) error {
	var events [64]memorywatch.Event
	for {
		n, err := w.watcher.ReadEvents(ctx, events[:])
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			select {
			case output <- events[i]:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (w *pressureWatcher) loop(ctx context.Context, events chan<- memoryPressureEvent,
	observations <-chan memorywatch.Event, readerDone <-chan error,
) error {
	if err := w.refreshFromCgroupTree(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		if w.lifecycle != nil && w.lifecycle.TakeResync() {
			w.requestRecovery()
		}
		var retry <-chan time.Time
		if !w.recoveryDue.IsZero() {
			timer.Reset(max(0, time.Until(w.recoveryDue)))
			retry = timer.C
		} else {
			timer.Stop()
		}
		var output chan<- memoryPressureEvent
		var next memoryPressureEvent
		for _, event := range w.pending {
			next, output = event, events
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case output <- next:
			delete(w.pending, next.cgroupPath)
		case <-w.wake:
		case change := <-w.changes:
			if err := w.handleCgroupChange(ctx, change); err != nil {
				return err
			}
		case <-retry:
			if err := w.recoverWatches(ctx); err != nil {
				return err
			}
		case event, ok := <-observations:
			if !ok {
				return <-readerDone
			}
			if err := w.handleMemoryEvent(ctx, &event); err != nil {
				return err
			}
		}
	}
}

func (w *pressureWatcher) handleMemoryEvent(ctx context.Context, event *memorywatch.Event) error {
	entry := w.targets[event.TargetID]
	if entry == nil {
		return nil
	}
	switch event.Kind {
	case memorywatch.ThresholdObserved:
		w.pending[entry.cgroupPath] = memoryPressureEvent{
			containerID: entry.containerID, cgroupPath: entry.cgroupPath,
		}
	case memorywatch.TargetRemoved:
		if err := w.removeCgroup(ctx, entry.cgroupPath); err != nil {
			return err
		}
		w.requestRecovery()
	case memorywatch.TargetUnavailable:
		if isResourceExhaustion(event.Err) {
			return event.Err
		}
		log.WithField("cgroup", entry.cgroupPath).WithError(event.Err).
			Debug("memory threshold snapshot target unavailable")
		if !errors.Is(event.Err, memorywatch.ErrLimitUnavailable) {
			if err := w.removeCgroup(ctx, entry.cgroupPath); err != nil {
				return err
			}
			w.requestRecovery()
		}
	}
	return nil
}

func (w *pressureWatcher) addCgroup(ctx context.Context, containerID, path string) error {
	if len(w.cgroups) >= maxWatchedCgroups {
		return errCgroupWatchLimit
	}
	info, err := os.Lstat(w.memcgDir(path))
	if err != nil {
		return err
	}
	id, err := w.watcher.Add(ctx, path)
	if errors.Is(err, memorywatch.ErrTargetLimit) {
		return errCgroupWatchLimit
	}
	if err != nil {
		return err
	}
	entry := &watchedCgroup{containerID: containerID, cgroupPath: path, targetID: id, identity: info}
	w.cgroups[path], w.targets[id], w.containers[containerID] = entry, entry, path
	return nil
}

func (w *pressureWatcher) removeCgroup(ctx context.Context, path string) error {
	entry := w.cgroups[path]
	if entry == nil {
		return nil
	}
	if err := w.watcher.Remove(ctx, entry.targetID); err != nil {
		return err
	}
	delete(w.cgroups, path)
	delete(w.targets, entry.targetID)
	if w.containers[entry.containerID] == path {
		delete(w.containers, entry.containerID)
	}
	delete(w.pending, path)
	return nil
}

func (w *pressureWatcher) cgroupPathForContainer(id string) (string, bool) {
	path, ok := w.containers[id]
	return path, ok
}

func (w *pressureWatcher) close() {
	if w.lifecycle != nil {
		w.lifecycle.Close()
	}
	_ = w.watcher.Close()
}

func (w *pressureWatcher) memcgDir(path string) string {
	return filepath.Join(w.root, path)
}
