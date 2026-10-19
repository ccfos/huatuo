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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestActionRunnerCompletionGatesSubmission(t *testing.T) {
	runner := newActionRunner(t.Context(), &Config{}, nil, nil, new(time.Time))
	t.Cleanup(func() { runner.Close() })
	if runner.Done() != nil || !runner.Finish().IsZero() {
		t.Fatal("new runner is not idle")
	}
	runner.Cancel()
	runner.CancelIfInvalidated([]memoryWatchRegistrationID{1})
	for range 2 {
		if err := runner.Submit(nil); err != nil {
			t.Fatal(err)
		}
		if err := runner.Submit(nil); !errors.Is(err, errActionRunnerBusy) {
			t.Fatalf("second submission = %v, want busy", err)
		}
		waitActionRunnerForTest(t, runner)
		if err := runner.Submit(nil); !errors.Is(err, errActionRunnerBusy) {
			t.Fatalf("submission before Finish = %v, want busy", err)
		}
		if !runner.Finish().IsZero() || runner.Done() != nil {
			t.Fatal("empty batch did not return to idle without a capture attempt")
		}
	}
	for range 2 {
		if !runner.Close().IsZero() {
			t.Fatal("idle close returned a capture attempt")
		}
	}
	if err := runner.Submit(nil); !errors.Is(err, errActionRunnerClosed) {
		t.Fatalf("submission after close = %v, want closed", err)
	}
}

func TestActionRunnerCancellation(t *testing.T) {
	for _, method := range []string{"cancel", "invalidate"} {
		t.Run(method, func(t *testing.T) {
			batch := newRunnerActionBatchForTest(t)
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			releaseSnapshot := sync.OnceFunc(func() { close(release) })
			batch.ops.snapshotProcessMemory = func(ctx context.Context, _ memsnapshot.ProcessInstanceID, _ collector.Options) (*collector.Result, error) {
				started <- ctx
				<-ctx.Done()
				<-release
				return nil, ctx.Err()
			}
			runner := newActionRunner(t.Context(), batch.config, batch.source, batch.ops, new(time.Time))
			t.Cleanup(func() { releaseSnapshot(); runner.Close() })
			if err := runner.Submit(batch.targets); err != nil {
				t.Fatal(err)
			}
			ctx := waitActionContextForTest(t, started)
			runner.CancelIfInvalidated([]memoryWatchRegistrationID{3})
			if ctx.Err() != nil {
				t.Fatal("unrelated invalidation canceled the batch")
			}
			if method == "invalidate" {
				// The first target belongs to the batch, even though the second won selection.
				runner.CancelIfInvalidated([]memoryWatchRegistrationID{1})
			} else {
				runner.Cancel()
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("active batch was not canceled")
			}
			if runner.Done() == nil {
				t.Fatal("cancellation marked an executing batch idle")
			}
			select {
			case <-runner.Done():
				t.Fatal("canceled batch completed before the collector returned")
			default:
			}
			if err := runner.Submit(batch.targets); !errors.Is(err, errActionRunnerBusy) {
				t.Fatalf("submission during cancellation = %v, want busy", err)
			}
			releaseSnapshot()
			waitActionRunnerForTest(t, runner)
			if !runner.Finish().IsZero() {
				t.Fatal("canceled capture started cooldown")
			}

			batch.targets = batch.targets[1:]
			if err := runner.Submit(batch.targets); err != nil {
				t.Fatal(err)
			}
			ctx = waitActionContextForTest(t, started)
			runner.CancelIfInvalidated([]memoryWatchRegistrationID{1})
			if ctx.Err() != nil {
				t.Fatal("previous batch registration canceled the next batch")
			}
			runner.Cancel()
			waitActionRunnerForTest(t, runner)
			runner.Finish()
		})
	}
}

func TestActionRunnerCloseRetainsCompletedAttempt(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			var lastAttempt time.Time
			batch := newRunnerActionBatchForTest(t)
			batch.config.MemoryThresholdSnapshot.IntervalTracing = 3600
			started := make(chan context.Context, 1)
			batch.ops.snapshotProcessMemory = func(ctx context.Context, _ memsnapshot.ProcessInstanceID, _ collector.Options) (*collector.Result, error) {
				started <- ctx
				if canceled {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return &collector.Result{SnapshotStartedAt: time.Now()}, nil
			}
			runner := newActionRunner(t.Context(), batch.config, batch.source, batch.ops, &lastAttempt)
			t.Cleanup(func() { runner.Close() })
			if err := runner.Submit(batch.targets); err != nil {
				t.Fatal(err)
			}
			waitActionContextForTest(t, started)
			if !canceled {
				waitActionRunnerForTest(t, runner)
			}
			if finished := runner.Close(); finished.IsZero() != canceled {
				t.Fatalf("close completion = %s, canceled = %t", finished, canceled)
			}
			if runner.Done() != nil || !runner.Close().IsZero() {
				t.Fatal("close retained the active batch or repeated its completion")
			}
			next := newActionRunner(t.Context(), batch.config, batch.source, batch.ops, &lastAttempt)
			defer next.Close()
			if next.Allowed(time.Now()) != canceled {
				t.Fatal("runner replacement lost the completed attempt cooldown")
			}
		})
	}
}

