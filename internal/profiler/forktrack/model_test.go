// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"sync"
	"testing"
	"time"
)

func newTestModel(t *testing.T, config Config) *Model {
	t.Helper()
	model, err := NewModel(config)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestModelTracksDescendantsAfterRootAndParentExit(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 100, MaxTracked: 10})
	now := time.Unix(100, 0)
	child := model.Fork(100, 101, now)
	grandchild := model.Fork(101, 102, now.Add(time.Millisecond))
	if !child.Accepted || !grandchild.Accepted {
		t.Fatalf("fork decisions = %+v, %+v", child, grandchild)
	}
	if grandchild.Process.Generation != 2 || grandchild.Process.RootPID != 100 {
		t.Fatalf("grandchild inheritance = %+v", grandchild.Process)
	}

	// The root is implicit, so its exit must not erase its descendants.
	if model.Exit(100) {
		t.Fatal("root unexpectedly occupied a descendant map entry")
	}
	if model.Tracked(100) {
		t.Fatal("exited root remains traceable and is vulnerable to PID reuse")
	}
	if !model.Tracked(101) || !model.Tracked(102) {
		t.Fatal("root exit stopped descendant tracking")
	}
	if !model.Exit(101) {
		t.Fatal("child exit was not cleaned up")
	}
	if model.Tracked(101) || !model.Tracked(102) {
		t.Fatal("parent cleanup incorrectly removed surviving grandchild")
	}
	if err := model.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	stats := model.Stats()
	if stats.Active != 1 || stats.Accepted != 2 || stats.Exited != 1 || stats.DeepestGeneration != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestModelRecognizesPreexistingRootWorker(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 200, MaxTracked: 10})
	now := time.Unix(110, 0)
	decision := model.ForkFrom(205, 200, 210, now)
	if !decision.Accepted || decision.Process.Generation != 1 {
		t.Fatalf("pre-existing root worker fork = %+v", decision)
	}
	grandchild := model.ForkFrom(215, 210, 220, now)
	if !grandchild.Accepted || grandchild.Process.Generation != 2 {
		t.Fatalf("tracked process worker fork = %+v", grandchild)
	}
	model.Exit(200)
	if decision := model.ForkFrom(205, 200, 230, now); decision.Reason != RejectUntracked {
		t.Fatalf("fork from reused root identity = %+v", decision)
	}
}