func TestActionRunnerSelectsHighestPressureAndTracksCooldown(t *testing.T) {
	var lastAttempt time.Time
	batch := newRunnerActionBatchForTest(t)
	batch.config.MemoryThresholdSnapshot.IntervalTracing = 60
	ops, selected := newActionBatchOpsForTest(t)
	batch.ops = ops
	runner := newActionRunner(t.Context(), batch.config, batch.source, batch.ops, &lastAttempt)
	t.Cleanup(func() { runner.Close() })
	if !runner.Allowed(time.Now()) {
		t.Fatal("new runner started in cooldown")
	}
	if err := runner.Submit(batch.targets); err != nil {
		t.Fatal(err)
	}
	waitActionRunnerForTest(t, runner)
	finished := runner.Finish()
	if finished.IsZero() || !lastAttempt.Equal(finished) {
		t.Fatal("completed attempt did not update shared cooldown")
	}
	if got := <-selected; got != batch.targets[1].Cgroup.Path {
		t.Fatalf("selected %s instead of the highest pressure target", got)
	}
	if runner.Allowed(finished.Add(time.Minute-time.Nanosecond)) || !runner.Allowed(finished.Add(time.Minute)) {
		t.Fatal("cooldown boundary does not match the configured interval")
	}
	if err := runner.Submit(nil); err != nil {
		t.Fatal(err)
	}
	waitActionRunnerForTest(t, runner)
	if !runner.Finish().IsZero() || !lastAttempt.Equal(finished) {
		t.Fatal("skipped batch changed the completed attempt time")
	}
	runner.Close()
	config := &Config{}
	config.MemoryThresholdSnapshot.IntervalTracing = 120
	next := newActionRunner(t.Context(), config, batch.source, batch.ops, &lastAttempt)
	defer next.Close()
	if next.Allowed(finished.Add(time.Minute)) || !next.Allowed(finished.Add(2*time.Minute)) {
		t.Fatal("replacement runner did not apply its interval to the retained attempt")
	}
}

func TestActionRunnerMergedInvalidationsCancelSnapshot(t *testing.T) {
	for _, invalidatedBy := range []string{"container", "memory"} {
		t.Run(invalidatedBy, func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			first, second := "/"+strings.Repeat("a", 64), "/"+strings.Repeat("b", 64)
			for _, path := range []string{first, second} {
				createMemoryCgroupForTest(t, source.root, path, 95)
				if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
					t.Fatal(err)
				}
			}
			active := tracker.containers[containerRefForTest(first).Key.ID]
			if invalidatedBy == "memory" {
				active = tracker.containers[containerRefForTest(second).Key.ID]
			}
			memoryID := tracker.containers[containerRefForTest(second).Key.ID].registrationID
			var lastAttempt time.Time
			ops, _ := newActionBatchOpsForTest(t)
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			releaseSnapshot := sync.OnceFunc(func() { close(release) })
			ops.snapshotProcessMemory = func(ctx context.Context, _ memsnapshot.ProcessInstanceID, _ collector.Options) (*collector.Result, error) {
				started <- ctx
				<-ctx.Done()
				<-release
				return nil, ctx.Err()
			}
			config := &Config{}
			config.MemoryThresholdSnapshot.ThresholdPercent = 90
			config.MemoryThresholdSnapshot.IntervalTracing = 3600
			runner := newActionRunner(t.Context(), config, source, ops, &lastAttempt)
			t.Cleanup(func() { releaseSnapshot(); runner.Close() })
			if err := runner.Submit([]memoryEventObservation{
				{RegistrationID: active.registrationID, Cgroup: active.cgroup, Container: containerRefForTest(active.cgroup.Path)},
			}); err != nil {
				t.Fatal(err)
			}
			ctx := waitActionContextForTest(t, started)
			update := applyContainerEventsForTest(t, tracker, pod.ContainerEvent{
				Kind: pod.ContainerDeleted, Container: containerRefForTest(first),
			})
			invalidated := append([]memoryWatchRegistrationID(nil), update.Invalidated...)
			update, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
				{Kind: memoryTargetRemoved, TargetID: memoryID},
			}, memoryEventOptions{})
			if err != nil {
				t.Fatal(err)
			}
			invalidated = append(invalidated, update.Invalidated...)
			runner.CancelIfInvalidated(invalidated)
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("merged %s invalidation did not cancel capture", invalidatedBy)
			}
			closed := make(chan struct{})
			go func() { runner.Close(); close(closed) }()
			t.Cleanup(func() { releaseSnapshot(); <-closed })
			select {
			case <-closed:
				t.Fatal("runner closed before the collector returned")
			default:
			}
			releaseSnapshot()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("runner did not join capture")
			}
			if !lastAttempt.IsZero() {
				t.Fatal("invalidated capture started cooldown")
			}
		})
	}
}

func TestActionRunnerRevalidatesSubmittedContainer(t *testing.T) {
	batch := newRunnerActionBatchForTest(t)
	isCurrent, snapshotCalls := true, 0
	batch.ops.validateContainer = func(pod.ContainerRef) error {
		if !isCurrent {
			return errors.New("container instance was replaced")
		}
		return nil
	}
	batch.ops.snapshotProcessMemory = func(context.Context, memsnapshot.ProcessInstanceID, collector.Options) (*collector.Result, error) {
		snapshotCalls++
		return &collector.Result{SnapshotStartedAt: time.Now()}, nil
	}
	runner := newActionRunner(t.Context(), batch.config, batch.source, batch.ops, new(time.Time))
	t.Cleanup(func() { runner.Close() })
	if err := runner.Submit(batch.targets[:1]); err != nil {
		t.Fatal(err)
	}
	waitActionRunnerForTest(t, runner)
	runner.Finish()
	if snapshotCalls != 1 {
		t.Fatal("current container was not captured")
	}
	isCurrent = false
	if err := runner.Submit(batch.targets[1:]); err != nil {
		t.Fatal(err)
	}
	waitActionRunnerForTest(t, runner)
	runner.Finish()
	if snapshotCalls != 1 {
		t.Fatal("stale container was captured")
	}
}

func TestActionRunnerParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := newActionRunner(ctx, &Config{}, nil, nil, new(time.Time))
	t.Cleanup(func() { runner.Close() })
	defer cancel()
	cancel()
	if err := runner.Submit(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("submission after parent cancellation = %v", err)
	}
	if runner.Done() != nil || !runner.Close().IsZero() {
		t.Fatal("rejected submission created an active batch")
	}
}

func waitActionRunnerForTest(t *testing.T, runner *actionRunner) {
	t.Helper()
	select {
	case <-runner.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("action batch did not complete")
	}
}

func waitActionContextForTest(t *testing.T, started <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-started:
		return ctx
	case <-time.After(3 * time.Second):
		t.Fatal("action capture did not start")
		return nil
	}
}

func newRunnerActionBatchForTest(t *testing.T) actionBatch {
	t.Helper()
	source := newTestCgroupSource(t)
	first, second := "/"+strings.Repeat("a", 64), "/"+strings.Repeat("b", 64)
	createMemoryCgroupForTest(t, source.root, first, 95)
	createMemoryCgroupForTest(t, source.root, second, 99)
	config := &Config{}
	config.MemoryThresholdSnapshot.ThresholdPercent = 90
	ops := &actionBatchOps{
		validateContainer: validateActionContainerForTest,
		validateProcess:   validateActionProcessForTest,
		save:              func(*tracing.WriteRequest) error { return nil },
		selectProcess: func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
			return selectedProcess{instance: memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}}, nil
		},
	}
	return newActionBatch(t.Context(), config, source, []memoryEventObservation{
		{RegistrationID: 1, Cgroup: cgroupRefForTest(t, source, first), Container: containerRefForTest(first)},
		{RegistrationID: 2, Cgroup: cgroupRefForTest(t, source, second), Container: containerRefForTest(second)},
	}, ops)
}