func TestModelRejectsForkStormByRate(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 10, MaxTracked: 100, Rate: 2, Burst: 1})
	start := time.Unix(50, 0)
	for child := uint32(11); child <= 13; child++ {
		if decision := model.Fork(10, child, start); !decision.Accepted {
			t.Fatalf("child %d unexpectedly rejected: %+v", child, decision)
		}
	}
	if decision := model.Fork(10, 14, start); decision.Reason != RejectRate {
		t.Fatalf("fourth event = %+v, want rate rejection", decision)
	}
	if decision := model.Fork(10, 15, start.Add(Window)); !decision.Accepted {
		t.Fatalf("new window event rejected: %+v", decision)
	}
	stats := model.Stats()
	if stats.RejectedRate != 1 || stats.Accepted != 4 || stats.WindowEvents != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestModelEnforcesCapacityAndRecoversAfterExit(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 20, MaxTracked: 2})
	now := time.Unix(60, 0)
	model.Fork(20, 21, now)
	model.Fork(20, 22, now)
	if decision := model.Fork(20, 23, now); decision.Reason != RejectLimit {
		t.Fatalf("third child = %+v, want capacity rejection", decision)
	}
	model.Exit(21)
	if decision := model.Fork(20, 23, now); !decision.Accepted {
		t.Fatalf("capacity did not recover after exit: %+v", decision)
	}
	if err := model.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestModelRejectsUnknownParentAndDuplicate(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 30, MaxTracked: 4})
	now := time.Unix(70, 0)
	if decision := model.Fork(999, 31, now); decision.Reason != RejectUntracked {
		t.Fatalf("unknown parent decision = %+v", decision)
	}
	first := model.Fork(30, 31, now)
	duplicate := model.Fork(30, 31, now)
	if !first.Accepted || duplicate.Reason != RejectDuplicate {
		t.Fatalf("duplicate decisions = %+v, %+v", first, duplicate)
	}
	stats := model.Stats()
	if stats.Duplicate != 1 || stats.Active != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestModelConcurrentForkAndExit(t *testing.T) {
	const children = 500
	model := newTestModel(t, Config{Enabled: true, RootPID: 40, MaxTracked: children})
	now := time.Unix(80, 0)
	var wg sync.WaitGroup
	for i := uint32(0); i < children; i++ {
		pid := uint32(1000) + i
		wg.Add(1)
		go func() {
			defer wg.Done()
			model.Fork(40, pid, now)
		}()
	}
	wg.Wait()
	if stats := model.Stats(); stats.Active != children || stats.Accepted != children {
		t.Fatalf("post-fork stats: %+v", stats)
	}
	for i := uint32(0); i < children; i += 2 {
		pid := uint32(1000) + i
		wg.Add(1)
		go func() {
			defer wg.Done()
			model.Exit(pid)
		}()
	}
	wg.Wait()
	if stats := model.Stats(); stats.Active != children/2 || stats.Exited != children/2 {
		t.Fatalf("post-exit stats: %+v", stats)
	}
	if err := model.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestModelConcurrentCapacityNeverExceeded(t *testing.T) {
	const (
		attempts = 1000
		capacity = 37
	)
	model := newTestModel(t, Config{Enabled: true, RootPID: 50, MaxTracked: capacity})
	now := time.Unix(90, 0)
	var wg sync.WaitGroup
	for i := uint32(0); i < attempts; i++ {
		pid := uint32(10_000) + i
		wg.Add(1)
		go func() {
			defer wg.Done()
			model.Fork(50, pid, now)
		}()
	}
	wg.Wait()
	stats := model.Stats()
	if stats.Active != capacity || stats.Accepted != capacity {
		t.Fatalf("capacity stats = %+v, want %d accepted", stats, capacity)
	}
	if stats.RejectedLimit != attempts-capacity {
		t.Fatalf("RejectedLimit = %d, want %d", stats.RejectedLimit, attempts-capacity)
	}
	if got := len(model.Snapshot()); got != capacity {
		t.Fatalf("snapshot length = %d, want %d", got, capacity)
	}
	if err := model.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestModelDisabledAndInvalidEvents(t *testing.T) {
	disabled := newTestModel(t, Config{RootPID: 60, MaxTracked: 1})
	if decision := disabled.Fork(60, 61, time.Now()); decision.Reason != RejectDisabled {
		t.Fatalf("disabled decision = %+v", decision)
	}
	if disabled.Tracked(61) {
		t.Fatal("disabled model tracked an unrelated PID")
	}

	enabled := newTestModel(t, Config{Enabled: true, RootPID: 60, MaxTracked: 3})
	tests := []struct {
		parent uint32
		tgid   uint32
		child  uint32
	}{
		{parent: 0, tgid: 60, child: 61},
		{parent: 60, tgid: 0, child: 61},
		{parent: 60, tgid: 60, child: 0},
		{parent: 60, tgid: 60, child: 60},
	}
	for _, test := range tests {
		if decision := enabled.ForkFrom(test.parent, test.tgid, test.child, time.Now()); decision.Reason != RejectInvalid {
			t.Errorf("ForkFrom(%d, %d, %d) = %+v", test.parent, test.tgid, test.child, decision)
		}
	}
}

func TestModelSnapshotOrderAndProcessLookup(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 70, MaxTracked: 10})
	now := time.Unix(100, 0)
	model.Fork(70, 73, now)
	model.Fork(70, 71, now)
	model.Fork(71, 72, now)
	model.Fork(73, 74, now)
	want := []uint32{71, 73, 72, 74}
	snapshot := model.Snapshot()
	if len(snapshot) != len(want) {
		t.Fatalf("snapshot length = %d, want %d", len(snapshot), len(want))
	}
	for i, pid := range want {
		if snapshot[i].PID != pid {
			t.Fatalf("snapshot[%d].PID = %d, want %d", i, snapshot[i].PID, pid)
		}
	}
	process, ok := model.Process(72)
	if !ok || process.ParentPID != 71 || process.Generation != 2 {
		t.Fatalf("Process(72) = %+v, %t", process, ok)
	}
	if _, ok := model.Process(999); ok {
		t.Fatal("unknown process found")
	}
}