func TestActionBatchRanksAllReadableCgroupTargets(t *testing.T) {
	source := newTestCgroupSource(t)
	inputs := []struct {
		path         string
		usage, limit uint64
	}{
		{path: "/below-threshold", usage: 80, limit: 100},
		{path: "/smaller", usage: 95, limit: 100},
		{path: "/larger-b", usage: 190, limit: 200},
		{path: "/zero-usage", usage: 0, limit: 100},
		{path: "/over-limit", usage: 101, limit: 100},
		{path: "/larger-a", usage: 190, limit: 200},
		{path: "/at-threshold", usage: 90, limit: 100},
	}
	targets := make([]memoryEventObservation, 0, len(inputs)+1)
	for i := range inputs {
		input := &inputs[i]
		createMemoryCgroupForTest(t, source.root, input.path, input.usage)
		for _, name := range []string{"memory.max", "memory.limit_in_bytes"} {
			writeMemoryEventsForTest(t, filepath.Join(source.memcgDir(input.path), name), strconv.FormatUint(input.limit, 10))
		}
		targets = append(targets, memoryEventObservation{Cgroup: cgroupRefForTest(t, source, input.path)})
	}
	// A failed read must not prevent the other targets from being ranked.
	targets = append(targets, memoryEventObservation{Cgroup: cgroupRef{Path: "/missing"}})
	config := &Config{}
	config.MemoryThresholdSnapshot.ThresholdPercent = 90
	batch := newActionBatch(t.Context(), config, source, targets, nil)
	candidates, err := batch.rankCgroupTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/over-limit", "/larger-a", "/larger-b", "/smaller", "/at-threshold", "/below-threshold", "/zero-usage"}
	got := make([]string, len(candidates))
	for i := range candidates {
		got[i] = candidates[i].observation.Cgroup.Path
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ranked targets = %v, want %v", got, want)
	}
	for i := range inputs {
		if batch.targets[i].Cgroup.Path != inputs[i].path {
			t.Fatal("ranking changed the batch's observations")
		}
	}
}

func TestActionBatchSkipsLimitsChangedBeforeRanking(t *testing.T) {
	for _, test := range []struct {
		name, v1Limit, v2Limit string
	}{
		{name: "zero", v1Limit: "0", v2Limit: "0"},
		{name: "unlimited", v1Limit: "9223372036854771712", v2Limit: "max"},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch := newRunnerActionBatchForTest(t)
			batch.targets = batch.targets[:1]
			directory := batch.source.memcgDir(batch.targets[0].Cgroup.Path)
			// The batch was admitted with a finite limit, which is no longer current.
			writeMemoryEventsForTest(t, filepath.Join(directory, "memory.limit_in_bytes"), test.v1Limit)
			writeMemoryEventsForTest(t, filepath.Join(directory, "memory.max"), test.v2Limit)
			candidates, err := batch.rankCgroupTargets(t.Context())
			if err != nil || len(candidates) != 0 {
				t.Fatalf("changed limit produced %d candidates: %v", len(candidates), err)
			}
			if !batch.Run().IsZero() {
				t.Fatal("unlimited target started a capture attempt")
			}
		})
	}
}

func TestActionBatchRankingFailure(t *testing.T) {
	batch := newRunnerActionBatchForTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if candidates, err := batch.rankCgroupTargets(ctx); !errors.Is(err, context.Canceled) || len(candidates) != 0 {
		t.Fatalf("canceled ranking returned %d candidates: %v", len(candidates), err)
	}
	batch.targets = []memoryEventObservation{{Cgroup: cgroupRef{Path: "/missing"}}}
	if candidates, err := batch.rankCgroupTargets(t.Context()); !errors.Is(err, os.ErrNotExist) || len(candidates) != 0 {
		t.Fatalf("failed reads returned %d candidates: %v", len(candidates), err)
	}
	batch.targets = nil
	if candidates, err := batch.rankCgroupTargets(t.Context()); err != nil || len(candidates) != 0 {
		t.Fatalf("empty batch returned %d candidates: %v", len(candidates), err)
	}
}

func TestActionBatchSnapshotsFirstAndLogsRemaining(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			batch := newRunnerActionBatchForTest(t)
			third := "/" + strings.Repeat("c", 64)
			createMemoryCgroupForTest(t, batch.source.root, third, 80)
			batch.targets = append(batch.targets, memoryEventObservation{
				RegistrationID: 3, Cgroup: cgroupRefForTest(t, batch.source, third), Container: containerRefForTest(third),
			})
			var selected []string
			snapshotCalls := 0
			batch.ops.selectProcess = func(_ context.Context, group cgroupRef, _ uint64) (selectedProcess, error) {
				selected = append(selected, group.Path)
				return selectedProcess{instance: memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}}, nil
			}
			batch.ops.snapshotProcessMemory = func(context.Context, memsnapshot.ProcessInstanceID, collector.Options) (*collector.Result, error) {
				snapshotCalls++
				if failed {
					return nil, errors.New("capture failed")
				}
				return &collector.Result{SnapshotStartedAt: time.Now()}, nil
			}
			var logs bytes.Buffer
			level := log.GetLevel()
			log.SetLevel("info")
			log.SetOutput(&logs)
			t.Cleanup(func() {
				log.SetOutput(os.Stdout)
				log.SetLevel(level.String())
			})
			if batch.Run().IsZero() {
				t.Fatal("ranked batch did not attempt capture")
			}
			if snapshotCalls != 1 || !slices.Equal(selected, []string{batch.targets[1].Cgroup.Path}) {
				t.Fatalf("captures = %d, selected = %v; want only the highest usage ratio target", snapshotCalls, selected)
			}
			var remaining []string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "memory threshold snapshot candidate not selected") {
					remaining = append(remaining, line)
				}
			}
			if len(remaining) != 2 {
				t.Fatalf("logged %d unselected candidates, want 2: %s", len(remaining), logs.String())
			}
			for i, targetIndex := range []int{0, 2} {
				for _, field := range []string{
					`rank="` + strconv.Itoa(i+2) + `"`,
					`container="` + batch.targets[targetIndex].Container.Key.ID + `"`,
					`cgroup="` + batch.targets[targetIndex].Cgroup.Path + `"`,
					`limit_bytes="100"`,
				} {
					if !strings.Contains(remaining[i], field) {
						t.Fatalf("ranked candidate log lacks %s: %s", field, remaining[i])
					}
				}
				usage := []string{"95", "80"}[i]
				if !strings.Contains(remaining[i], `usage_bytes="`+usage+`"`) ||
					!strings.Contains(remaining[i], `usage_percent="`+usage+`"`) {
					t.Fatalf("ranked candidate log lacks memory usage: %s", remaining[i])
				}
			}
		})
	}
}

func TestActionBatchCompletion(t *testing.T) {
	for _, test := range []struct {
		name        string
		usage       uint64
		missing     bool
		canceled    bool
		wantAttempt bool
	}{
		{name: "below threshold", usage: 89},
		{name: "missing target", usage: 95, missing: true},
		{name: "canceled", usage: 95, canceled: true},
		{name: "captured", usage: 95, wantAttempt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := newTestCgroupSource(t)
			path := "/" + strings.Repeat("a", 64)
			createMemoryCgroupForTest(t, source.root, path, test.usage)
			target := cgroupRefForTest(t, source, path)
			if test.missing {
				target.Path = "/missing"
			}
			ops, selected := newActionBatchOpsForTest(t)
			cfg := &Config{}
			cfg.MemoryThresholdSnapshot.ThresholdPercent = 90
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			batch := newActionBatch(ctx, cfg, source,
				[]memoryEventObservation{{Cgroup: target, Container: containerRefForTest(path)}}, ops)
			if test.canceled {
				cancel()
			}
			if finished := batch.Run(); finished.IsZero() == test.wantAttempt {
				t.Fatalf("completion = %s, want attempt = %t", finished, test.wantAttempt)
			}
			select {
			case got := <-selected:
				if !test.wantAttempt || got != path {
					t.Fatalf("unexpected capture target %q", got)
				}
			default:
				if test.wantAttempt {
					t.Fatal("eligible batch did not attempt capture")
				}
			}
		})
	}
}