func TestModelResetCountersPreservesState(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 80, MaxTracked: 10})
	now := time.Unix(110, 0)
	model.Fork(80, 81, now)
	model.Fork(80, 82, now)
	model.Exit(80)
	model.ResetCounters()
	stats := model.Stats()
	if stats.Active != 2 || stats.RootExited != 1 {
		t.Fatalf("ResetCounters state = %+v", stats)
	}
	if stats.Accepted != 0 || stats.Exited != 0 || stats.DeepestGeneration != 0 {
		t.Fatalf("ResetCounters retained event counters: %+v", stats)
	}
	if !model.Tracked(81) || !model.Tracked(82) || model.Tracked(80) {
		t.Fatal("ResetCounters changed process membership")
	}
}

func TestModelUnlimitedRate(t *testing.T) {
	const attempts = 200
	model := newTestModel(t, Config{Enabled: true, RootPID: 90, MaxTracked: attempts, Rate: 0})
	now := time.Unix(120, 0)
	for i := uint32(0); i < attempts; i++ {
		decision := model.Fork(90, 20_000+i, now)
		if !decision.Accepted {
			t.Fatalf("event %d rejected: %+v", i, decision)
		}
	}
	stats := model.Stats()
	if stats.RejectedRate != 0 || stats.Accepted != attempts || stats.WindowEvents != 0 {
		t.Fatalf("unlimited rate stats = %+v", stats)
	}
}

func TestModelThreadGenerationAndExecMigration(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 300, MaxTracked: 10})
	now := time.Unix(130, 0)
	child := model.ForkTask(300, 300, 310, 310, now)
	worker := model.ForkTask(310, 310, 311, 310, now.Add(time.Millisecond))
	if !child.Accepted || child.Process.Generation != 1 || child.Process.Thread {
		t.Fatalf("child process = %+v", child)
	}
	if !worker.Accepted || worker.Process.Generation != 1 || !worker.Process.Thread || worker.Process.ParentPID != 300 {
		t.Fatalf("child worker = %+v", worker)
	}

	// de_thread removes the old leader before sched_process_exec migrates the
	// execing worker from old_pid=311 to pid=310.
	if !model.Exit(310) {
		t.Fatal("old leader exit was not observed")
	}
	if !model.Exec(310, 311) {
		t.Fatal("worker exec was not migrated")
	}
	if model.Tracked(311) || !model.Tracked(310) {
		t.Fatal("exec migration left stale or missing keys")
	}
	migrated, ok := model.Process(310)
	if !ok || migrated.Thread || migrated.Generation != 1 || migrated.ParentPID != 300 {
		t.Fatalf("migrated process = %+v, %t", migrated, ok)
	}
	grandchild := model.Fork(310, 320, now.Add(2*time.Millisecond))
	if !grandchild.Accepted || grandchild.Process.Generation != 2 || grandchild.Process.ParentPID != 310 {
		t.Fatalf("post-exec fork = %+v", grandchild)
	}
	stats := model.Stats()
	if stats.ExecMigrations != 1 || stats.Active != 2 {
		t.Fatalf("post-exec stats = %+v", stats)
	}
	if err := model.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestModelExecCollapsesExistingLeaderAndWorker(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 400, MaxTracked: 10})
	now := time.Unix(140, 0)
	model.Fork(400, 410, now)
	model.ForkTask(410, 410, 411, 410, now)
	if !model.Exec(410, 411) {
		t.Fatal("exec collision was not handled")
	}
	if stats := model.Stats(); stats.Active != 1 || stats.ExecMigrations != 1 {
		t.Fatalf("collision stats = %+v", stats)
	}
	process, ok := model.Process(410)
	if !ok || process.Generation != 1 || process.ParentPID != 400 || process.Thread {
		t.Fatalf("leader record was not preserved: %+v, %t", process, ok)
	}
}

func TestModelRootThreadExecUsesImplicitRoot(t *testing.T) {
	model := newTestModel(t, Config{Enabled: true, RootPID: 500, MaxTracked: 10})
	now := time.Unix(150, 0)
	worker := model.ForkTask(500, 500, 501, 500, now)
	if !worker.Accepted || worker.Process.Generation != 0 || !worker.Process.Thread {
		t.Fatalf("root worker = %+v", worker)
	}
	if !model.Exec(500, 501) {
		t.Fatal("root worker exec not handled")
	}
	if model.Tracked(501) || !model.Tracked(500) {
		t.Fatal("root exec did not collapse to implicit root")
	}
	if stats := model.Stats(); stats.Active != 0 || stats.ExecMigrations != 1 {
		t.Fatalf("root exec stats = %+v", stats)
	}
}