func TestActionBatchRevalidatesContainerBeforePersistence(t *testing.T) {
	for _, test := range []struct {
		name              string
		hasChangedBinding bool
		status            memsnapshot.Status
	}{
		{name: "complete", status: memsnapshot.SnapshotStatusComplete},
		{name: "complete with changed binding", hasChangedBinding: true, status: memsnapshot.SnapshotStatusComplete},
		{name: "failed", status: memsnapshot.SnapshotStatusFailed},
		{name: "failed with changed binding", hasChangedBinding: true, status: memsnapshot.SnapshotStatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := test.hasChangedBinding
			outputDir := t.TempDir()
			store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{
				LocalFile: &tracingstore.LocalFileConfig{
					Path: outputDir, RotationSizeMiB: 1, MaxRotatedFiles: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(t.Context()); err != nil {
					t.Error(err)
				}
			})
			if err := tracing.EnableDocumentWriter(store, document.New("test")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tracing.DisableDocumentWriter)

			source := newTestCgroupSource(t)
			id := strings.Repeat("a", 64)
			original := "/" + id
			createMemoryCgroupForTest(t, source.root, original, 95)
			target := cgroupRefForTest(t, source, original)
			path, saved := original, false
			process := memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}
			rssBytes := uint64(4096)
			result := &collector.Result{
				Process: process, Language: memsnapshot.LanguageGo,
				Snapshot:      &memsnapshot.Snapshot{Status: test.status},
				ProcessMemory: &memsnapshot.ProcessMemory{Status: memsnapshot.SnapshotStatusComplete, RSSBytes: &rssBytes},
			}
			if test.status == memsnapshot.SnapshotStatusFailed {
				result.Snapshot.StatusReason = "read memory: permission denied"
			}
			ops := &actionBatchOps{
				validateContainer: func(ref pod.ContainerRef) error {
					if path != original || ref != containerRefForTest(original) {
						return errors.New("container binding changed")
					}
					return nil
				},
				selectProcess: func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
					return selectedProcess{instance: process}, nil
				},
				validateProcess: func(_ context.Context, group cgroupRef, actual memsnapshot.ProcessInstanceID) error {
					if !group.SameInstance(target) || actual != process {
						t.Fatal("selected identity or cgroup was replaced")
					}
					return nil
				},
				snapshotProcessMemory: func(ctx context.Context, actual memsnapshot.ProcessInstanceID, options collector.Options) (*collector.Result, error) {
					if actual != process {
						t.Fatalf("collector identity = %+v, want %+v", actual, process)
					}
					if options.MaxMemoryObjectEntries != 7 {
						t.Fatalf("collector maximum memory object entries = %d, want 7", options.MaxMemoryObjectEntries)
					}
					if options.SnapshotTimeout != 3*time.Second {
						t.Fatalf("collector capture timeout = %s, want 3s", options.SnapshotTimeout)
					}
					result.SnapshotStartedAt = time.Now().UTC()
					if changed {
						path = "/replacement"
					}
					return result, nil
				},
				save: func(req *tracing.WriteRequest) error {
					saved = true
					if req.TracerName != "memory_threshold_snapshot" {
						t.Fatalf("tracer name = %q", req.TracerName)
					}
					data := req.TracerData.(*memoryThresholdSnapshotData)
					if data.Snapshot != result.Snapshot || data.Language != result.Language ||
						data.ProcessMemory != result.ProcessMemory ||
						!req.ObservedTimestamp.Equal(result.SnapshotStartedAt) {
						t.Fatalf("saved result = %+v", data)
					}
					if req.ContainerID != id {
						t.Fatalf("saved container = %q, want %q", req.ContainerID, id)
					}
					// This filesystem fixture has no runtime container metadata.
					request := *req
					request.ContainerID = ""
					return tracing.Save(&request)
				},
			}
			cfg := &Config{}
			cfg.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 7
			cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = 3
			before := time.Now().UTC()
			batch := newActionBatch(t.Context(), cfg, source, nil, ops)
			err = batch.snapshotCandidate(t.Context(), &memcgCandidate{observation: &memoryEventObservation{Cgroup: target, Container: containerRefForTest(original)}})
			if changed && (err == nil || saved) {
				t.Fatalf("changed container path persisted: error=%v saved=%v", err, saved)
			}
			if !changed && (err != nil || !saved) {
				t.Fatalf("unchanged container not persisted: error=%v saved=%v", err, saved)
			}
			if err := store.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(outputDir, memoryThresholdSnapshotTracer))
			if changed {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("changed container snapshot exists: error=%v data=%s", err, raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct {
				types.Document
				TracerData memoryThresholdSnapshotData `json:"tracer_data"`
			}
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if err := persisted.Validate(); err != nil {
				t.Fatalf("persisted document is invalid: %v", err)
			}
			if persisted.TracerRunType != types.TracerRunTypeAutotracing {
				t.Fatalf("tracer type = %q, want autotracing", persisted.TracerRunType)
			}
			if persisted.StartedTimestamp == nil || persisted.StartedTimestamp.Before(before) ||
				persisted.StartedTimestamp.After(result.SnapshotStartedAt) {
				t.Fatalf("started timestamp = %v, want between %s and %s",
					persisted.StartedTimestamp, before, result.SnapshotStartedAt)
			}
			if persisted.ObservedTimestamp == nil || !persisted.ObservedTimestamp.Equal(result.SnapshotStartedAt) {
				t.Fatalf("observed timestamp = %v, want %s", persisted.ObservedTimestamp, result.SnapshotStartedAt)
			}
			if persisted.TracerName != memoryThresholdSnapshotTracer || persisted.TracerData.VictimPID != 42 ||
				persisted.TracerData.Snapshot == nil || persisted.TracerData.Snapshot.Status != test.status ||
				persisted.TracerData.Snapshot.StatusReason != result.Snapshot.StatusReason {
				t.Fatalf("persisted snapshot = %+v", persisted)
			}
			memory := persisted.TracerData.ProcessMemory
			if memory == nil || memory.RSSBytes == nil || *memory.RSSBytes != rssBytes {
				t.Fatalf("persisted process memory = %+v", memory)
			}
		})
	}
}

func newActionBatchOpsForTest(t *testing.T) (*actionBatchOps, <-chan string) {
	t.Helper()
	selected := make(chan string, 1)
	ops := &actionBatchOps{
		validateContainer: validateActionContainerForTest,
		validateProcess:   validateActionProcessForTest,
		save:              func(*tracing.WriteRequest) error { return nil },
		selectProcess: func(ctx context.Context, group cgroupRef, _ uint64) (selectedProcess, error) {
			select {
			case selected <- group.Path:
				return selectedProcess{instance: memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}}, nil
			case <-ctx.Done():
				return selectedProcess{}, ctx.Err()
			}
		},
		snapshotProcessMemory: func(context.Context, memsnapshot.ProcessInstanceID, collector.Options) (*collector.Result, error) {
			return &collector.Result{SnapshotStartedAt: time.Now()}, nil
		},
	}
	return ops, selected
}

func validateActionContainerForTest(pod.ContainerRef) error {
	return nil
}

func validateActionProcessForTest(context.Context, cgroupRef, memsnapshot.ProcessInstanceID) error {
	return nil
}

func TestActionBatchValidatesBoundContainerAndProcess(t *testing.T) {
	for _, change := range []string{"unchanged", "container deleted", "generation changed", "view unavailable", "pid reused", "process moved", "directory replaced"} {
		t.Run(change, func(t *testing.T) {
			batch := newRunnerActionBatchForTest(t)
			observation := &batch.targets[1]
			ref := observation.Container
			process := memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}
			selector := &processSelector{source: batch.source, procRoot: snapshotProcRootForTest(t)}
			writeProcessForTest(t, selector.procRoot, process, 100, 0)
			members := filepath.Join(batch.source.memcgDir(observation.Cgroup.Path), "cgroup.procs")
			writeMemoryEventsForTest(t, members, "42\n")
			collected, saved := false, false
			batch.ops.selectProcess = func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
				if change == "container deleted" {
					t.Fatal("deleted container reached process selection")
				}
				return selectedProcess{instance: process}, nil
			}
			batch.ops.validateContainer = func(current pod.ContainerRef) error {
				if current != ref {
					t.Fatal("capture lost the bound container instance")
				}
				if change == "container deleted" || (collected && change == "generation changed") {
					return errors.New("container instance changed")
				}
				if collected && change == "view unavailable" {
					return pod.ErrContainersUnavailable
				}
				return nil
			}
			batch.ops.validateProcess = func(ctx context.Context, group cgroupRef, actual memsnapshot.ProcessInstanceID) error {
				if !group.SameInstance(observation.Cgroup) || actual != process {
					t.Fatal("validation lost selected process identity or cgroup")
				}
				return selector.Validate(ctx, group, actual)
			}
			batch.ops.snapshotProcessMemory = func(ctx context.Context, actual memsnapshot.ProcessInstanceID, options collector.Options) (*collector.Result, error) {
				if actual != process {
					t.Fatal("collector lost selected process identity")
				}
				collected = true
				switch change {
				case "pid reused":
					replacement := process
					replacement.StartTimeTicks++
					writeProcessForTest(t, selector.procRoot, replacement, 100, 0)
				case "process moved":
					writeMemoryEventsForTest(t, members, "")
				case "directory replaced":
					if err := os.Rename(batch.source.memcgDir(observation.Cgroup.Path), filepath.Join(batch.source.root, "old")); err != nil {
						t.Fatal(err)
					}
					createMemoryCgroupForTest(t, batch.source.root, observation.Cgroup.Path, 99)
				}
				result := &collector.Result{SnapshotStartedAt: time.Now()}
				return result, nil
			}
			batch.ops.save = func(req *tracing.WriteRequest) error {
				saved = true
				if req.ContainerID != ref.Key.ID {
					t.Fatalf("saved container = %q; want bound container %q", req.ContainerID, ref.Key.ID)
				}
				return nil
			}
			err := batch.snapshotCandidate(t.Context(), &memcgCandidate{observation: observation})
			if change == "unchanged" {
				if err != nil || !saved {
					t.Fatalf("capture = saved:%t error:%v", saved, err)
				}
			} else if err == nil || saved {
				t.Fatalf("invalid capture persisted: %v", err)
			}
		})
	}
}

func TestActionRunnerDirectoryReplacementCancelsSnapshot(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/container"
	createMemoryCgroupForTest(t, source.root, path, 99)
	first := containerRefForTest(path)
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: first})
	id := tracker.containers[containerRefForTest(path).Key.ID].registrationID
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: id}}, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	ops, _ := newActionBatchOpsForTest(t)
	started := make(chan context.Context, 1)
	ops.snapshotProcessMemory = func(ctx context.Context, _ memsnapshot.ProcessInstanceID, _ collector.Options) (*collector.Result, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runner := newActionRunner(t.Context(), &Config{}, source, ops, new(time.Time))
	t.Cleanup(func() { runner.Close() })
	if err := runner.Submit(tracker.DrainPending()); err != nil {
		t.Fatal(err)
	}
	ctx := waitActionContextForTest(t, started)
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 99)
	second := first
	second.Key.ID = "second"
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: second})
	update := waitMemoryInvalidationForTest(t, tracker, id)
	runner.CancelIfInvalidated(update.Invalidated)
	if ctx.Err() == nil || tracker.containers[first.Key.ID].registrationID != 0 || len(tracker.targets) != 1 ||
		tracker.containers[second.Key.ID].registrationID == 0 {
		t.Fatal("directory replacement did not cancel old capture while retaining the new watch")
	}
	waitActionRunnerForTest(t, runner)
	if !runner.Finish().IsZero() {
		t.Fatal("replacement cancellation started cooldown")
	}
}

func TestActionBatchSnapshotSequenceAndFailures(t *testing.T) {
	for _, stage := range []string{"success", "cancel before selection", "select", "capture", "cancel after capture", "save"} {
		t.Run(stage, func(t *testing.T) {
			batch := newRunnerActionBatchForTest(t)
			observation := &batch.targets[1]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			process := memsnapshot.ProcessInstanceID{TGID: 42, StartTimeTicks: 100}
			failure := errors.New("operation failed")
			var calls []string
			var selectionDone <-chan struct{}
			batch.ops.validateContainer = func(pod.ContainerRef) error {
				if ctx.Err() != nil {
					t.Fatal("canceled capture reached container validation")
				}
				return nil
			}
			batch.ops.selectProcess = func(ctx context.Context, group cgroupRef, limit uint64) (selectedProcess, error) {
				calls = append(calls, "select")
				selectionDone = ctx.Done()
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > processSelectionTimeout ||
					!group.SameInstance(observation.Cgroup) || limit != 100 {
					t.Fatal("selection lost its budget, cgroup instance or memory limit")
				}
				if stage == "select" {
					return selectedProcess{}, failure
				}
				return selectedProcess{instance: process, comm: "worker", oomScoreAdj: 10}, nil
			}
			batch.ops.snapshotProcessMemory = func(snapshotCtx context.Context, actual memsnapshot.ProcessInstanceID,
				_ collector.Options,
			) (*collector.Result, error) {
				calls = append(calls, "capture")
				select {
				case <-selectionDone:
				default:
					t.Fatal("selection context was not released before capture")
				}
				if snapshotCtx != ctx || snapshotCtx.Err() != nil {
					t.Fatal("selection cancellation propagated to capture")
				}
				if actual != process {
					t.Fatal("capture lost the selected process identity")
				}
				if stage == "capture" {
					return nil, failure
				}
				if stage == "cancel after capture" {
					cancel()
				}
				return &collector.Result{Process: process, SnapshotStartedAt: time.Now()}, nil
			}
			batch.ops.save = func(req *tracing.WriteRequest) error {
				calls = append(calls, "save")
				data := req.TracerData.(*memoryThresholdSnapshotData)
				if data.VictimPID != process.TGID || data.VictimProcessName != "worker" || data.VictimOOMScoreAdj != 10 {
					t.Fatalf("save lost selected process metadata: %+v", data)
				}
				if stage == "save" {
					return failure
				}
				return nil
			}
			if stage == "cancel before selection" {
				cancel()
			}
			err := batch.snapshotCandidate(ctx, &memcgCandidate{observation: observation, current: 99, max: 100, ratio: 0.99})
			if selectionDone != nil {
				select {
				case <-selectionDone:
				default:
					t.Fatal("selection context was not released after return")
				}
			}
			wantCalls := []string{"select", "capture", "save"}
			wantErr := failure
			switch stage {
			case "success":
				wantErr = nil
			case "cancel before selection":
				wantCalls = nil
				wantErr = context.Canceled
			case "select":
				wantCalls = wantCalls[:1]
			case "capture":
				wantCalls = wantCalls[:2]
			case "cancel after capture":
				wantCalls = wantCalls[:2]
				wantErr = context.Canceled
			}
			if !slices.Equal(calls, wantCalls) || !errors.Is(err, wantErr) {
				t.Fatalf("calls=%v err=%v, want calls=%v err=%v", calls, err, wantCalls, wantErr)
			}
		})
	}
}
